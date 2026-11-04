// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package csqlite

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"unsafe"
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
// panicked, and of the COMMIT that such a statement made fail.
var ErrCollationPanic = errors.New("panic in a Go collation")

// commitHookCode is SQLITE_CONSTRAINT_COMMITHOOK: the commit hook of
// go_guard_commits refused a commit.
const commitHookCode = 531

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
	if r == OK {
		r = int32(go_classify(c.sp, c.db, c.classifyWord()))
	}
	if r == OK {
		r = int32(go_guard_commits(c.sp, c.db, c.st.base+pendingOffset))
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

// The bits goClassify writes (engine/os.c).
const (
	writesMain  = 1
	private     = 2
	changesConn = 4
)

// err is the error of result code r, with the connection's message.
func (c *Conn) err(r int) error {
	if r == OK || r == Row || r == Done {
		return nil
	}
	if c.db == 0 {
		return &Error{Code: r, Msg: errstr(c.sp, r)}
	}
	if r == commitHookCode && c.st.doomed != "" {
		// The commit hook refused because of a collation's panic, and
		// SQLite rolled the transaction back.
		msg := c.st.doomed
		c.st.doomed = ""
		return &Error{Code: r, Msg: "the transaction is rolled back: panic in a Go collation: " + msg, err: ErrCollationPanic}
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
		return c.execResult(int(int32(sqlite3_exec(c.sp, c.db, p, 0, 0, 0))))
	}
	a := c.args()
	z := a.str(sql)
	return c.execResult(int(int32(sqlite3_exec(a.top, c.db, z, 0, 0, 0))))
}

func (c *Conn) execResult(r int) error {
	if c.st.collatePanic != "" {
		// endCollatePanic runs C, which a broken connection must not.
		if err := c.brokenErr(); err != nil {
			return err
		}
		return c.endCollatePanic(0, false)
	}
	return c.err(r)
}

// pending is the word of go_guard_commits: while it is set, the commit hook
// refuses every commit. A collation's panic sets it and the rollback hook
// clears it, so it is 0 whenever no write transaction is open.
func (c *Conn) pending() *int32 { return (*int32)(at(c.st.base + pendingOffset)) }

// endCollatePanic ends the statement s (0 for none, as in Exec) in which a
// Go collation panicked, and gives the panic as its error. wasPending is the
// pending word before s ran.
//
// The comparisons after the panic gave 0, so the rows s read may be wrong
// and what it wrote may be out of order. The commit hook has refused every
// commit since the panic: an autocommit write is already rolled back. What
// is left decides the word:
//   - no write transaction is open: nothing can commit, clear it.
//   - s only read and the word was clear before: the transaction holds
//     nothing of s, clear it.
//   - else s, or a statement of Exec, wrote in the open transaction: keep it,
//     so that the transaction's COMMIT fails and rolls it back.
func (c *Conn) endCollatePanic(s uintptr, wasPending bool) error {
	msg := c.st.collatePanic
	c.st.collatePanic = ""
	readOnly := false
	if s != 0 {
		readOnly = int32(sqlite3_stmt_readonly(c.sp, s)) != 0
		sqlite3_reset(c.sp, s)
	}
	switch {
	case int32(sqlite3_txn_state(c.sp, c.db, 0)) != txnWrite:
		*c.pending() = 0
		c.st.doomed = ""
	case readOnly && !wasPending:
		*c.pending() = 0
	case c.st.doomed == "":
		c.st.doomed = msg
	}
	return &Error{Code: 1, Msg: "panic in a Go collation: " + msg, err: ErrCollationPanic}
}

// txnWrite is SQLITE_TXN_WRITE.
const txnWrite = 2

// UndoCollationPanic tells the connection that what a statement wrote after
// a Go collation panicked is gone: the caller rolled back to a SAVEPOINT it
// took before the statement, and no earlier statement of the transaction
// panicked. The transaction may commit again.
func (c *Conn) UndoCollationPanic() {
	*c.pending() = 0
	c.st.doomed = ""
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
	return &Stmt{c: c, p: p, kind: kind}, tail, nil
}

// Changes is the number of rows the last statement changed.
func (c *Conn) Changes() int64 { return int64(sqlite3_changes64(c.sp, c.db)) }

// LastInsertRowID is the rowid of the last row inserted.
func (c *Conn) LastInsertRowID() int64 { return int64(sqlite3_last_insert_rowid(c.sp, c.db)) }

// SetLastInsertRowID sets what LastInsertRowID and last_insert_rowid() give.
func (c *Conn) SetLastInsertRowID(id int64) { sqlite3_set_last_insert_rowid(c.sp, c.db, uintptr(id)) }

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
}

// Mergeable says the statement changes tables of the main database and
// touches nothing only its connection has: run on another connection of
// the same file, it does the same.
func (s *Stmt) Mergeable() bool { return s.kind == writesMain }

// ChangesConn says the statement changes the connection itself: a PRAGMA
// that sets something, ATTACH, DETACH, or a temporary table, index, view or
// trigger.
func (s *Stmt) ChangesConn() bool { return s.kind&changesConn != 0 }

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
	return s.err(sqlite3_reset(s.c.sp, s.p))
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
	wasPending := *s.c.pending() != 0
	r := int32(sqlite3_step(s.c.sp, s.p))
	// A callback that panicked during the step leaves no trace in r: the
	// row it gave, or its end, may be wrong.
	if err := s.c.brokenErr(); err != nil {
		return false, err
	}
	if s.c.st.collatePanic != "" {
		return false, s.c.endCollatePanic(s.p, wasPending)
	}
	switch r {
	case Row:
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
