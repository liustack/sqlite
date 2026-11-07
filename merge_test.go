// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The tests of this file hold a file's write lock with a transaction, let
// another caller queue behind it, and check that what the caller gets is
// what it would get running alone.

func mOpen(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func mExec(t *testing.T, db execer, q string, args ...any) sql.Result {
	t.Helper()
	r, err := db.ExecContext(context.Background(), q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return r
}

func mCount(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func mPath(t *testing.T) string { return filepath.Join(t.TempDir(), "m.db") }

// waiting is how many callers wait for a write lock of this process, in its
// queue or for the lock itself.
func waiting() int {
	writeLocks.Lock()
	defer writeLocks.Unlock()
	n := 0
	for _, l := range writeLocks.all {
		l.mu.Lock()
		n += len(l.queue) + len(l.waiters)
		l.mu.Unlock()
	}
	return n
}

// behind runs f while holder holds the write lock in a transaction (which
// wrote a row of h), and commits once f waits for the lock. It returns f's
// error.
func behind(t *testing.T, holder execBeginner, f func() error) error {
	t.Helper()
	ctx := context.Background()
	tx, err := holder.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO h VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	ch := make(chan error, 1)
	go func() { ch <- f() }()
	deadline := time.Now().Add(5 * time.Second)
	for waiting() == 0 {
		select {
		case err := <-ch:
			tx.Commit()
			return err // it did not wait
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the caller never waited for the lock")
		}
		time.Sleep(time.Millisecond)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return <-ch
}

type execBeginner interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

const schemaH = `CREATE TABLE IF NOT EXISTS h(x)`

func TestMergeReadOnlyURI(t *testing.T) {
	p := mPath(t)
	rw := mOpen(t, "file:"+p+"?_pragma=journal_mode(WAL)")
	mExec(t, rw, schemaH)
	mExec(t, rw, `CREATE TABLE t(x)`)
	ro := mOpen(t, "file:"+p+"?mode=ro")
	err := behind(t, rw, func() error {
		_, err := ro.Exec(`INSERT INTO t VALUES (1)`)
		return err
	})
	if err == nil || mCount(t, rw, `SELECT count(*) FROM t`) != 0 {
		t.Fatalf("a read-only handle wrote: err %v", err)
	}
}

func TestMergeQueryOnly(t *testing.T) {
	p := mPath(t)
	rw := mOpen(t, "file:"+p+"?_pragma=journal_mode(WAL)")
	mExec(t, rw, schemaH)
	mExec(t, rw, `CREATE TABLE t(x)`)
	qo := mOpen(t, "file:"+p+"?_pragma=query_only(1)")
	qo.SetMaxOpenConns(1)
	err := behind(t, rw, func() error {
		_, err := qo.Exec(`INSERT INTO t VALUES (1)`)
		return err
	})
	if err == nil {
		t.Fatal("a query_only handle wrote through the queue")
	}
	// A read-only transaction must leave query_only as the DSN set it.
	tx, err := qo.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := qo.Exec(`INSERT INTO t VALUES (2)`); err == nil {
		t.Fatal("a read-only transaction turned query_only off")
	}
	if n := mCount(t, rw, `SELECT count(*) FROM t`); n != 0 {
		t.Fatalf("%d rows written through query_only", n)
	}
}

func TestMergeForeignKeysDiffer(t *testing.T) {
	p := mPath(t)
	nofk := mOpen(t, "file:"+p+"?_pragma=journal_mode(WAL)")
	fk := mOpen(t, "file:"+p+"?_pragma=foreign_keys(1)")
	mExec(t, nofk, schemaH)
	mExec(t, nofk, `CREATE TABLE p(id INTEGER PRIMARY KEY)`)
	mExec(t, nofk, `CREATE TABLE c(pid REFERENCES p(id) ON DELETE CASCADE)`)
	mExec(t, nofk, `INSERT INTO p VALUES (1)`)
	mExec(t, nofk, `INSERT INTO c VALUES (1)`)
	if err := behind(t, nofk, func() error {
		_, err := fk.Exec(`DELETE FROM p WHERE id = 1`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := mCount(t, fk, `SELECT count(*) FROM c`); n != 0 {
		t.Fatalf("the cascade of a foreign_keys pool did not run: %d children left", n)
	}
	err := behind(t, nofk, func() error {
		_, err := fk.Exec(`INSERT INTO c VALUES (99)`)
		return err
	})
	if err == nil {
		t.Fatal("an orphan row was accepted for a foreign_keys pool")
	}
}

func TestMergeSynchronousDiffers(t *testing.T) {
	p := mPath(t)
	off := mOpen(t, "file:"+p+"?_pragma=journal_mode(WAL)&_pragma=synchronous(OFF)")
	full := mOpen(t, "file:"+p+"?_pragma=synchronous(FULL)")
	mExec(t, off, schemaH)
	mExec(t, off, `CREATE TABLE t(x)`)
	ctx := context.Background()
	holder, err := off.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := behind(t, holder, func() error {
		_, err := full.Exec(`INSERT INTO t VALUES (1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := holder.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("a synchronous=OFF connection committed the write of a synchronous=FULL pool (total_changes %d)", n)
	}
}

func TestMergeTempShadow(t *testing.T) {
	p := mPath(t)
	db := mOpen(t, "file:"+p+"?_pragma=journal_mode(WAL)")
	mExec(t, db, schemaH)
	mExec(t, db, `CREATE TABLE t(x)`)
	ctx := context.Background()
	a, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	mExec(t, a, `CREATE TEMP TABLE t(x)`)
	if err := behind(t, a, func() error {
		_, err := db.Exec(`INSERT INTO t VALUES ('b')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var temp int
	if err := a.QueryRowContext(ctx, `SELECT count(*) FROM temp.t`).Scan(&temp); err != nil {
		t.Fatal(err)
	}
	if main := mCount(t, db, `SELECT count(*) FROM main.t`); main != 1 || temp != 0 {
		t.Fatalf("main.t has %d rows (want 1), the holder's temp.t %d (want 0)", main, temp)
	}
}

func TestMergeTempTriggerOfCaller(t *testing.T) {
	p := mPath(t)
	db := mOpen(t, "file:"+p+"?_pragma=journal_mode(WAL)")
	other := mOpen(t, "file:"+p)
	mExec(t, db, schemaH)
	mExec(t, db, `CREATE TABLE t(x)`)
	mExec(t, db, `CREATE TABLE audit(x)`)
	ctx := context.Background()
	a, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	mExec(t, a, `CREATE TEMP TRIGGER log AFTER INSERT ON main.t BEGIN INSERT INTO audit VALUES (new.x); END`)
	if err := behind(t, other, func() error {
		_, err := a.ExecContext(ctx, `INSERT INTO t VALUES (7)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := mCount(t, db, `SELECT count(*) FROM audit`); n != 1 {
		t.Fatalf("the caller's temporary trigger ran %d times, want 1", n)
	}
}

func TestFailedCommitEndsTransaction(t *testing.T) {
	p := mPath(t)
	db := mOpen(t, "file:"+p+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(2000)")
	db.SetMaxOpenConns(1)
	mExec(t, db, `CREATE TABLE p(id INTEGER PRIMARY KEY)`)
	mExec(t, db, `CREATE TABLE c(pid REFERENCES p(id) DEFERRABLE INITIALLY DEFERRED)`)
	mExec(t, db, `CREATE TABLE w(x)`)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	mExec(t, tx, `INSERT INTO c VALUES (42)`)
	if err := tx.Commit(); err == nil {
		t.Fatal("the commit of an orphan row succeeded")
	}
	mExec(t, db, `INSERT INTO w VALUES (1)`)
	other := mOpen(t, "file:"+p)
	if n := mCount(t, other, `SELECT count(*) FROM w`); n != 1 {
		t.Fatalf("a write after a failed commit is not committed (%d rows)", n)
	}
	if n := mCount(t, other, `SELECT count(*) FROM c`); n != 0 {
		t.Fatalf("the failed transaction left %d rows", n)
	}
}

func TestMergeDeferredForeignKey(t *testing.T) {
	p := mPath(t)
	db := mOpen(t, "file:"+p+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	db.SetMaxOpenConns(4)
	mExec(t, db, schemaH)
	mExec(t, db, `CREATE TABLE p(id INTEGER PRIMARY KEY)`)
	mExec(t, db, `CREATE TABLE c(pid REFERENCES p(id) DEFERRABLE INITIALLY DEFERRED)`)
	mExec(t, db, `CREATE TABLE ok(x)`)
	// The holder keeps the lock until both inserts wait in the queue, so
	// they run as one batch.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	mExec(t, tx, `INSERT INTO h VALUES (1)`)
	var bad, good error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, bad = db.Exec(`INSERT INTO c VALUES (7)`) }()
	for waiting() < 1 {
		time.Sleep(time.Millisecond)
	}
	go func() { defer wg.Done(); _, good = db.Exec(`INSERT INTO ok VALUES (1)`) }()
	for waiting() < 2 {
		time.Sleep(time.Millisecond)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if bad == nil || !strings.Contains(bad.Error(), "FOREIGN KEY") {
		t.Fatalf("the orphan insert got %v, want a foreign key error", bad)
	}
	if good != nil {
		t.Fatalf("an unrelated insert failed with %v", good)
	}
	if n := mCount(t, db, `SELECT count(*) FROM ok`); n != 1 {
		t.Fatalf("ok has %d rows, want 1", n)
	}
	if n := mCount(t, db, `SELECT count(*) FROM c`); n != 0 {
		t.Fatalf("c has %d orphan rows", n)
	}
}

func TestExecArgsOfSeveralStatements(t *testing.T) {
	db := mOpen(t, "file:"+mPath(t))
	mExec(t, db, `CREATE TABLE a(x)`)
	mExec(t, db, `INSERT INTO a VALUES (?); INSERT INTO a VALUES (?)`, 1, 2)
	if n := mCount(t, db, `SELECT sum(x) FROM a`); n != 3 {
		t.Fatalf("sum %d, want 3", n)
	}
}

func TestMissingArgument(t *testing.T) {
	db := mOpen(t, "file:"+mPath(t))
	mExec(t, db, `CREATE TABLE a(x, y)`)
	if _, err := db.Exec(`INSERT INTO a VALUES (?, ?)`, 1); err == nil {
		t.Fatal("a statement ran with a parameter left unbound")
	}
	if _, err := db.Exec(`INSERT INTO a VALUES (:x, :y)`, sql.Named("x", 1)); err == nil {
		t.Fatal("a statement ran with a named parameter left unbound")
	}
	if _, err := db.Exec(`INSERT INTO a VALUES (:x, :x)`, sql.Named("x", 1)); err != nil {
		t.Fatalf("one name twice: %v", err)
	}
	// A number the SQL skips needs an argument, as it does with no names.
	var a, b int
	if err := db.QueryRow(`SELECT ?2, :x`, sql.Named("x", 9), 5).Scan(&a, &b); err == nil {
		t.Fatal("a statement ran with a number it skips left without an argument")
	}
	if err := db.QueryRow(`SELECT ?2, :x`, 0, 5, sql.Named("x", 9)).Scan(&a, &b); err != nil || a != 5 || b != 9 {
		t.Fatalf("(%d, %d), %v, want (5, 9)", a, b, err)
	}
	if _, err := db.Exec(`INSERT INTO a VALUES (?, :y)`, sql.Named("y", 1)); err == nil {
		t.Fatal("a statement ran with a ? left unbound")
	}
	if _, err := db.Exec(`INSERT INTO a VALUES (:x, 1) RETURNING ?`, sql.Named("x", 1)); err == nil {
		t.Fatal("a statement ran with a ? of RETURNING left unbound")
	}
	if _, err := db.Exec(`CREATE TABLE w(s TEXT); CREATE INDEX w_s ON w(s)`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM w WHERE s LIKE ? AND :x`, sql.Named("x", 1)).Scan(&a); err == nil {
		t.Fatal("a statement ran with a ? of LIKE left unbound")
	}
	// A prepared statement SQLite made again for the value bound last.
	mExec(t, db, `INSERT INTO w VALUES ('abc'); CREATE TABLE log(x)`)
	s, err := db.Prepare(`INSERT INTO log SELECT :x WHERE NOT EXISTS (SELECT 1 FROM w INDEXED BY w_s WHERE s GLOB ?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Exec(sql.Named("x", 9), "a*"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(sql.Named("x", 9)); err == nil {
		t.Fatal("a prepared statement ran with a ? of GLOB left unbound")
	}
}

func TestMergeLastInsertRowid(t *testing.T) {
	p := mPath(t)
	db := mOpen(t, "file:"+p+"?_pragma=journal_mode(WAL)")
	other := mOpen(t, "file:"+p+"?_pragma=journal_mode(WAL)")
	mExec(t, db, schemaH)
	mExec(t, db, `CREATE TABLE p(id INTEGER PRIMARY KEY, v)`)
	mExec(t, db, `CREATE TABLE c(pid)`)
	mExec(t, db, `INSERT INTO p(v) VALUES ('old')`)
	if err := behind(t, other, func() error {
		_, err := db.Exec(`INSERT INTO p(v) VALUES ('new'); INSERT INTO c VALUES (last_insert_rowid())`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if pid, id := mCount(t, db, `SELECT pid FROM c`), mCount(t, db, `SELECT id FROM p WHERE v = 'new'`); pid != id {
		t.Fatalf("last_insert_rowid() after a merged insert is %d, want %d", pid, id)
	}
}

func TestMergeLastInsertIdOfUpdate(t *testing.T) {
	p := mPath(t)
	db := mOpen(t, "file:"+p+"?_pragma=journal_mode(WAL)")
	other := mOpen(t, "file:"+p+"?_pragma=journal_mode(WAL)")
	mExec(t, db, schemaH)
	mExec(t, db, `CREATE TABLE t(id INTEGER PRIMARY KEY, v)`)
	ctx := context.Background()
	u, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	own, _ := mExec(t, u, `INSERT INTO t(v) VALUES (0)`).LastInsertId()
	mExec(t, other, `INSERT INTO t(v) VALUES (1)`)
	var res sql.Result
	if err := behind(t, other, func() error {
		var err error
		res, err = u.ExecContext(ctx, `UPDATE t SET v = 2 WHERE id = 1`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if id, _ := res.LastInsertId(); id != own {
		t.Fatalf("LastInsertId of a merged UPDATE is %d, want the connection's own %d", id, own)
	}
	var ignored sql.Result
	if err := behind(t, other, func() error {
		var err error
		ignored, err = u.ExecContext(ctx, `INSERT OR IGNORE INTO t(id, v) VALUES (1, 9)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if id, _ := ignored.LastInsertId(); id != own {
		t.Fatalf("LastInsertId of an ignored merged INSERT is %d, want %d", id, own)
	}
}

func TestMergeTrailingText(t *testing.T) {
	p := mPath(t)
	db := mOpen(t, "file:"+p+"?_pragma=journal_mode(WAL)")
	mExec(t, db, schemaH)
	mExec(t, db, `CREATE TABLE t(x)`)
	st, err := db.Prepare("INSERT INTO t VALUES (?);\n-- the end\n")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Exec(0); err != nil {
		t.Fatal(err)
	}
	if err := behind(t, db, func() error {
		_, err := st.Exec(1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := mCount(t, db, `SELECT count(*) FROM t`); n != 2 {
		t.Fatalf("%d rows, want 2", n)
	}
}

func TestMergeOrFailMatchesAlone(t *testing.T) {
	p := mPath(t)
	db := mOpen(t, "file:"+p+"?_pragma=journal_mode(WAL)")
	mExec(t, db, schemaH)
	mExec(t, db, `CREATE TABLE t(x UNIQUE)`)
	mExec(t, db, `INSERT INTO t VALUES (3)`)
	const q = `INSERT OR FAIL INTO t VALUES (1), (2), (3)`
	if _, err := db.Exec(q); err == nil {
		t.Fatal("no constraint error")
	}
	alone := mCount(t, db, `SELECT count(*) FROM t`)
	mExec(t, db, `DELETE FROM t WHERE x <> 3`)
	if err := behind(t, db, func() error {
		_, err := db.Exec(q)
		return err
	}); err == nil {
		t.Fatal("no constraint error when merged")
	}
	if merged := mCount(t, db, `SELECT count(*) FROM t`); merged != alone {
		t.Fatalf("OR FAIL keeps %d rows merged and %d alone", merged, alone)
	}
}

// batchConn is a connection of its own on a new WAL database with foreign
// keys on, for tests that hand runBatch the requests in a set order.
func batchConn(t *testing.T, opts ...Option) *conn {
	t.Helper()
	cn, err := NewConnector("file:"+mPath(t)+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", opts...)
	if err != nil {
		t.Fatal(err)
	}
	dc, err := cn.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c := dc.(*conn)
	t.Cleanup(func() { c.Close() })
	return c
}

func batchReq(q string) *request {
	return &request{ctx: context.Background(), sql: q, done: make(chan struct{})}
}

func batchCount(t *testing.T, c *conn, q string) int64 {
	t.Helper()
	s, _, err := c.c.Prepare(q)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Finalize()
	if row, err := s.Step(); err != nil || !row {
		t.Fatalf("%s: row %v, %v", q, row, err)
	}
	return s.ColumnInt64(0)
}

// TestMergeNoRerun: when a statement ends the transaction of its batch, it
// gets its own error, each statement that ran before it gets
// SQLITE_ABORT_ROLLBACK over that error, those that failed alone too, and
// none runs again. The statements after it never touched SQLite and go back
// to the queue. A statement whose context ended before it stepped is
// answered alone.
func TestMergeNoRerun(t *testing.T) {
	var calls atomic.Int64
	c := batchConn(t, WithFunction("observe", 0, func([]driver.Value) (driver.Value, error) {
		calls.Add(1)
		return int64(4), nil
	}, Innocuous))
	if err := c.c.Exec(`CREATE TABLE t(x UNIQUE); INSERT INTO t VALUES (3)`); err != nil {
		t.Fatal(err)
	}
	fail := batchReq(`INSERT OR FAIL INTO t VALUES (1), (2), (3)`)
	ok := batchReq(`INSERT INTO t VALUES (observe())`)
	late := batchReq(`INSERT INTO t VALUES (6)`)
	late.ctx = &lateCtx{Context: context.Background()}
	end := batchReq(`INSERT OR ROLLBACK INTO t VALUES (3)`)
	after := batchReq(`INSERT INTO t VALUES (5)`)
	back := c.runBatch([]*request{fail, ok, late, end, after})
	if len(back) != 1 || back[0] != after {
		t.Fatalf("back %v, want only the statement after the one that ended the transaction", back)
	}
	var e *Error
	if !errors.As(end.err, &e) || e.ExtendedCode() != CONSTRAINT_UNIQUE {
		t.Fatalf("the statement that ended the transaction got %v", end.err)
	}
	for name, r := range map[string]*request{"OR FAIL": fail, "observe": ok} {
		if !errors.As(r.err, &e) || e.ExtendedCode() != ABORT_ROLLBACK {
			t.Fatalf("%s: %v, want SQLITE_ABORT_ROLLBACK", name, r.err)
		}
		if !errors.Is(r.err, end.err) {
			t.Fatalf("%s: %v does not wrap %v", name, r.err, end.err)
		}
	}
	if !errors.Is(late.err, context.Canceled) {
		t.Fatalf("the statement whose context ended got %v", late.err)
	}
	for _, r := range []*request{fail, ok, late, end} {
		select {
		case <-r.done:
		default:
			t.Fatalf("%s not answered", r.sql)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("observe() ran %d times, want 1", n)
	}
	if n := batchCount(t, c, `SELECT count(*) FROM t`); n != 1 {
		t.Fatalf("%d rows, want 1: the batch rolled back", n)
	}
}

// TestLeadNotRerun: a request handed the lock leads a batch. When a later
// statement ends the transaction, the leader ran: it gets
// SQLITE_ABORT_ROLLBACK and does not run again on its own connection.
func TestLeadNotRerun(t *testing.T) {
	c := batchConn(t)
	if err := c.c.Exec(`CREATE TABLE t(x UNIQUE); INSERT INTO t VALUES (3)`); err != nil {
		t.Fatal(err)
	}
	lead := batchReq(`INSERT INTO t VALUES (1)`)
	lead.key = c.key
	end := batchReq(`INSERT OR ROLLBACK INTO t VALUES (3)`)
	end.key = c.key
	c.wl.mu.Lock()
	c.wl.queue = append(c.wl.queue, end)
	c.wl.mu.Unlock()
	if !c.wl.lead(c, lead) {
		t.Fatal("the leader is to run alone")
	}
	var e *Error
	if !errors.As(lead.err, &e) || e.ExtendedCode() != ABORT_ROLLBACK {
		t.Fatalf("the leader got %v, want SQLITE_ABORT_ROLLBACK", lead.err)
	}
	if n := batchCount(t, c, `SELECT count(*) FROM t`); n != 1 {
		t.Fatalf("%d rows, want 1", n)
	}
}

// TestMergeOrFailDeferredForeignKey: an OR FAIL statement that fails after
// leaving a deferred foreign key unresolved is undone, as its own COMMIT
// would fail, and the next statement neither fails for it nor resolves it.
func TestMergeOrFailDeferredForeignKey(t *testing.T) {
	const fail = `INSERT OR FAIL INTO t VALUES (1, 999), (3, NULL)`
	alone := batchConn(t)
	if err := alone.c.Exec(`CREATE TABLE p(id PRIMARY KEY); CREATE TABLE t(id UNIQUE, p REFERENCES p(id) DEFERRABLE INITIALLY DEFERRED); INSERT INTO t VALUES (3, NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := alone.c.Exec(fail); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("alone: %v, want a foreign key error", err)
	}
	for _, next := range []string{`INSERT INTO good VALUES (1)`, `INSERT INTO p VALUES (999)`} {
		c := batchConn(t)
		if err := c.c.Exec(`CREATE TABLE p(id PRIMARY KEY); CREATE TABLE t(id UNIQUE, p REFERENCES p(id) DEFERRABLE INITIALLY DEFERRED); INSERT INTO t VALUES (3, NULL); CREATE TABLE good(x)`); err != nil {
			t.Fatal(err)
		}
		a, b := batchReq(fail), batchReq(next)
		c.runBatch([]*request{a, b})
		if a.err == nil || !strings.Contains(a.err.Error(), "FOREIGN KEY") {
			t.Errorf("%s: the OR FAIL statement got %v, want a foreign key error", next, a.err)
		}
		if b.err != nil {
			t.Errorf("%s: %v", next, b.err)
		}
		if n := batchCount(t, c, `SELECT count(*) FROM t`); n != 1 {
			t.Errorf("%s: t has %d rows, want 1", next, n)
		}
	}
}

// TestRefusedQueryChangesConn: a query of two statements is refused, but
// SQLite applied the PRAGMA of the second as it prepared it. The connection
// changed itself and must not run the statements of others.
func TestRefusedQueryChangesConn(t *testing.T) {
	db := mOpen(t, "file:"+mPath(t)+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	mExec(t, db, schemaH)
	mExec(t, db, `CREATE TABLE p(id PRIMARY KEY); CREATE TABLE c(pid REFERENCES p(id))`)
	leader, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer leader.Close()
	if r, err := leader.QueryContext(context.Background(), `SELECT 1; PRAGMA foreign_keys=OFF`); err == nil {
		r.Close()
		t.Fatal("a query of two statements was taken")
	}
	err = behind(t, leader, func() error { _, err := db.Exec(`INSERT INTO c VALUES (999)`); return err })
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("an orphan insert got %v, want a foreign key error", err)
	}
}

// TestMergeKeepsReadSnapshot: a connection with a query not done holds the
// snapshot it read. Alone, its write fails (SQLITE_BUSY_SNAPSHOT) once
// another connection committed. Merged, it must fail the same way, not run
// on the newer snapshot of the connection that runs the batch.
func TestMergeKeepsReadSnapshot(t *testing.T) {
	db := mOpen(t, "file:"+mPath(t)+"?_pragma=journal_mode(WAL)")
	mExec(t, db, schemaH)
	mExec(t, db, `CREATE TABLE t(x); INSERT INTO t VALUES (1)`)
	reader, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	rows, err := reader.QueryContext(context.Background(), `SELECT x FROM t`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("no row")
	}
	mExec(t, db, `INSERT INTO t VALUES (2)`)
	const q = `INSERT INTO t SELECT max(x) + 1 FROM t`
	if _, err := reader.ExecContext(context.Background(), q); err == nil {
		t.Fatal("alone, the write on an old snapshot succeeded")
	}
	err = behind(t, db, func() error {
		_, err := reader.ExecContext(context.Background(), q)
		return err
	})
	if err == nil {
		t.Fatal("merged, the write on an old snapshot succeeded")
	}
}

// TestMergeDeferForeignKeysSetting: SQLite turns defer_foreign_keys off at
// the end of each transaction, so two connections whose DSN sets it differ
// once one of them had a transaction. A DELETE that a RESTRICT key refuses
// at once on one passes on the other, so they must not merge.
func TestMergeDeferForeignKeysSetting(t *testing.T) {
	for i := range 6 {
		merged := i%2 == 1
		set := []string{"defer_foreign_keys(1)", "main.defer_foreign_keys(1)", "%22defer_foreign_keys%22=1"}[i/2]
		db := mOpen(t, "file:"+mPath(t)+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma="+set)
		mExec(t, db, schemaH)
		mExec(t, db, `CREATE TABLE t(id PRIMARY KEY, p REFERENCES t(id) ON DELETE RESTRICT); INSERT INTO t VALUES (1, NULL), (2, 1)`)
		leader, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		caller, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		del := func() error { _, err := caller.ExecContext(t.Context(), `DELETE FROM t`); return err }
		if merged {
			err = behind(t, leader, del)
		} else {
			err = del()
		}
		if err != nil {
			t.Errorf("%s, merged %v: %v", set, merged, err)
		}
		caller.Close()
		leader.Close()
	}
}

// TestMergeMinRowid: the smallest int64 is a rowid like any other, and a
// merged INSERT of it gives it as LastInsertId and last_insert_rowid().
func TestMergeMinRowid(t *testing.T) {
	db := mOpen(t, "file:"+mPath(t)+"?_pragma=journal_mode(WAL)")
	db.SetMaxOpenConns(1 << 4)
	mExec(t, db, schemaH)
	mExec(t, db, `CREATE TABLE t(id INTEGER PRIMARY KEY)`)
	caller, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer caller.Close()
	var id int64
	if err := behind(t, db, func() error {
		r, err := caller.ExecContext(t.Context(), `INSERT INTO t VALUES (?)`, int64(math.MinInt64))
		if err == nil {
			id, err = r.LastInsertId()
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var last int64
	if err := caller.QueryRowContext(t.Context(), `SELECT last_insert_rowid()`).Scan(&last); err != nil {
		t.Fatal(err)
	}
	if id != math.MinInt64 || last != math.MinInt64 {
		t.Fatalf("LastInsertId %d, last_insert_rowid() %d, want %d", id, last, int64(math.MinInt64))
	}
}

func TestReadOnlyTxHoldsNoLock(t *testing.T) {
	p := mPath(t)
	db := mOpen(t, "file:"+p+"?_pragma=journal_mode(WAL)")
	db.SetMaxOpenConns(4)
	mExec(t, db, `CREATE TABLE t(x)`)
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO t VALUES (0)`); err == nil {
		t.Fatal("a read-only transaction wrote")
	}
	done := make(chan error, 1)
	go func() { _, err := db.Exec(`INSERT INTO t VALUES (1)`); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a writer waits for a read-only transaction")
	}
	tx.Rollback()
	if n := mCount(t, db, `SELECT count(*) FROM t`); n != 1 {
		t.Fatalf("%d rows, want 1", n)
	}
}

func TestCancelBeforeStep(t *testing.T) {
	p := mPath(t)
	db := mOpen(t, "file:"+p)
	mExec(t, db, `CREATE TABLE t(x)`)
	cn, err := NewConnector("file:" + p)
	if err != nil {
		t.Fatal(err)
	}
	dc, err := cn.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := dc.(driver.ExecerContext).ExecContext(ctx, `INSERT INTO t VALUES (1)`, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Exec with a done context: %v", err)
	}
	if n := mCount(t, db, `SELECT count(*) FROM t`); n != 0 {
		t.Fatalf("a canceled Exec wrote %d rows", n)
	}
	if _, err := dc.(driver.QueryerContext).QueryContext(ctx, `SELECT 1`, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Query with a done context: %v", err)
	}
	// Query steps to the first row: a cancel while it does stops it.
	ctx2, cancel2 := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel2)
	done := make(chan error, 1)
	go func() {
		r, err := dc.(driver.QueryerContext).QueryContext(ctx2, `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c) SELECT count(*) FROM c`, nil)
		if err == nil {
			r.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Query canceled before its first row: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a query canceled before its first row runs on")
	}
}

func TestMergeCancelStopsStatement(t *testing.T) {
	p := mPath(t)
	db := mOpen(t, "file:"+p+"?_pragma=journal_mode(WAL)")
	mExec(t, db, schemaH)
	mExec(t, db, `CREATE TABLE t(x)`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	mExec(t, tx, `INSERT INTO h VALUES (1)`)
	done := make(chan error, 1)
	go func() {
		_, err := db.ExecContext(ctx, `INSERT INTO t SELECT count(*) FROM (WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c) SELECT x FROM c)`)
		done <- err
	}()
	for waiting() == 0 {
		time.Sleep(time.Millisecond)
	}
	committed := make(chan error, 1)
	go func() { committed <- tx.Commit() }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled merged statement: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceling a merged statement does not stop it")
	}
	select {
	case err := <-committed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the holder's commit does not return")
	}
	if n := mCount(t, db, `SELECT count(*) FROM h`); n != 1 {
		t.Fatalf("h has %d rows, want 1", n)
	}
}

func TestColumnsAfterSchemaChange(t *testing.T) {
	db := mOpen(t, "file:"+mPath(t))
	db.SetMaxOpenConns(1)
	mExec(t, db, `CREATE TABLE t(a)`)
	mExec(t, db, `INSERT INTO t VALUES (1)`)
	st, err := db.Prepare(`SELECT * FROM t`)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cols := func() []string {
		rows, err := st.Query()
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		c, _ := rows.Columns()
		return c
	}
	cols()
	mExec(t, db, `ALTER TABLE t RENAME COLUMN a TO b`)
	if c := cols(); len(c) != 1 || c[0] != "b" {
		t.Fatalf("columns after a rename: %v", c)
	}
}

func TestUnknownDSNParameter(t *testing.T) {
	if _, err := NewConnector("file:x.db?_foreign_keys=1"); err == nil {
		t.Fatal("an unknown driver parameter was ignored")
	}
}

func TestBusyTimeoutSetLater(t *testing.T) {
	p := mPath(t)
	db := mOpen(t, "file:"+p+"?_pragma=journal_mode(WAL)")
	db.SetMaxOpenConns(2)
	mExec(t, db, `CREATE TABLE t(x)`)
	ctx := context.Background()
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	mExec(t, c, `PRAGMA busy_timeout = 50`)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	mExec(t, tx, `INSERT INTO t VALUES (1)`)
	start := time.Now()
	if _, err := c.ExecContext(ctx, `INSERT INTO t VALUES (2)`); err == nil {
		t.Fatal("a write got past a held lock")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("waited %v for busy_timeout 50 ms", d)
	}
}

func TestSharedCacheRefused(t *testing.T) {
	_, err := NewConnector("file::memory:?cache=shared")
	if err == nil || !strings.Contains(err.Error(), "vfs=memdb") {
		t.Fatalf("cache=shared: %v", err)
	}
	db := mOpen(t, "file:/shared-memdb?vfs=memdb")
	db.SetMaxOpenConns(2)
	c1, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	if _, err := c1.ExecContext(t.Context(), `CREATE TABLE t(x); INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	c2, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	var n int
	if err := c2.QueryRowContext(t.Context(), `SELECT count(*) FROM t`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("a second connection to vfs=memdb: %d rows, %v", n, err)
	}
}

func TestNilBytesAreNull(t *testing.T) {
	db := mOpen(t, ":memory:")
	db.SetMaxOpenConns(1)
	mExec(t, db, `CREATE TABLE t(x)`)
	var b []byte
	mExec(t, db, `INSERT INTO t VALUES (?), (?)`, b, []byte{})
	var types []string
	rows, err := db.Query(`SELECT typeof(x) FROM t ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		rows.Scan(&s)
		types = append(types, s)
	}
	if strings.Join(types, ",") != "null,blob" {
		t.Fatalf("nil and empty []byte stored as %v, want null and blob", types)
	}
}

// TestHandedRequestLeadsBatch: when the lock goes to the first request of
// the queue, that request runs with the others its connection can run, in
// one commit, and not in a commit of its own.
func TestHandedRequestLeadsBatch(t *testing.T) {
	p := mPath(t)
	db := mOpen(t, "file:"+p)
	db.SetMaxOpenConns(4)
	mExec(t, db, `CREATE TABLE h(x); CREATE TABLE t(x)`)
	// Another connector: its connection merges nothing of db's.
	other := mOpen(t, "file:"+p+"?_pragma=cache_size(-1000)")
	sc, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var wl *writeLock
	sc.Raw(func(dc any) error { wl = dc.(*conn).wl; return nil })
	sc.Close()
	commits0, merged0 := wl.stats.commits.Load(), wl.stats.merged.Load()

	tx, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO h VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	const n = 3
	errs := make(chan error, n)
	for i := range n {
		go func() {
			_, err := db.Exec(`INSERT INTO t VALUES (?)`, i)
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
			t.Fatal(err)
		}
	}
	commits, merged := wl.stats.commits.Load()-commits0, wl.stats.merged.Load()-merged0
	if commits != 1 || merged != n {
		t.Fatalf("%d inserts handed the lock took %d batch commits for %d statements, want 1 for %d", n, commits, merged, n)
	}
	if c := mCount(t, db, `SELECT count(*) FROM t`); c != n {
		t.Fatalf("%d rows, want %d", c, n)
	}
}

// When SQLite rolls back a transaction itself (here RAISE(ROLLBACK) in a
// trigger), the statements after it in the sql.Tx fail: none of them may
// run on its own and commit, and Commit fails.
func TestTxEndedBySQLite(t *testing.T) {
	db := mOpen(t, "file:"+mPath(t)+"?_pragma=journal_mode(WAL)")
	mExec(t, db, `CREATE TABLE t(x); CREATE TABLE stop(x);
		CREATE TRIGGER stop_all BEFORE INSERT ON stop BEGIN SELECT RAISE(ROLLBACK, 'stopped'); END`)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	mExec(t, tx, `INSERT INTO t VALUES ('a')`)
	if _, err := tx.Exec(`INSERT INTO stop VALUES (1)`); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("the trigger: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO t VALUES ('b')`); err == nil {
		t.Error("an insert after the rollback ran")
	}
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err == nil {
		t.Error("a query after the rollback ran")
	}
	if err := tx.Commit(); err == nil {
		t.Error("Commit after the rollback succeeded")
	}
	if n := mCount(t, db, `SELECT count(*) FROM t`); n != 0 {
		t.Fatalf("t has %d rows", n)
	}
	mExec(t, db, `INSERT INTO t VALUES ('c')`)
}

// A write on one connection after another connection changed the schema
// succeeds once: SQLite runs it again inside the step, and it commits.
func TestWriteAfterSchemaChange(t *testing.T) {
	db := mOpen(t, "file:"+mPath(t)+"?_pragma=journal_mode(WAL)")
	ctx := t.Context()
	a, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := a.ExecContext(ctx, `CREATE TABLE t(x)`); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ExecContext(ctx, `INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ExecContext(ctx, `CREATE TABLE other(x)`); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ExecContext(ctx, `INSERT INTO t VALUES (2)`); err != nil {
		t.Fatalf("insert after a change of the schema: %v", err)
	}
	if n := mCount(t, db, `SELECT count(*) FROM t`); n != 2 {
		t.Fatalf("t has %d rows", n)
	}
}

// queue runs f(0) to f(n-1) at once while holder holds the write lock in a
// transaction (which wrote a row of h), and commits once all n wait.
func queue(t *testing.T, holder *sql.DB, n int, f func(i int) error) {
	t.Helper()
	tx, err := holder.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO h VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, n)
	for i := range n {
		go func() { errs <- f(i) }()
	}
	deadline := time.Now().Add(5 * time.Second)
	for waiting() < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d wait for the lock", waiting(), n)
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
}

// statsOf gives the write lock of db's file.
func statsOf(t *testing.T, db *sql.DB) *writeLock {
	t.Helper()
	sc, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	var wl *writeLock
	sc.Raw(func(dc any) error { wl = dc.(*conn).wl; return nil })
	return wl
}

// A prepared statement that SQLite prepares again after a change of the
// schema is sorted again: one that became unmergeable no longer merges,
// on the connection of its caller or on the one that runs a batch.
func TestMergeAfterSchemaChange(t *testing.T) {
	p := mPath(t)
	db := mOpen(t, "file:"+p)
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	mExec(t, db, `CREATE TABLE h(x); CREATE TABLE t(x); CREATE TABLE log(x)`)
	other := mOpen(t, "file:"+p)
	wl := statsOf(t, db)
	ps, err := db.Prepare(`INSERT INTO t VALUES (?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer ps.Close()
	const n = 8
	insert := func(i int) error { _, err := ps.Exec(i); return err }
	m0 := wl.stats.merged.Load()
	queue(t, other, n, insert)
	queue(t, other, n, insert)
	if wl.stats.merged.Load() == m0 {
		t.Fatal("nothing merged before the change")
	}
	// A write from a trigger never merges.
	mExec(t, other, `CREATE TRIGGER tr AFTER INSERT ON t BEGIN INSERT INTO log VALUES (new.x); END`)
	m0 = wl.stats.merged.Load()
	queue(t, other, n, insert)
	if m := wl.stats.merged.Load() - m0; m != 0 {
		t.Errorf("%d inserts with a trigger merged", m)
	}
	if got := mCount(t, db, `SELECT count(*) FROM log`); got != n {
		t.Errorf("%d rows in log, want %d", got, n)
	}
	// Without it again, they merge again.
	mExec(t, other, `DROP TRIGGER tr`)
	m0 = wl.stats.merged.Load()
	queue(t, other, n, insert)
	queue(t, other, n, insert)
	if wl.stats.merged.Load() == m0 {
		t.Error("nothing merged after the trigger was dropped")
	}
}

// More distinct statements than a connection caches, merged: the cache
// makes room without closing a statement a batch is running.
func TestMergeManyStatements(t *testing.T) {
	const writers, each = 16, 300
	db := mOpen(t, mPath(t))
	db.SetMaxOpenConns(writers)
	mExec(t, db, "CREATE TABLE t(x)")
	// Every connection reads the schema before the writers start, and stays
	// open. In rollback journal mode a connection cannot start a read while a
	// COMMIT holds the file's PENDING or EXCLUSIVE lock, and with writes back
	// to back SQLite's busy handler, which polls, can miss every gap between
	// them until the busy timeout. That is SQLite's, not what this test is
	// about.
	db.SetMaxIdleConns(writers)
	openAll(t, db, writers)
	wl := statsOf(t, db)
	merged0 := wl.stats.merged.Load()
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for i := range each {
				// Each statement is a text of its own.
				if _, err := db.Exec(fmt.Sprintf("INSERT INTO t VALUES (%d)", w*each+i)); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	if n := mCount(t, db, "SELECT count(DISTINCT x) FROM t"); n != writers*each {
		t.Fatalf("%d rows, want %d", n, writers*each)
	}
	// More than all the connections can cache: some made room.
	if m := wl.stats.merged.Load() - merged0; m <= writers*maxCached {
		t.Fatalf("%d statements merged, want more than %d connections cache", m, writers)
	}
}

// openAll opens n connections of db at once, each having read the schema
// (with a table t), and leaves them idle in the pool.
func openAll(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	for range n {
		c, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		// Held to the end, so that the next is another connection.
		defer c.Close()
		var x int
		if err := c.QueryRowContext(t.Context(), "SELECT count(*) FROM t").Scan(&x); err != nil {
			t.Fatal(err)
		}
	}
}

// TestBusyTimeoutSetByPrepare: SQLite sets busy_timeout when it prepares the
// PRAGMA, so the write lock waits as long even when the statement never runs.
func TestBusyTimeoutSetByPrepare(t *testing.T) {
	for _, how := range []string{"refused query", "prepare only", "syntax error"} {
		db := mOpen(t, "file:"+mPath(t)+"?_pragma=journal_mode(WAL)")
		db.SetMaxOpenConns(2)
		mExec(t, db, `CREATE TABLE t(x)`)
		c, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if how == "refused query" {
			if _, err := c.QueryContext(t.Context(), `PRAGMA busy_timeout = 0; SELECT 1`); err == nil {
				t.Fatal("a query of two statements ran")
			}
		} else if how == "syntax error" {
			if _, err := c.PrepareContext(t.Context(), `PRAGMA busy_timeout = 0 junk`); err == nil {
				t.Fatal("a statement with a syntax error was prepared")
			}
		} else {
			s, err := c.PrepareContext(t.Context(), `PRAGMA busy_timeout = 0`)
			if err != nil {
				t.Fatal(err)
			}
			s.Close()
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		mExec(t, tx, `INSERT INTO t VALUES (1)`)
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		_, err = c.ExecContext(ctx, `INSERT INTO t VALUES (2)`)
		cancel()
		if err == nil || errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s: %v, want busy at once", how, err)
		}
		tx.Rollback()
		c.Close()
	}
}

// Many goroutines insert at once until the database is full (max_page_count
// stops it growing, as a full disk would), with their inserts merged. Each
// caller gets either success or SQLITE_FULL, the table holds exactly the
// rows whose callers got success, the file stays sound, and once rows are
// deleted the writers go on.
func TestMergeDiskFull(t *testing.T) {
	db := mOpen(t, "file:"+mPath(t)+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=max_page_count(400)")
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(32)
	mExec(t, db, `CREATE TABLE t(k TEXT PRIMARY KEY, v BLOB)`)
	sc, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var wl *writeLock
	sc.Raw(func(dc any) error { wl = dc.(*conn).wl; return nil })
	sc.Close()
	merged0 := wl.stats.merged.Load()

	const goroutines, each = 32, 100
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		ok   = map[string]bool{}
		full int
	)
	for g := range goroutines {
		wg.Go(func() {
			for i := range each {
				k := fmt.Sprintf("g%d-%d", g, i)
				_, err := db.Exec(`INSERT INTO t VALUES (?, randomblob(600))`, k)
				mu.Lock()
				if err == nil {
					ok[k] = true
				} else if strings.Contains(err.Error(), "full") {
					full++
				} else {
					t.Errorf("%s: %v", k, err)
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if full == 0 {
		t.Fatal("the database never filled up")
	}
	if wl.stats.merged.Load() == merged0 {
		t.Fatal("no insert was merged")
	}
	rows, err := db.Query(`SELECT k FROM t`)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		if !ok[k] {
			t.Errorf("row %s is in the table, but its caller got an error", k)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != len(ok) {
		t.Errorf("%d rows in the table, %d callers got success", n, len(ok))
	}
	var check string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity_check: %q %v", check, err)
	}
	mExec(t, db, `DELETE FROM t WHERE rowid % 2 = 0`)
	mExec(t, db, `INSERT INTO t VALUES ('after', randomblob(600))`)
	t.Logf("%d inserts succeeded, %d got SQLITE_FULL, %d merged", len(ok), full, wl.stats.merged.Load()-merged0)
}

// A statement in the queue gets the lock in its turn, though writers no batch
// can take keep coming to wait for it: here those of another connector, whose
// connections never run the queue. Each insert of db ends long before its
// busy timeout, as the same insert in a transaction does.
func TestQueuedNotStarved(t *testing.T) {
	p := mPath(t)
	db := mOpen(t, "file:"+p+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(2000)")
	other := mOpen(t, "file:"+p+"?_pragma=busy_timeout(10000)&_pragma=cache_size(-1000)")
	other.SetMaxOpenConns(8)
	mExec(t, db, `CREATE TABLE t(x)`)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				tx, err := other.Begin()
				if err != nil {
					t.Error(err)
					return
				}
				if _, err := tx.Exec(`INSERT INTO t VALUES (0)`); err != nil {
					t.Error(err)
					tx.Rollback()
					return
				}
				time.Sleep(200 * time.Microsecond)
				if err := tx.Commit(); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	defer wg.Wait()
	defer close(stop)
	time.Sleep(100 * time.Millisecond)
	for i := range 10 {
		start := time.Now()
		if _, err := db.Exec(`INSERT INTO t VALUES (1)`); err != nil {
			t.Fatalf("insert %d after %v: %v", i, time.Since(start).Round(time.Millisecond), err)
		}
		if d := time.Since(start); d > 500*time.Millisecond {
			t.Errorf("insert %d waited %v", i, d.Round(time.Millisecond))
		}
	}
}

// A Go function not marked Innocuous may have side effects, so the statement
// that calls it is the last of its batch: the batch commits after it, and no
// failure of a later statement can make it run again. Here the statement
// after it is interrupted, which ends the whole transaction.
func TestMergeSideEffectsRunOnce(t *testing.T) {
	var calls atomic.Int64
	side := func([]driver.Value) (driver.Value, error) { calls.Add(1); return int64(1), nil }
	c := batchConn(t, WithFunction("side", 0, side), WithFunction("pure", 0, side, Innocuous))
	if err := c.c.Exec(`CREATE TABLE t(x)`); err != nil {
		t.Fatal(err)
	}
	long := `INSERT INTO t SELECT count(*) FROM (WITH RECURSIVE r(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM r) SELECT x FROM r)`
	for _, tc := range []struct {
		fn    string
		calls int64 // times the function runs
		back  int   // statements left for the next batch
	}{
		{"side", 1, 1},
		{"pure", 1, 0}, // rolled back with the batch, never run again
	} {
		t.Run(tc.fn, func(t *testing.T) {
			calls.Store(0)
			a := batchReq(`INSERT INTO t VALUES (` + tc.fn + `())`)
			b := batchReq(long)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			b.ctx = ctx
			time.AfterFunc(50*time.Millisecond, cancel)
			back := c.runBatch([]*request{a, b})
			if len(back) != tc.back {
				t.Fatalf("%d statements back, want %d", len(back), tc.back)
			}
			if tc.back == 1 && a.err != nil {
				t.Fatal(a.err)
			}
			if tc.back == 0 {
				wantRolledBack(t, a.err)
				if !errors.Is(b.err, context.Canceled) {
					t.Fatalf("the canceled statement got %v", b.err)
				}
			}
			if n := calls.Load(); n != tc.calls {
				t.Fatalf("%s() ran %d times, want %d", tc.fn, n, tc.calls)
			}
			if tc.back == 1 && back[0] != b {
				t.Fatal("the statement after side() was not left for the next batch")
			}
		})
	}
}

// Many writers, some cancelled as they run: a function with side effects
// runs once for each statement that calls it and commits, never more.
func TestMergeSideEffectsUnderCancels(t *testing.T) {
	var calls atomic.Int64
	cn, err := NewConnector("file:"+mPath(t)+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)", WithFunction("side", 0, func([]driver.Value) (driver.Value, error) {
		calls.Add(1)
		return int64(1), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(cn)
	t.Cleanup(func() { db.Close() })
	mExec(t, db, `CREATE TABLE t(x)`)
	const writers, each = 16, 40
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				if w%2 == 0 {
					db.Exec(`INSERT INTO t VALUES (side())`)
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(i%5)*time.Millisecond)
				db.ExecContext(ctx, `INSERT INTO t SELECT count(*) FROM (WITH RECURSIVE r(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM r LIMIT 20000) SELECT x FROM r)`)
				cancel()
			}
		}()
	}
	wg.Wait()
	var rows int64
	if err := db.QueryRow(`SELECT count(*) FROM t WHERE x = 1`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != rows {
		t.Fatalf("side() ran %d times for %d rows", n, rows)
	}
}

// A statement SQLite prepares again on a connection that runs a batch, and
// that is no longer mergeable, must fail before it does anything. Here the
// refusal falls on a statement R*Tree prepares as it connects again after
// the schema changed (a trigger on its shadow table writes), and R*Tree
// with an auxiliary column goes on after its prepare fails: the statement
// must still not run on the connection of the batch, and the table must
// still work there.
func TestMergeRefusedNestedPrepare(t *testing.T) {
	var calls atomic.Int64
	path := mPath(t)
	cn, err := NewConnector("file:"+path+"?_pragma=journal_mode(WAL)", WithFunction("side", 0, func([]driver.Value) (driver.Value, error) {
		calls.Add(1)
		return int64(1), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	dc, err := cn.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c := dc.(*conn)
	t.Cleanup(func() { c.Close() })
	if err := c.c.Exec(`CREATE VIRTUAL TABLE rt USING rtree(id, x0, x1, +aux); INSERT INTO rt VALUES (1, 0, 1, 'a'); CREATE TABLE t(x); CREATE TABLE log(x)`); err != nil {
		t.Fatal(err)
	}
	q := `INSERT INTO t SELECT side() FROM rt`
	if back := c.runBatch([]*request{batchReq(q)}); len(back) != 0 || calls.Load() != 1 {
		t.Fatalf("first run: %d back, %d calls", len(back), calls.Load())
	}

	other := mOpen(t, "file:"+path)
	mExec(t, other, `CREATE TRIGGER tr AFTER INSERT ON rt_node BEGIN INSERT INTO log VALUES (1); END`)

	calls.Store(0)
	a := batchReq(q)
	back := c.runBatch([]*request{a})
	if len(back) == 1 && calls.Load() != 0 {
		t.Fatalf("refused statement ran side() %d times on the connection of the batch: its caller runs it again", calls.Load())
	}
	if err := c.c.Exec(`INSERT INTO rt VALUES (2, 0, 1, 'b')`); err != nil {
		t.Fatalf("rt on the connection of the batch: %v", err)
	}
}

// A Go function without Innocuous ends its batch wherever the statement calls
// it from: a trigger, a CHECK, a generated column, a DEFAULT, an aggregate,
// a window function. One the statement never calls does not.
func TestMergeSideEffectsFromAnywhere(t *testing.T) {
	var calls atomic.Int64
	side := func([]driver.Value) (driver.Value, error) { calls.Add(1); return int64(1), nil }
	c := batchConn(t,
		WithFunction("side", 0, side),
		WithFunction("dside", 1, side, Deterministic),
		WithAggregate("aside", 1, func() Aggregate { calls.Add(1); return &effectsAgg{} }),
		WithWindow("wside", 1, func() Window { calls.Add(1); return &effectsAgg{} }),
	)
	if err := c.c.Exec(`CREATE TABLE t(x);
		CREATE TABLE tr_t(x);
		CREATE TRIGGER tr AFTER INSERT ON tr_t BEGIN SELECT side(); END;
		CREATE TABLE ck(x CHECK (dside(x) = 1));
		CREATE TABLE gc(x, y AS (dside(x)) STORED);
		CREATE TABLE df(x, y DEFAULT (dside(1)));
		CREATE TABLE u(x)`); err != nil {
		t.Fatal(err)
	}
	long := `INSERT INTO t SELECT count(*) FROM (WITH RECURSIVE r(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM r) SELECT x FROM r)`
	for _, tc := range []struct {
		q     string
		calls int64
	}{
		{`INSERT INTO tr_t VALUES (1)`, 1},
		{`INSERT INTO ck VALUES (1)`, 1},
		{`INSERT INTO gc(x) VALUES (1)`, 1},
		{`INSERT INTO df(x) VALUES (1)`, 1},
		{`INSERT INTO u SELECT aside(1)`, 1},
		{`INSERT INTO u SELECT wside(1) OVER () FROM (SELECT 1 UNION ALL SELECT 2)`, 1},
		{`INSERT INTO u SELECT aside(x) FROM t WHERE 0 GROUP BY x`, 0}, // no group
		{`INSERT INTO u SELECT 1 WHERE 0 AND side()`, 0},               // never called
	} {
		calls.Store(0)
		a, b := batchReq(tc.q), batchReq(long)
		ctx, cancel := context.WithCancel(context.Background())
		b.ctx = ctx
		time.AfterFunc(50*time.Millisecond, cancel)
		back := c.runBatch([]*request{a, b})
		cancel()
		if tc.calls == 0 {
			wantRolledBack(t, a.err)
		} else if a.err != nil {
			t.Fatalf("%s: %v", tc.q, a.err)
		}
		if n := calls.Load(); n != tc.calls {
			t.Errorf("%s: Go code ran %d times, want %d", tc.q, n, tc.calls)
		}
		if want := int(tc.calls); len(back) != want {
			t.Errorf("%s: %d statements left for the next batch, want %d", tc.q, len(back), want)
		}
	}
}

// wantRolledBack checks err is the SQLITE_ABORT_ROLLBACK of a statement whose
// batch another statement's canceled context rolled back: that cancel is not
// its own.
func wantRolledBack(t *testing.T, err error) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.ExtendedCode() != ABORT_ROLLBACK {
		t.Fatalf("%v, want SQLITE_ABORT_ROLLBACK", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("%v is a cancel of its own", err)
	}
	if !errors.As(errors.Unwrap(err), &e) || e.Code() != INTERRUPT {
		t.Fatalf("%v is not over an interrupt", err)
	}
}

type effectsAgg struct{}

func (effectsAgg) Step([]driver.Value) error    { return nil }
func (effectsAgg) Final() (driver.Value, error) { return int64(1), nil }
func (effectsAgg) Value() (driver.Value, error) { return int64(1), nil }
func (effectsAgg) Inverse([]driver.Value) error { return nil }

// EXPLAIN of a write writes nothing: it never waits for the write lock in
// the queue.
func TestExplainNotMerged(t *testing.T) {
	p := mPath(t)
	db := mOpen(t, "file:"+p)
	mExec(t, db, `CREATE TABLE t(x)`)
	other := mOpen(t, "file:"+p)
	tx, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	mExec(t, tx, `INSERT INTO t VALUES (0)`) // holds the write lock
	done := make(chan error, 1)
	go func() {
		_, err := db.Exec(`EXPLAIN INSERT INTO t VALUES (1)`)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("EXPLAIN INSERT waits for the write lock")
	}
}

// lateCtx ends the first time Done is asked for: watch asks for it after
// the statement is bound, the last moment before the step.
type lateCtx struct {
	context.Context
	ended atomic.Bool
}

func (c *lateCtx) Done() <-chan struct{} {
	c.ended.Store(true)
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (c *lateCtx) Err() error {
	if c.ended.Load() {
		return context.Canceled
	}
	return nil
}

func TestCancelBetweenBindAndStep(t *testing.T) {
	p := mPath(t)
	db := mOpen(t, "file:"+p)
	mExec(t, db, `CREATE TABLE t(x)`)
	cn, err := NewConnector("file:" + p)
	if err != nil {
		t.Fatal(err)
	}
	dc, err := cn.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Close()
	// SQLite forgets an interrupt that comes before a statement starts.
	ran := false
	if r, err := dc.(driver.QueryerContext).QueryContext(&lateCtx{Context: context.Background()}, `SELECT 1`, nil); err == nil {
		ran = true
		r.Close()
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("Query: %v", err)
	}
	if ran {
		t.Fatal("Query stepped a statement whose context had ended")
	}
	if _, err := dc.(driver.ExecerContext).ExecContext(&lateCtx{Context: context.Background()}, `INSERT INTO t VALUES (1)`, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Exec: %v", err)
	}
	if n := mCount(t, db, `SELECT count(*) FROM t`); n != 0 {
		t.Fatalf("Exec wrote %d rows after its context had ended", n)
	}
}

// A Go table named only in USING or NATURAL JOIN is read too, though
// SQLite asks the authorizer about no column of it: a write that reads it
// never merges, and a query that reads it is not kept.
func TestGoTableOnlyInJoin(t *testing.T) {
	c := batchConn(t, withUpto(nil))
	mustExec(t, c, `CREATE TABLE m(value); CREATE TABLE d(value)`)
	for _, q := range []string{
		`INSERT INTO d SELECT value FROM m JOIN upto(3) USING (value)`,
		`INSERT INTO d SELECT m.value FROM m NATURAL JOIN upto(3)`,
		`INSERT INTO d SELECT 1 FROM upto(3) JOIN m USING (value)`,
		`SELECT value FROM m JOIN upto(3) USING (value)`,
	} {
		p, _, err := c.c.Prepare(q)
		if err != nil {
			t.Fatal(err)
		}
		if p.Mergeable() || !p.Private() {
			t.Errorf("%s: mergeable %v, private %v", q, p.Mergeable(), p.Private())
		}
		p.Finalize()
	}
}

// A merged statement that SQLite prepares again after a table of its USING
// became a Go table is refused, and runs on its caller's connection.
func TestMergeTableOnlyInJoinBecomesGoTable(t *testing.T) {
	var connects atomic.Int64
	c := batchConn(t, withWords(&connects))
	mustExec(t, c, `CREATE TABLE m(word); CREATE TABLE x(word); CREATE TABLE d(word); INSERT INTO m VALUES ('a'); INSERT INTO x VALUES ('a')`)
	const q = `INSERT INTO d SELECT word FROM m JOIN x USING (word)`
	if back := c.runBatch([]*request{batchReq(q)}); len(back) != 0 {
		t.Fatalf("%d back before the change", len(back))
	}
	mustExec(t, c, `DROP TABLE x; CREATE VIRTUAL TABLE x USING words(a)`)
	r := batchReq(q)
	if back := c.runBatch([]*request{r}); len(back) != 1 || back[0] != r {
		t.Fatalf("back %v after x became a Go table, err %v", back, r.err)
	}
	if n := batchCount(t, c, `SELECT count(*) FROM d`); n != 1 {
		t.Fatalf("%d rows in d, want 1: the refused statement ran", n)
	}
}
