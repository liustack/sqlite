// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// upto is upto(stop): the rows 1 to stop, each with its value, its square
// as an int, and a time. stop is a hidden column that must be given.
type upto struct{ connects *atomic.Int64 }

func (upto) Schema() string { return "CREATE TABLE x(value, square, at, stop HIDDEN)" }

func (upto) BestIndex(info *IndexInfo) error {
	for i, c := range info.Constraints {
		if c.Column == 3 && c.Op == IndexEQ && c.Usable {
			info.Usage[i] = IndexUsage{ArgvIndex: 1, Omit: true}
			info.IdxStr = "stop"
			info.EstimatedCost = 10
			return nil
		}
	}
	return ErrUnusablePlan
}

func (upto) Open() (VCursor, error) { return &uptoCursor{}, nil }
func (upto) Disconnect() error      { return nil }

type uptoCursor struct{ i, stop int64 }

func (c *uptoCursor) Filter(_ int, idxStr string, args []driver.Value) error {
	if idxStr != "stop" || len(args) != 1 {
		return fmt.Errorf("Filter(%q, %v)", idxStr, args)
	}
	stop, ok := args[0].(int64)
	if !ok {
		return fmt.Errorf("stop is a %T", args[0])
	}
	c.i, c.stop = 1, stop
	return nil
}

func (c *uptoCursor) Next() error { c.i++; return nil }
func (c *uptoCursor) EOF() bool   { return c.i > c.stop }

func (c *uptoCursor) Column(i int) (driver.Value, error) {
	switch i {
	case 0:
		return c.i, nil
	case 1:
		return int(c.i * c.i), nil // an int, as a bound argument may be
	case 2:
		return time.Date(2026, 1, int(c.i), 0, 0, 0, 0, time.UTC), nil
	case 3:
		return c.stop, nil
	}
	return nil, fmt.Errorf("column %d", i)
}

func (c *uptoCursor) RowID() (int64, error) { return c.i, nil }
func (c *uptoCursor) Close() error          { return nil }

func withUpto(connects *atomic.Int64, flags ...ModuleFlag) Option {
	return WithModule("upto", func(args []string) (VTable, error) {
		if connects != nil {
			connects.Add(1)
		}
		return upto{connects}, nil
	}, append([]ModuleFlag{EponymousOnly}, flags...)...)
}

func TestWithModule(t *testing.T) {
	db := fOpen(t, ":memory:", withUpto(nil))
	if got := fString(t, db, `SELECT group_concat(value || ':' || square || ':' || at, ' ') FROM upto(3)`); got != "1:1:2026-01-01 00:00:00+00:00 2:4:2026-01-02 00:00:00+00:00 3:9:2026-01-03 00:00:00+00:00" {
		t.Errorf("upto(3) = %q", got)
	}
	if got := fString(t, db, `SELECT sum(value) FROM upto WHERE stop = ?`, 10); got != "55" {
		t.Errorf("sum of upto(10) = %q", got)
	}
	// The hidden column is not in SELECT *.
	rows, err := db.Query(`SELECT * FROM upto(1)`)
	if err != nil {
		t.Fatal(err)
	}
	cols, _ := rows.Columns()
	rows.Close()
	if strings.Join(cols, ",") != "value,square,at" {
		t.Errorf("SELECT * columns = %v", cols)
	}
	// Without stop, BestIndex has no plan.
	if _, err := db.Exec(`SELECT count(*) FROM upto`); err == nil {
		t.Error("upto without stop ran")
	}
	if _, err := db.Exec(`SELECT count(*) FROM upto('x')`); err == nil || !strings.Contains(err.Error(), "stop is a string") {
		t.Errorf("Filter error = %v", err)
	}
	if _, err := db.Exec(`INSERT INTO upto(value) VALUES (1)`); err == nil {
		t.Error("INSERT into a module worked")
	}
}

// words is CREATE VIRTUAL TABLE t USING words(a, b, ...): one row for each
// argument.
type words []string

func (words) Schema() string                  { return "CREATE TABLE x(word TEXT)" }
func (words) BestIndex(info *IndexInfo) error { return nil }
func (w words) Open() (VCursor, error)        { return &wordsCursor{w: w}, nil }
func (words) Disconnect() error               { return nil }

type wordsCursor struct {
	w words
	i int
}

func (c *wordsCursor) Filter(int, string, []driver.Value) error { c.i = 0; return nil }
func (c *wordsCursor) Next() error                              { c.i++; return nil }
func (c *wordsCursor) EOF() bool                                { return c.i >= len(c.w) }
func (c *wordsCursor) Column(int) (driver.Value, error)         { return c.w[c.i], nil }
func (c *wordsCursor) RowID() (int64, error)                    { return int64(c.i + 1), nil }
func (c *wordsCursor) Close() error                             { return nil }

func withWords(connects *atomic.Int64) Option {
	return WithModule("words", func(args []string) (VTable, error) {
		connects.Add(1)
		return words(args), nil
	})
}

// A table made by CREATE VIRTUAL TABLE is in the file, and each connection
// connects it when it first uses it.
func TestModuleCreateVirtualTable(t *testing.T) {
	p := mPath(t)
	var connects atomic.Int64
	db := fOpen(t, "file:"+p, withWords(&connects))
	db.SetMaxOpenConns(2)
	mExec(t, db, `CREATE VIRTUAL TABLE w USING words(alpha, beta, gamma)`)
	c1, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	c2, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	before := connects.Load()
	var a, b string
	if err := c1.QueryRowContext(t.Context(), `SELECT group_concat(word) FROM w`).Scan(&a); err != nil {
		t.Fatal(err)
	}
	if err := c2.QueryRowContext(t.Context(), `SELECT group_concat(word) FROM w`).Scan(&b); err != nil {
		t.Fatal(err)
	}
	c1.Close()
	c2.Close()
	if a != "alpha,beta,gamma" || b != a {
		t.Errorf("w = %q and %q", a, b)
	}
	if n := connects.Load() - before; n < 1 {
		t.Errorf("%d connects for two connections", n)
	}
	// A connector without the module cannot use the table.
	plain := mOpen(t, "file:"+p)
	if _, err := plain.Exec(`SELECT * FROM w`); err == nil || !strings.Contains(err.Error(), "no such module") {
		t.Errorf("w without the module: %v", err)
	}
	mExec(t, db, `DROP TABLE w`)
}

// A DirectOnly module may not be used from a view.
func TestModuleDirectOnly(t *testing.T) {
	db := fOpen(t, ":memory:", withUpto(nil, ModuleDirectOnly))
	db.SetMaxOpenConns(1)
	if got := fString(t, db, `SELECT count(*) FROM upto(4)`); got != "4" {
		t.Errorf("count(upto(4)) = %q", got)
	}
	mExec(t, db, `CREATE VIEW v AS SELECT value FROM upto(4)`)
	if _, err := db.Exec(`SELECT * FROM v`); err == nil || !strings.Contains(err.Error(), "unsafe use") {
		t.Errorf("a DirectOnly module from a view: %v", err)
	}
	// Innocuous lets a view use it with trusted_schema off, DirectOnly not.
	in := fOpen(t, ":memory:", withUpto(nil, ModuleInnocuous))
	in.SetMaxOpenConns(1)
	mExec(t, in, `PRAGMA trusted_schema = OFF`)
	mExec(t, in, `CREATE VIEW v AS SELECT value FROM upto(4)`)
	if got := fString(t, in, `SELECT count(*) FROM v`); got != "4" {
		t.Errorf("an Innocuous module from a view = %q", got)
	}
}

func TestWithModuleRefused(t *testing.T) {
	connect := func([]string) (VTable, error) { return words(nil), nil }
	for _, opt := range []Option{
		WithModule("", connect),
		WithModule("a\x00b", connect),
		WithModule(strings.Repeat("m", maxFuncName+1), connect),
		WithModule("m", nil),
		WithModule("m", connect, ModuleFlag(16)),
		WithModule("m", connect, ModuleDirectOnly, ModuleInnocuous),
	} {
		if _, err := NewConnector(":memory:", opt); err == nil {
			t.Errorf("a bad module was taken")
		}
	}
}

// A connect that fails or gives nil fails the statement, and a table that
// fails to open fails the scan.
func TestModuleErrors(t *testing.T) {
	db := fOpen(t, ":memory:",
		WithModule("broken", func([]string) (VTable, error) { return nil, errors.New("cannot connect") }),
		WithModule("nothing", func([]string) (VTable, error) { return nil, nil }),
		WithModule("closed", func([]string) (VTable, error) { return closed{}, nil }),
	)
	db.SetMaxOpenConns(1)
	mExec(t, db, `CREATE VIRTUAL TABLE c USING closed`)
	for q, want := range map[string]string{
		`CREATE VIRTUAL TABLE b USING broken`:  "cannot connect",
		`CREATE VIRTUAL TABLE n USING nothing`: "",
		`SELECT * FROM c`:                      "cannot open",
		`SELECT * FROM closed`:                 "no such table",
	} {
		_, err := db.Exec(q)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", q, err)
		}
	}
}

type closed struct{ words }

func (closed) Open() (VCursor, error) { return nil, errors.New("cannot open") }

// An INSERT that reads a Go table never merges: each connection has its own
// instance of the table, which may give other rows. The inserts still all
// run, each on its own connection.
func TestMergeWithModule(t *testing.T) {
	p := mPath(t)
	db := fOpen(t, "file:"+p, withUpto(nil))
	db.SetMaxOpenConns(8)
	mExec(t, db, `CREATE TABLE h(x); CREATE TABLE t(n INTEGER)`)
	other := mOpen(t, "file:"+p)
	sc, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var wl *writeLock
	sc.Raw(func(dc any) error { wl = dc.(*conn).wl; return nil })
	sc.Close()
	commits0, merged0 := wl.stats.commits.Load(), wl.stats.merged.Load()

	const n = 6
	errs := make(chan error, n)
	tx, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	mExec(t, tx, `INSERT INTO h VALUES (1)`)
	for i := range n {
		go func() {
			r, err := db.Exec(`INSERT INTO t SELECT value FROM upto(?)`, i+1)
			if err == nil {
				var k int64
				if k, err = r.RowsAffected(); err == nil && k != int64(i+1) {
					err = fmt.Errorf("upto(%d) inserted %d rows", i+1, k)
				}
			}
			errs <- err
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for waiting() < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d inserts wait", waiting(), n)
		}
		time.Sleep(time.Millisecond)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for range n {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
	if c, m := wl.stats.commits.Load()-commits0, wl.stats.merged.Load()-merged0; c != 0 || m != 0 {
		t.Errorf("%d batch commits of %d statements, want none", c, m)
	}
	if got := mCount(t, db, `SELECT count(*) FROM t`); got != n*(n+1)/2 {
		t.Errorf("%d rows, want %d", got, n*(n+1)/2)
	}
}

// store is the data of the memo tables of all connections.
type store struct {
	sync.Mutex
	rows map[int64]string
	next int64
}

// memo is CREATE VIRTUAL TABLE m USING memo: a writable table of one
// column, without transactions of its own.
type memo struct{ s *store }

func (memo) Schema() string                  { return "CREATE TABLE x(v TEXT)" }
func (memo) BestIndex(info *IndexInfo) error { return nil }
func (memo) Disconnect() error               { return nil }

func (m memo) Open() (VCursor, error) {
	m.s.Lock()
	defer m.s.Unlock()
	var w words
	for _, k := range slices.Sorted(maps.Keys(m.s.rows)) {
		w = append(w, m.s.rows[k])
	}
	return &wordsCursor{w: w}, nil
}

func (m memo) Insert(rowid driver.Value, cols []driver.Value) (int64, error) {
	m.s.Lock()
	defer m.s.Unlock()
	m.s.next++
	m.s.rows[m.s.next] = cols[0].(string)
	return m.s.next, nil
}

func (m memo) Update(oldRowid, newRowid driver.Value, cols []driver.Value) error {
	return errors.New("memo: no updates")
}

func (m memo) Delete(rowid driver.Value) error { return errors.New("memo: no deletes") }

// Writes to a table of a writable module never merge, and writes to other
// tables at the same time still do.
func TestMergeWithWritableModule(t *testing.T) {
	p := mPath(t)
	s := &store{rows: map[int64]string{}}
	db := fOpen(t, "file:"+p, WithModule("memo", func([]string) (VTable, error) { return memo{s}, nil }, ModuleWritable))
	db.SetMaxOpenConns(16)
	mExec(t, db, `CREATE TABLE h(x); CREATE TABLE t(v TEXT); CREATE VIRTUAL TABLE m USING memo`)
	other := mOpen(t, "file:"+p)
	sc, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var wl *writeLock
	sc.Raw(func(dc any) error { wl = dc.(*conn).wl; return nil })
	sc.Close()
	merged0 := wl.stats.merged.Load()

	const n = 5
	type result struct {
		memo bool
		id   int64
		err  error
	}
	results := make(chan result, 2*n)
	tx, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	mExec(t, tx, `INSERT INTO h VALUES (1)`)
	for i := range n {
		go func() {
			r, err := db.Exec(`INSERT INTO m(v) VALUES (?)`, fmt.Sprint("m", i))
			var id int64
			if err == nil {
				id, err = r.LastInsertId()
			}
			results <- result{true, id, err}
		}()
		go func() {
			_, err := db.Exec(`INSERT INTO t(v) VALUES (?)`, fmt.Sprint("t", i))
			results <- result{false, 0, err}
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for waiting() < 2*n {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d inserts wait", waiting(), 2*n)
		}
		time.Sleep(time.Millisecond)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	ids := map[int64]bool{}
	for range 2 * n {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.memo {
			ids[r.id] = true
		}
	}
	if m := wl.stats.merged.Load() - merged0; m != n {
		t.Errorf("%d statements merged, want the %d inserts into t", m, n)
	}
	if len(ids) != n {
		t.Errorf("LastInsertId of the inserts into m: %v", ids)
	}
	if got := fString(t, db, `SELECT count(*) FROM m`); got != fmt.Sprint(n) {
		t.Errorf("%s rows in m", got)
	}
	if got := mCount(t, db, `SELECT count(*) FROM t`); got != n {
		t.Errorf("%d rows in t", got)
	}
	// Reading a writable table does not merge either: INSERT ... SELECT
	// from it.
	merged0 = wl.stats.merged.Load()
	tx, err = other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	mExec(t, tx, `INSERT INTO h VALUES (2)`)
	errs := make(chan error, n)
	for range n {
		go func() {
			_, err := db.Exec(`INSERT INTO t SELECT v FROM m LIMIT 1`)
			errs <- err
		}()
	}
	for waiting() < n {
		if time.Now().After(deadline.Add(5 * time.Second)) {
			t.Fatalf("%d of %d wait", waiting(), n)
		}
		time.Sleep(time.Millisecond)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for range n {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if m := wl.stats.merged.Load() - merged0; m != 0 {
		t.Errorf("%d of %d INSERT ... SELECT from m merged", m, n)
	}
}

func TestWritableModuleRefused(t *testing.T) {
	connect := func([]string) (VTable, error) { return words(nil), nil }
	if _, err := NewConnector(":memory:", WithModule("w", connect, ModuleWritable, EponymousOnly)); err == nil {
		t.Error("a writable eponymous module was taken")
	}
	db := fOpen(t, ":memory:", WithModule("w", connect, ModuleWritable))
	db.SetMaxOpenConns(1)
	mExec(t, db, `CREATE VIRTUAL TABLE w1 USING w`)
	if _, err := db.Exec(`INSERT INTO w1 VALUES ('a')`); err == nil || !strings.Contains(err.Error(), "takes no writes") {
		t.Errorf("a write to a table that is no VUpdater: %v", err)
	}
}

// txMemo is a memo that takes part in transactions and savepoints, and
// writes down the calls it gets.
type txMemo struct {
	memo
	log *[]string
	old map[int64]string
	sp  map[int]map[int64]string
}

func (m *txMemo) note(s string) error { *m.log = append(*m.log, s); return nil }

func (m *txMemo) Begin() error {
	m.s.Lock()
	m.old, m.sp = maps.Clone(m.s.rows), map[int]map[int64]string{}
	m.s.Unlock()
	return m.note("begin")
}
func (m *txMemo) Sync() error   { return m.note("sync") }
func (m *txMemo) Commit() error { return m.note("commit") }
func (m *txMemo) Rollback() error {
	m.s.Lock()
	m.s.rows = m.old
	m.s.Unlock()
	return m.note("rollback")
}
func (m *txMemo) Savepoint(n int) error {
	m.s.Lock()
	m.sp[n] = maps.Clone(m.s.rows)
	m.s.Unlock()
	return m.note(fmt.Sprint("savepoint ", n))
}
func (m *txMemo) Release(n int) error { return m.note(fmt.Sprint("release ", n)) }
func (m *txMemo) RollbackTo(n int) error {
	m.s.Lock()
	m.s.rows = maps.Clone(m.old) // n is older than the table's savepoints
	for k := n; k >= 0; k-- {
		if r, ok := m.sp[k]; ok {
			m.s.rows = maps.Clone(r)
			break
		}
	}
	m.s.Unlock()
	return m.note(fmt.Sprint("rollback to ", n))
}

func (m *txMemo) Insert(rowid driver.Value, cols []driver.Value) (int64, error) {
	if cols[0] == "bad" {
		return 0, errors.New("memo: bad")
	}
	return m.memo.Insert(rowid, cols)
}

// The calls of database/sql transactions reach a VTransactor and a
// VSavepointer, and a failed statement in a transaction is undone.
func TestWritableModuleTransactions(t *testing.T) {
	s := &store{rows: map[int64]string{}}
	var log []string
	db := fOpen(t, ":memory:", WithModule("memo", func([]string) (VTable, error) {
		return &txMemo{memo: memo{s}, log: &log}, nil
	}, ModuleWritable))
	db.SetMaxOpenConns(1)
	mExec(t, db, `CREATE VIRTUAL TABLE m USING memo`)
	log = nil

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	mExec(t, tx, `INSERT INTO m VALUES ('a')`)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := fString(t, db, `SELECT count(*) FROM m`); got != "0" {
		t.Errorf("%s rows after the rollback", got)
	}
	if want := "begin rollback"; strings.Join(log, " ") != want {
		t.Errorf("calls %q, want %q", log, want)
	}

	log = nil
	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	mExec(t, tx, `INSERT INTO m VALUES ('b')`)
	if _, err := tx.Exec(`SAVEPOINT x; INSERT INTO m VALUES ('c'); ROLLBACK TO x; RELEASE x`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := fString(t, db, `SELECT group_concat(v) FROM m`); got != "b" {
		t.Errorf("m = %q, want b", got)
	}
	if !slices.Contains(log, "rollback to 0") || log[0] != "begin" || !slices.Equal(log[len(log)-2:], []string{"sync", "commit"}) {
		t.Errorf("calls %q", log)
	}

	// Autocommit: a row the table refuses fails the statement alone.
	r, err := db.Exec(`INSERT INTO m VALUES ('d')`)
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := r.LastInsertId(); id != 4 { // the ids of a, b and c are not taken back
		t.Errorf("LastInsertId = %d, want 4", id)
	}
	if _, err := db.Exec(`INSERT INTO m VALUES ('bad')`); err == nil || !strings.Contains(err.Error(), "memo: bad") {
		t.Errorf("a refused row: %v", err)
	}
	if got := fString(t, db, `SELECT group_concat(v) FROM m`); got != "b,d" {
		t.Errorf("m = %q, want b,d", got)
	}
}

// A prepared insert into a table that became a table of a writable module
// no longer merges.
func TestMergeAfterTableBecomesWritable(t *testing.T) {
	p := mPath(t)
	s := &store{rows: map[int64]string{}}
	db := fOpen(t, "file:"+p, WithModule("memo", func([]string) (VTable, error) { return memo{s}, nil }, ModuleWritable))
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	mExec(t, db, `CREATE TABLE h(x); CREATE TABLE m(v TEXT)`)
	other := mOpen(t, "file:"+p)
	wl := statsOf(t, db)
	ps, err := db.Prepare(`INSERT INTO m(v) VALUES (?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer ps.Close()
	const n = 8
	insert := func(i int) error { _, err := ps.Exec(fmt.Sprint(i)); return err }
	queue(t, other, n, insert)
	queue(t, other, n, insert)
	mExec(t, db, `DROP TABLE m; CREATE VIRTUAL TABLE m USING memo`)
	m0 := wl.stats.merged.Load()
	queue(t, other, n, insert)
	if m := wl.stats.merged.Load() - m0; m != 0 {
		t.Errorf("%d inserts into the Go table merged", m)
	}
	if len(s.rows) != n {
		t.Errorf("%d rows in the Go table, want %d", len(s.rows), n)
	}
}

// TestModuleReadUsesOwnInstance: a write that reads a Go table reads the
// instance of its own connection, not the one of the connection that holds
// the lock when it comes. Here each connection's instance gives a word of
// its own.
func TestModuleReadUsesOwnInstance(t *testing.T) {
	var next atomic.Int64
	db := fOpen(t, "file:"+mPath(t)+"?_pragma=journal_mode(WAL)", WithModule("localwords", func([]string) (VTable, error) {
		return words{strconv.FormatInt(next.Add(1), 10)}, nil
	}, EponymousOnly))
	mExec(t, db, `CREATE TABLE h(x); CREATE TABLE t(x)`)
	leader, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer leader.Close()
	caller, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer caller.Close()
	var theirs, mine string
	if err := leader.QueryRowContext(t.Context(), `SELECT word FROM localwords`).Scan(&theirs); err != nil {
		t.Fatal(err)
	}
	if err := caller.QueryRowContext(t.Context(), `SELECT word FROM localwords`).Scan(&mine); err != nil {
		t.Fatal(err)
	}
	if err := behind(t, leader, func() error {
		_, err := caller.ExecContext(t.Context(), `INSERT INTO t SELECT word FROM localwords`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := caller.QueryRowContext(t.Context(), `SELECT x FROM t`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != mine {
		t.Fatalf("inserted %q, the caller's table gives %q and the leader's %q", got, mine, theirs)
	}
}
