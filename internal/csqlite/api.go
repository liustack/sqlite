// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package csqlite

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"unsafe"

	// A Go newer than the one the engine was checked with stops the build.
	_ "github.com/liustack/sqlite/internal/goversion"
)

// The C API of SQLite for the driver. A Conn and its statements are used by
// one goroutine at a time. Strings and out-parameters of a call go on the
// connection's shadow stack, below the shadow pointer the call gets, so a
// call allocates no C memory.

// Result codes and flags of sqlite3.h.
const (
	OK   = 0
	Row  = 100
	Done = 101

	OpenReadOnly     = 0x00000001
	OpenReadWrite    = 0x00000002
	OpenCreate       = 0x00000004
	OpenURI          = 0x00000040
	OpenMemory       = 0x00000080
	OpenNoMutex      = 0x00008000
	OpenFullMutex    = 0x00010000
	OpenSharedCache  = 0x00020000
	OpenPrivateCache = 0x00040000
	OpenExResCode    = 0x02000000

	TypeInteger = 1
	TypeFloat   = 2
	TypeText    = 3
	TypeBlob    = 4
	TypeNull    = 5
)

// Error is a result code of SQLite with its message.
type Error struct {
	Code int // the extended result code
	Msg  string
	err  error // ErrCollationPanic, or nil
}

func (e *Error) Error() string { return fmt.Sprintf("sqlite: %s (%d)", e.Msg, e.Code) }

func (e *Error) Unwrap() error { return e.err }

// ErrCollationPanic is under the error of a statement in which a Go collation
// panicked.
var ErrCollationPanic = errors.New("panic in a Go collation")

// Primary is the primary result code.
func (e *Error) Primary() int { return e.Code & 0xff }

var initOnce sync.Once
var initErr error

func start() error {
	initOnce.Do(func() {
		s, err := newStack()
		if err != nil {
			initErr = err
			return
		}
		defer s.free()
		if r := int32(go_init(s.sp())); r != OK {
			initErr = &Error{Code: int(r), Msg: errstr(s.sp(), int(r))}
		}
	})
	return initErr
}

func errstr(sp uintptr, code int) string { return cstring(sqlite3_errstr(sp, uintptr(code))) }

// LibVersion is the version of the SQLite in the engine.
func LibVersion() (string, error) {
	if err := start(); err != nil {
		return "", err
	}
	s, err := newStack()
	if err != nil {
		return "", err
	}
	defer s.free()
	return cstring(sqlite3_libversion(s.sp())), nil
}

// Conn is a connection to a database.
type Conn struct {
	st *stack
	sp uintptr
	db uintptr
	// changed says a statement prepared on the connection changes it
	// (ChangesConn). SQLite applies some of them as it prepares them, such as
	// a PRAGMA that sets a flag, so this holds whatever became of the
	// statement.
	changed bool
}

// args builds the strings and out-parameters of one call on the shadow
// stack.
type args struct{ top uintptr }

func (c *Conn) args() args { return args{c.sp} }

// str copies s with a NUL after it.
func (a *args) str(s string) uintptr {
	p := a.top
	b := bytesAt(p, len(s)+1)
	copy(b, s)
	b[len(s)] = 0
	a.top = (p + uintptr(len(s)) + 1 + 15) &^ 15
	return p
}

// bytes copies b.
func (a *args) bytes(b []byte) uintptr {
	p := a.top
	copy(bytesAt(p, len(b)), b)
	a.top = (p + uintptr(len(b)) + 15) &^ 15
	return p
}

// space is room for n bytes.
func (a *args) space(n int) uintptr {
	p := a.top
	a.top = (p + uintptr(n) + 15) &^ 15
	return p
}

// word is room for one out-parameter, zeroed.
func (a *args) word() uintptr {
	p := a.top
	*(*uintptr)(at(p)) = 0
	a.top += 16
	return p
}

// big is the largest string or blob passed on the shadow stack: a larger one
// is passed from malloc'd memory.
const big = 1 << 20

// maxName is the longest file name, VFS name or schema name passed on the
// shadow stack. Real paths are a few KiB at most.
const maxName = 1 << 16

// Open opens a connection. It does not take OpenFullMutex: a connection has
// one shadow stack, so two goroutines cannot run on it at once, and a mutex
// of SQLite could not tell them apart.
func Open(name string, flags int, vfs string) (*Conn, error) {
	if flags&OpenFullMutex != 0 {
		return nil, &Error{Code: 21, Msg: "OpenFullMutex is not supported: a connection is used by one goroutine at a time"}
	}
	if len(name) > maxName || len(vfs) > maxName {
		return nil, &Error{Code: 18, Msg: "file name too long"}
	}
	if err := start(); err != nil {
		return nil, err
	}
	st, err := newStack()
	if err != nil {
		return nil, err
	}
	c := &Conn{st: st, sp: st.sp()}
	a := c.args()
	pdb := a.word()
	zName := a.str(name)
	var zVfs uintptr
	if vfs != "" {
		zVfs = a.str(vfs)
	}
	r := int32(sqlite3_open_v2(a.top, zName, pdb, uintptr(flags|OpenExResCode), zVfs))
	c.db = *(*uintptr)(at(pdb))
	c.st.db = c.db
	if r == OK {
		r = int32(go_classify(c.sp, c.db, c.classifyWord()))
	}
	if r == OK {
		r = int32(go_count_rollbacks(c.sp, c.db, c.st.base+rollbackOffset))
	}
	if r != OK {
		err := c.err(int(r))
		if c.db != 0 {
			sqlite3_close(c.sp, c.db)
		}
		st.free()
		return nil, err
	}
	return c, nil
}

// classifyWord is where goClassify writes the kind of the statement being
// prepared.
func (c *Conn) classifyWord() uintptr { return c.st.base + classifyOffset }

// The values of goClassifyWord.merging (engine/os.c).
const (
	merging = 1
	refused = 2
)

// SetMerging says whether the connection runs statements of other
// connections' callers (group commit). While it does, a statement that
// SQLite prepares again after a change of the schema and that is no longer
// mergeable fails before it does anything. Turned off, SetMerging reports
// whether that happened since it was turned on: the statement must run on
// its caller's connection, and Reclassify sorts it again.
func (c *Conn) SetMerging(on bool) (wasRefused bool) {
	w := (*int32)(at(c.classifyWord() + 4))
	wasRefused = *w == refused
	*w = 0
	if on {
		*w = merging
	}
	return wasRefused
}

// The bits goClassify writes (engine/os.c).
const (
	writesMain  = 1
	private     = 2
	changesConn = 4
)

// err is the error of result code r, with the connection's message.
func (c *Conn) err(r int) error {
	if msg := c.st.collatePanic; msg != "" {
		// go_collate_failed made SQLite stop with SQLITE_NOMEM and roll
		// back what the statement did.
		c.st.collatePanic = ""
		if r == OK || r == Row || r == Done {
			// SQLite went on after the panic: nothing it does on this
			// connection can be trusted any more.
			poison(c.st.base, "a Go collation panicked and SQLite went on: "+msg)
		}
		return &Error{Code: r, Msg: "panic in a Go collation: " + msg, err: ErrCollationPanic}
	}
	if r == OK || r == Row || r == Done {
		return nil
	}
	if c.db == 0 {
		return &Error{Code: r, Msg: errstr(c.sp, r)}
	}
	return &Error{Code: r, Msg: cstring(sqlite3_errmsg(c.sp, c.db))}
}

// Close closes the connection. Its statements must be finalized first: with
// one open, Close returns SQLITE_BUSY and the connection stays open and usable,
// since SQLite would otherwise keep a half-closed connection that runs on a
// shadow stack already given back. A broken connection runs no C: Close frees
// its stack and returns the error that says so.
func (c *Conn) Close() error {
	if c.db == 0 {
		return errors.New("sqlite: connection already closed")
	}
	if err := c.brokenErr(); err != nil {
		c.db = 0
		c.st.free()
		return err
	}
	if r := int32(sqlite3_close(c.sp, c.db)); r != OK {
		return c.err(int(r))
	}
	c.db = 0
	return c.st.free()
}

// Exec runs one or more statements that return no rows.
func (c *Conn) Exec(sql string) error {
	if err := c.brokenErr(); err != nil {
		return err
	}
	if len(sql) > big {
		p := stageAlloc(uintptr(len(sql) + 1))
		if p == 0 {
			return errNoMem()
		}
		defer release(p)
		copy(bytesAt(p, len(sql)), sql)
		*(*byte)(at(p + uintptr(len(sql)))) = 0
		*(*int32)(at(c.classifyWord())) = 0
		return c.execResult(int32(sqlite3_exec(c.sp, c.db, p, 0, 0, 0)))
	}
	a := c.args()
	z := a.str(sql)
	*(*int32)(at(c.classifyWord())) = 0
	return c.execResult(int32(sqlite3_exec(a.top, c.db, z, 0, 0, 0)))
}

func (c *Conn) execResult(r int32) error {
	if err := c.guardErr(r); err != nil {
		return err
	}
	return c.err(int(r))
}

// Prepare compiles the first statement of sql. tail is the rest of sql. A
// nil statement with no error is sql with no statement in it.
func (c *Conn) Prepare(sql string) (s *Stmt, tail string, err error) {
	if err := c.brokenErr(); err != nil {
		return nil, "", err
	}
	a := c.args()
	pStmt := a.word()
	pTail := a.word()
	var z uintptr
	if len(sql) > big {
		z = stageAlloc(uintptr(len(sql) + 1))
		if z == 0 {
			return nil, "", errNoMem()
		}
		defer release(z)
		copy(bytesAt(z, len(sql)), sql)
	} else {
		z = a.bytes([]byte(sql))
	}
	w := (*int32)(at(c.classifyWord()))
	*w = 0
	r := int32(sqlite3_prepare_v2(a.top, c.db, z, uintptr(len(sql)), pStmt, pTail))
	kind := *w
	if kind&changesConn != 0 {
		c.changed = true
	}
	if err := c.guardErr(r); err != nil {
		return nil, "", err
	}
	if r != OK {
		return nil, "", c.err(int(r))
	}
	if t := *(*uintptr)(at(pTail)); t != 0 {
		tail = sql[t-z:]
	}
	p := *(*uintptr)(at(pStmt))
	if p == 0 {
		return nil, tail, nil
	}
	writes := int32(sqlite3_stmt_readonly(a.top, p)) == 0
	return &Stmt{c: c, p: p, kind: kind, writes: writes}, tail, nil
}

// rollbacks is how many transactions SQLite has rolled back on the
// connection (go_count_rollbacks).
func (c *Conn) rollbacks() uint32 { return *(*uint32)(at(c.st.base + rollbackOffset)) }

// ErrRolledBack is the error of a statement that wrote, at its end, when
// SQLite rolled back its transaction while it ran: another statement of the
// connection failed in a way that ends the transaction (out of memory, an
// I/O error, a full disk, a panic in a Go collation), or ROLLBACK ran.
var ErrRolledBack = errors.New("sqlite: the transaction of the statement was rolled back while it ran: what it wrote is gone")

// Changes is the number of rows the last statement changed.
func (c *Conn) Changes() int64 { return int64(sqlite3_changes64(c.sp, c.db)) }

// LastInsertRowID is the rowid of the last row inserted.
func (c *Conn) LastInsertRowID() int64 { return int64(sqlite3_last_insert_rowid(c.sp, c.db)) }

// SetLastInsertRowID sets what LastInsertRowID and last_insert_rowid() give.
func (c *Conn) SetLastInsertRowID(id int64) { sqlite3_set_last_insert_rowid(c.sp, c.db, uintptr(id)) }

// VirtualTablesInTx says virtual tables take part in the open transaction:
// one was written since it began.
func (c *Conn) VirtualTablesInTx() bool { return int32(go_vtabs_in_tx(c.sp, c.db)) != 0 }

// DeferredFKs says foreign key constraints of the transaction are not
// resolved yet (SQLITE_DBSTATUS_DEFERRED_FKS).
func (c *Conn) DeferredFKs() bool {
	a := c.args()
	cur, hi := a.word(), a.word()
	sqlite3_db_status(c.sp, c.db, 10, cur, hi, 0) // SQLITE_DBSTATUS_DEFERRED_FKS
	return *(*int32)(at(cur)) != 0
}

// ReadOnly says the main database is open read-only.
func (c *Conn) ReadOnly() bool {
	a := c.args()
	return int32(sqlite3_db_readonly(c.sp, c.db, a.str("main"))) == 1
}

// AutoCommit says whether no transaction is open.
func (c *Conn) AutoCommit() bool { return int32(sqlite3_get_autocommit(c.sp, c.db)) != 0 }

// BusyTimeout sets how long a statement waits for a lock.
func (c *Conn) BusyTimeout(ms int) error {
	if err := c.brokenErr(); err != nil {
		return err
	}
	return c.err(int(int32(sqlite3_busy_timeout(c.sp, c.db, uintptr(ms)))))
}

// Stmt is a prepared statement.
type Stmt struct {
	c    *Conn
	p    uintptr
	kind int32 // what goClassify saw as it was prepared
	// sorted is the count of Reprepares when kind was read, or -1 when the
	// statement must be sorted again.
	sorted int
	// writes says the statement is not read-only. paused says it stopped
	// at a row, when the connection had rolled back rollbacks
	// transactions.
	writes    bool
	paused    bool
	rollbacks uint32
}

// lost says whether a statement that writes lost its transaction while it
// stopped at a row: another statement ended it. A rollback inside its own
// step is its own error, already in the result, or SQLite running it again
// after a change of the schema, after which it goes on and commits.
func (s *Stmt) lost() bool {
	lost := s.paused && s.c.rollbacks() != s.rollbacks
	s.paused = false
	return lost
}

// Mergeable says the statement changes tables of the main database and
// touches nothing only its connection has: run on another connection of
// the same file, it does the same. After SQLite prepared the statement again
// (a change of the schema can add a trigger or make a table a virtual one),
// it is sorted again first.
func (s *Stmt) Mergeable() bool {
	if p := s.Reprepares(); p != s.sorted {
		s.kind, s.sorted = s.classify(), p
	}
	return s.kind == writesMain
}

// Reclassify makes the next Mergeable sort the statement again: SQLite
// failed to prepare it again while the connection merged (SetMerging).
func (s *Stmt) Reclassify() { s.sorted = -1 }

// classify prepares the SQL of the statement once more and gives what
// goClassify sees now. SQLite prepares it again in sqlite3_step from the
// same SQL and the same schema. A statement that no longer prepares is not
// mergeable: run on its own connection, it gives its own error.
func (s *Stmt) classify() int32 {
	t, _, err := s.c.Prepare(cstring(sqlite3_sql(s.c.sp, s.p)))
	if err != nil || t == nil {
		return private
	}
	defer t.Finalize()
	return t.kind
}

// ChangesConn says the statement changes the connection itself: a PRAGMA
// that sets something, ATTACH, DETACH, or a temporary table, index, view or
// trigger.
func (s *Stmt) ChangesConn() bool { return s.kind&changesConn != 0 }

// ChangedItself says a statement that changes the connection was prepared on
// it, run or not.
func (c *Conn) ChangedItself() bool { return c.changed }

// Reprepares is how many times SQLite prepared the statement again, after a
// change of the schema.
func (s *Stmt) Reprepares() int { return int(int32(sqlite3_stmt_status(s.c.sp, s.p, 5, 0))) } // SQLITE_STMTSTATUS_REPREPARE

func (s *Stmt) err(r uintptr) error { return s.c.err(int(int32(r))) }

// Finalize frees the statement.
func (s *Stmt) Finalize() error {
	if err := s.c.brokenErr(); err != nil {
		s.p = 0
		return err
	}
	r := sqlite3_finalize(s.c.sp, s.p)
	s.p = 0
	return s.err(r)
}

// Reset makes the statement ready to run again. Bindings stay.
func (s *Stmt) Reset() error {
	if err := s.c.brokenErr(); err != nil {
		return err
	}
	lost := s.lost()
	// A statement stopped before its end finishes its write here.
	err := s.err(sqlite3_reset(s.c.sp, s.p))
	if lost && err == nil {
		err = ErrRolledBack
	}
	return err
}

// ClearBindings sets every parameter to NULL.
func (s *Stmt) ClearBindings() error {
	if err := s.c.brokenErr(); err != nil {
		return err
	}
	return s.err(sqlite3_clear_bindings(s.c.sp, s.p))
}

// Step runs the statement to its next row: true when there is one.
func (s *Stmt) Step() (bool, error) {
	if err := s.c.brokenErr(); err != nil {
		return false, err
	}
	if s.lost() {
		// The rows still to come are of a write that is gone.
		sqlite3_reset(s.c.sp, s.p)
		return false, ErrRolledBack
	}
	r := int32(sqlite3_step(s.c.sp, s.p))
	// A callback that panicked during the step leaves no trace in r: the
	// row it gave, or its end, may be wrong.
	if err := s.c.brokenErr(); err != nil {
		return false, err
	}
	switch r {
	case Row:
		if s.writes {
			s.paused, s.rollbacks = true, s.c.rollbacks()
		}
		return true, nil
	case Done:
		return false, nil
	default:
		return false, s.c.err(int(r))
	}
}

// ReadOnly says whether the statement writes nothing.
func (s *Stmt) ReadOnly() bool { return int32(sqlite3_stmt_readonly(s.c.sp, s.p)) != 0 }

// ParamCount is the number of the largest parameter.
func (s *Stmt) ParamCount() int { return int(int32(sqlite3_bind_parameter_count(s.c.sp, s.p))) }

// ParamName is the name of parameter i (from 1), with its prefix, or "".
func (s *Stmt) ParamName(i int) string {
	return cstring(sqlite3_bind_parameter_name(s.c.sp, s.p, uintptr(i)))
}

func (s *Stmt) BindNull(i int) error {
	if err := s.c.brokenErr(); err != nil {
		return err
	}
	return s.err(sqlite3_bind_null(s.c.sp, s.p, uintptr(i)))
}

func (s *Stmt) BindInt64(i int, v int64) error {
	if err := s.c.brokenErr(); err != nil {
		return err
	}
	return s.err(sqlite3_bind_int64(s.c.sp, s.p, uintptr(i), uintptr(v)))
}

func (s *Stmt) BindDouble(i int, v float64) error {
	if err := s.c.brokenErr(); err != nil {
		return err
	}
	return s.err(sqlite3_bind_double(s.c.sp, s.p, uintptr(i), v))
}

// transient tells SQLite to copy a string or blob before the call returns.
const transient = ^uintptr(0)

func (s *Stmt) BindText(i int, v string) error {
	if err := s.c.brokenErr(); err != nil {
		return err
	}
	if len(v) > math.MaxInt32 {
		return &Error{Code: 18, Msg: "string or blob too big"}
	}
	z, done, err := s.c.stage(unsafe.StringData(v), len(v))
	if err != nil {
		return err
	}
	defer done()
	return s.err(sqlite3_bind_text(s.c.sp+stageRoom(len(v)), s.p, uintptr(i), z, uintptr(len(v)), transient))
}

func (s *Stmt) BindBlob(i int, v []byte) error {
	if err := s.c.brokenErr(); err != nil {
		return err
	}
	if len(v) > math.MaxInt32 {
		return &Error{Code: 18, Msg: "string or blob too big"}
	}
	if v == nil {
		v = []byte{}
	}
	z, done, err := s.c.stage(unsafe.SliceData(v), len(v))
	if err != nil {
		return err
	}
	defer done()
	return s.err(sqlite3_bind_blob(s.c.sp+stageRoom(len(v)), s.p, uintptr(i), z, uintptr(len(v)), transient))
}

// stage copies n bytes from Go memory to where the C can read them: the
// bottom of the shadow stack when they fit, malloc'd memory when not.
func (c *Conn) stage(b *byte, n int) (uintptr, func(), error) { return stageAt(c.sp, b, n) }

// stageAt is stage from the shadow pointer sp, for a host callback that
// calls the C again.
func stageAt(sp uintptr, b *byte, n int) (uintptr, func(), error) {
	src := unsafe.Slice(b, n)
	if n <= big {
		copy(bytesAt(sp, n), src)
		return sp, func() {}, nil
	}
	p := stageAlloc(uintptr(n))
	if p == 0 {
		return 0, nil, errNoMem()
	}
	copy(bytesAt(p, n), src)
	return p, func() { release(p) }, nil
}

// stageAlloc is where the Go side gets memory for an argument too big for the
// shadow stack. It is a variable so that a test can make it fail.
var stageAlloc = alloc

func errNoMem() error { return &Error{Code: sqliteNoMem, Msg: "out of memory"} }

// stageRoom is the shadow stack stage takes for n bytes.
func stageRoom(n int) uintptr {
	if n > big {
		return 0
	}
	return (uintptr(n) + 15) &^ 15
}

// ColumnCount is the number of columns of a row.
func (s *Stmt) ColumnCount() int { return int(int32(sqlite3_column_count(s.c.sp, s.p))) }

func (s *Stmt) ColumnName(i int) string {
	return cstring(sqlite3_column_name(s.c.sp, s.p, uintptr(i)))
}

// ColumnDeclType is the declared type of column i, or "".
func (s *Stmt) ColumnDeclType(i int) string {
	return cstring(sqlite3_column_decltype(s.c.sp, s.p, uintptr(i)))
}

func (s *Stmt) ColumnType(i int) int { return int(int32(sqlite3_column_type(s.c.sp, s.p, uintptr(i)))) }

func (s *Stmt) ColumnInt64(i int) int64 { return int64(sqlite3_column_int64(s.c.sp, s.p, uintptr(i))) }

func (s *Stmt) ColumnDouble(i int) float64 { return sqlite3_column_double(s.c.sp, s.p, uintptr(i)) }

// ColumnText is the text of column i, copied.
func (s *Stmt) ColumnText(i int) string {
	p := sqlite3_column_text(s.c.sp, s.p, uintptr(i))
	n := int(int32(sqlite3_column_bytes(s.c.sp, s.p, uintptr(i))))
	if p == 0 || n == 0 {
		return ""
	}
	return string(bytesAt(p, n))
}

// ColumnBlob is the bytes of column i, copied: nil for NULL.
func (s *Stmt) ColumnBlob(i int) []byte {
	p := sqlite3_column_blob(s.c.sp, s.p, uintptr(i))
	n := int(int32(sqlite3_column_bytes(s.c.sp, s.p, uintptr(i))))
	if p == 0 {
		return nil
	}
	b := make([]byte, n)
	copy(b, bytesAt(p, n))
	return b
}

// sideStacks are shadow stacks for calls made beside a connection's own,
// from another goroutine. They are kept, not unmapped: there are as many as
// such calls ever ran at once.
var sideStacks struct {
	sync.Mutex
	free []*stack
}

func getSideStack() (*stack, error) {
	sideStacks.Lock()
	defer sideStacks.Unlock()
	if n := len(sideStacks.free); n > 0 {
		s := sideStacks.free[n-1]
		sideStacks.free = sideStacks.free[:n-1]
		return s, nil
	}
	return newStack()
}

func putSideStack(s *stack) {
	sideStacks.Lock()
	sideStacks.free = append(sideStacks.free, s)
	sideStacks.Unlock()
}

// Interrupt stops the statement running on the connection, from any
// goroutine. The statement returns SQLITE_INTERRUPT.
func (c *Conn) Interrupt() error {
	s, err := getSideStack()
	if err != nil {
		return err
	}
	sqlite3_interrupt(s.sp(), c.db)
	putSideStack(s)
	return nil
}

// Filename is the absolute name of the file of database schema ("main"), or
// "" for a memory or temporary database.
func (c *Conn) Filename(schema string) string {
	if len(schema) > maxName || c.brokenErr() != nil {
		return ""
	}
	a := c.args()
	z := a.str(schema)
	return cstring(sqlite3_db_filename(a.top, c.db, z))
}
