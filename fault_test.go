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
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/liustack/sqlite/internal/csqlite"
	"github.com/liustack/sqlite/internal/vfs"
)

// failOp has the n-th op on a file whose name ends with suffix fail with
// rc, once, for the rest of the test.
func failOp(t *testing.T, suffix string, op vfs.Op, n int64, rc int) *atomic.Int64 {
	var seen, failed atomic.Int64
	vfs.SetFault(func(path string, o vfs.Op) int {
		if o == op && strings.HasSuffix(path, suffix) && seen.Add(1) == n {
			failed.Add(1)
			return rc
		}
		return vfs.OK
	})
	t.Cleanup(func() { vfs.SetFault(nil) })
	return &failed
}

// Merged inserts while one write or sync of the WAL or the database fails:
// every caller told its insert committed finds its row, now and after the
// database is opened again, and the file stays sound.
func TestMergeDiskFails(t *testing.T) {
	for _, tc := range []struct {
		name, suffix string
		op           vfs.Op
		n            int64
		rc           int
	}{
		{"wal sync", "-wal", vfs.OpSync, 20, vfs.IOErrFsync},
		{"wal write", "-wal", vfs.OpWrite, 20, vfs.IOErrWrite},
		{"db sync", ".db", vfs.OpSync, 2, vfs.IOErrFsync},
		{"db write", ".db", vfs.OpWrite, 20, vfs.IOErrWrite},
	} {
		t.Run(tc.name, func(t *testing.T) { mergeDiskFails(t, tc.suffix, tc.op, tc.n, tc.rc) })
	}
}

func mergeDiskFails(t *testing.T, suffix string, op vfs.Op, n int64, rc int) {
	path := mPath(t)
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=wal_autocheckpoint(100)"
	db := mOpen(t, dsn)
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(32)
	mExec(t, db, `CREATE TABLE t(k TEXT PRIMARY KEY)`)
	failed := failOp(t, suffix, op, n, rc)

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		ok   = map[string]bool{}
		errs = map[string]error{}
	)
	for g := range 32 {
		wg.Go(func() {
			for i := range 50 {
				k := fmt.Sprintf("g%d-%d", g, i)
				_, err := db.Exec(`INSERT INTO t VALUES (?)`, k)
				mu.Lock()
				if err == nil {
					ok[k] = true
				} else {
					errs[k] = err
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	vfs.SetFault(nil)
	if failed.Load() != 1 {
		t.Fatalf("%d syncs failed, want 1", failed.Load())
	}
	check := func(db interface {
		Query(string, ...any) (*sql.Rows, error)
	}, when string) {
		t.Helper()
		have := map[string]bool{}
		rows, err := db.Query(`SELECT k FROM t`)
		if err != nil {
			t.Fatalf("%s: %v", when, err)
		}
		for rows.Next() {
			var k string
			rows.Scan(&k)
			have[k] = true
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("%s: %v", when, err)
		}
		lost, ghost := 0, 0
		for k := range ok {
			if !have[k] {
				lost++
			}
		}
		for k := range errs {
			if have[k] {
				ghost++
			}
		}
		kinds := map[string]int{}
		for _, err := range errs {
			kinds[err.Error()]++
		}
		t.Logf("%s: %d committed, %d failed %v, %d rows, %d told committed but missing, %d told failed but present", when, len(ok), len(errs), kinds, len(have), lost, ghost)
		if lost > 0 {
			t.Errorf("%s: %d rows told committed are missing", when, lost)
		}
	}
	check(db, "after the failure")
	db.Close()
	db2 := mOpen(t, dsn)
	check(db2, "opened again")
	var ic string
	if err := db2.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
		t.Fatalf("integrity_check: %q %v", ic, err)
	}
}

// setControlFault makes execCached fail as f says for the rest of the test.
func setControlFault(t *testing.T, f func(c *conn, sql string) error) {
	controlFault = f
	t.Cleanup(func() { controlFault = nil })
}

// twoConns opens two connections of one connector on a new database.
func twoConns(t *testing.T) (*conn, *conn) {
	t.Helper()
	cn, err := NewConnector("file:" + mPath(t) + "?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	var cs [2]*conn
	for i := range cs {
		dc, err := cn.Connect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		cs[i] = dc.(*conn)
		t.Cleanup(func() { cs[i].Close() })
	}
	return cs[0], cs[1]
}

var errNoMem = csqlite.NewError(NOMEM, "out of memory", nil)

// failRollback has RELEASE merge and ROLLBACK fail, as they would when the
// connection cannot prepare them: the transaction of the batch stays open.
func failRollback(_ *conn, sql string) error {
	if sql == "RELEASE merge" || sql == "ROLLBACK" {
		return errNoMem
	}
	return nil
}

// A batch whose transaction will not roll back keeps the lock on its
// connection, and the requests it did not get to wait in the queue. Closing
// the connection rolls the transaction back, and only then does the lock go
// on.
func TestBatchRollbackFails(t *testing.T) {
	c, other := twoConns(t)
	mustExec(t, c, `CREATE TABLE t(x)`)
	a, b := batchReq(`INSERT INTO t VALUES (1)`), batchReq(`INSERT INTO t VALUES (2)`)
	a.key, b.key = c.key, c.key
	wl := c.wl
	wl.mu.Lock()
	wl.held = true
	wl.queue = append(wl.queue, a, b)
	wl.mu.Unlock()
	setControlFault(t, failRollback)
	wl.release(c)
	if !errors.Is(a.err, errNoMem) {
		t.Fatalf("the statement whose RELEASE failed got %v", a.err)
	}
	if !c.bad || c.c.AutoCommit() || !c.writing {
		t.Fatalf("bad %v, autocommit %v, writing %v", c.bad, c.c.AutoCommit(), c.writing)
	}
	wl.mu.Lock()
	held, queued := wl.held, slices.Contains(wl.queue, b)
	wl.mu.Unlock()
	if !held || !queued || b.handed {
		t.Fatalf("held %v, b queued %v, b handed %v", held, queued, b.handed)
	}
	controlFault = nil
	// The committed callback runs as the lock goes on.
	var autocommit bool
	wl.committed.Store(&[]func(uint32){func(uint32) { autocommit = c.c.AutoCommit() }}[0])
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if !autocommit {
		t.Fatal("the lock went on while the transaction was open")
	}
	select {
	case <-b.done:
	default:
		t.Fatal("the lock did not go to the queued request")
	}
	if !b.handed {
		t.Fatal("b was answered, not handed the lock")
	}
	if n := countOf(t, other, `SELECT count(*) FROM t`); n != 0 {
		t.Fatalf("%d rows: the batch committed", n)
	}
}

func countOf(t *testing.T, c *conn, q string) int64 {
	t.Helper()
	got := queryOne(t, c, q)
	return got[0].(int64)
}

// A request handed the lock that leads a batch and goes back to its own
// connection must not run there when the batch would not roll back: it
// gets driver.ErrBadConn, having never run.
func TestLeadRollbackFails(t *testing.T) {
	c, other := twoConns(t)
	mustExec(t, c, `CREATE TABLE t(x)`)
	lead := batchReq(`INSERT INTO t VALUES (1); SELECT 1`) // never merges
	lead.key, lead.handed = c.key, true
	e := batchReq(`INSERT INTO t VALUES (2)`)
	e.key = c.key
	c.wl.mu.Lock()
	c.wl.held = true
	c.wl.queue = append(c.wl.queue, e)
	c.wl.mu.Unlock()
	setControlFault(t, failRollback)
	if _, err := c.answered(context.Background(), nil, lead); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("the leader got %v, want driver.ErrBadConn", err)
	}
	if !errors.Is(e.err, errNoMem) {
		t.Fatalf("the statement whose RELEASE failed got %v", e.err)
	}
	controlFault = nil
	c.Close()
	if n := countOf(t, other, `SELECT count(*) FROM t`); n != 0 {
		t.Fatalf("%d rows", n)
	}
}

// When writing a virtual table's index ends the transaction of a batch, the
// statement gets that error, though it had failed alone before, and the
// others get it under SQLITE_ABORT_ROLLBACK.
func TestFlushEndsBatch(t *testing.T) {
	c := batchConn(t)
	mustExec(t, c, `CREATE VIRTUAL TABLE f USING fts5(x); CREATE TABLE u(x UNIQUE); INSERT INTO u VALUES (1)`)
	errFull := csqlite.NewError(FULL, "database or disk is full", nil)
	flushes := 0
	setControlFault(t, func(c *conn, sql string) error {
		if sql != "SAVEPOINT flush" {
			return nil
		}
		if flushes++; flushes < 2 {
			return nil
		}
		c.c.Exec("ROLLBACK") // a full disk as the index is written
		return errFull
	})
	a := batchReq(`INSERT INTO f VALUES ('x')`)
	b := batchReq(`INSERT OR FAIL INTO u VALUES (2), (1)`)
	c.runBatch([]*request{a, b})
	if b.err != errFull {
		t.Fatalf("the statement got %v, want what ended the transaction", b.err)
	}
	var e *Error
	if !errors.As(a.err, &e) || e.ExtendedCode() != ABORT_ROLLBACK || errors.Unwrap(a.err) != errFull {
		t.Fatalf("the statement before got %v", a.err)
	}
}
