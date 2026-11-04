// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"strings"
	"sync"
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
	for _, l := range writeLocks.m {
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
	var bad, good error
	err := behind(t, db, func() error {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, bad = db.Exec(`INSERT INTO c VALUES (7)`) }()
		for waiting() == 0 {
			time.Sleep(time.Millisecond)
		}
		go func() { defer wg.Done(); _, good = db.Exec(`INSERT INTO ok VALUES (1)`) }()
		wg.Wait()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
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
