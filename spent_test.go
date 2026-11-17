// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liustack/sqlite/internal/csqlite"
	"github.com/liustack/sqlite/internal/vfs"
)

// The tests of this file check that a connection is not used again after
// SQLite ran out of memory, failed to write a file or found the disk full on
// it (conn.spent), and that this keeps an FTS5 index sound.

// A spentFault makes SQLite fail on one connection.
type spentFault struct {
	name string
	code int // the error the statement gives
	// set has the next statement that writes fail, and gives what undoes it.
	set func(t *testing.T, c *conn) (undo func())
}

var spentFaults = []spentFault{
	{"out of memory", NOMEM, func(t *testing.T, c *conn) func() {
		csqlite.SetAllocFault(func(n uintptr) bool { return n > 50000 })
		return allocOK
	}},
	{"a write fails", IOERR_WRITE, func(t *testing.T, c *conn) func() {
		vfs.SetFault(func(_ string, op vfs.Op) int {
			if op == vfs.OpWrite {
				return IOERR_WRITE
			}
			return vfs.OK
		})
		return func() { vfs.SetFault(nil) }
	}},
	{"the disk is full", FULL, func(t *testing.T, c *conn) func() {
		if err := c.c.Exec("PRAGMA max_page_count=8"); err != nil {
			t.Fatal(err)
		}
		return func() {
			if err := c.c.Exec("PRAGMA max_page_count=1000000"); err != nil {
				t.Fatal(err)
			}
		}
	}},
}

// spentDB is a new database with a table t, and a connection to it.
func spentDB(t *testing.T) (string, *conn) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "s.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(2000)"
	c := oomConn(t, dsn)
	if err := c.c.Exec("CREATE TABLE t(k INTEGER PRIMARY KEY, v BLOB NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	return dsn, c
}

// execOn runs q on c as database/sql would.
func execOn(c *conn, q string) error {
	_, err := c.ExecContext(context.Background(), q, nil)
	return err
}

// TestSpentConnection: after a statement fails with one of the three errors,
// database/sql is told to drop the connection, a statement sent to it anyway
// is refused before it runs, and the connection leads no batch.
func TestSpentConnection(t *testing.T) {
	const big = "INSERT INTO t(v) VALUES (randomblob(100000))"
	for _, f := range spentFaults {
		t.Run(f.name, func(t *testing.T) {
			dsn, c := spentDB(t)
			defer c.Close()
			if !c.IsValid() || !c.canMerge() {
				t.Fatal("the new connection is not usable")
			}
			undo := f.set(t, c)
			err := execOn(c, big)
			undo()
			if !isCode(err, f.code) {
				t.Fatalf("the statement gave %v, not the code %d", err, f.code)
			}
			if c.IsValid() {
				t.Error("IsValid says true: database/sql keeps the connection")
			}
			if err := c.ResetSession(context.Background()); !errors.Is(err, driver.ErrBadConn) {
				t.Errorf("ResetSession gave %v", err)
			}
			if c.canMerge() {
				t.Error("the connection may still lead a batch")
			}
			if err := execOn(c, "INSERT INTO t(v) VALUES ('after')"); !errors.Is(err, driver.ErrBadConn) {
				t.Errorf("a statement after it gave %v", err)
			}
			if s, err := c.PrepareContext(context.Background(), "SELECT 1"); !errors.Is(err, driver.ErrBadConn) {
				t.Errorf("a prepare after it gave %v", err)
				if s != nil {
					s.Close()
				}
			}
			if _, err := c.BeginTx(context.Background(), driver.TxOptions{}); !errors.Is(err, driver.ErrBadConn) {
				t.Errorf("a transaction after it gave %v", err)
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			v := oomConn(t, dsn)
			defer v.Close()
			if n := queryOne(t, v, "SELECT count(*) FROM t"); n[0] != int64(0) {
				t.Errorf("the table holds %v rows", n[0])
			}
		})
	}
}

// TestSpentInTransaction: a transaction the error left open goes on and
// commits, as it did before the driver dropped such connections: a program
// may make room after SQLITE_FULL and try again. The connection goes once
// the transaction is over.
func TestSpentInTransaction(t *testing.T) {
	dsn, c := spentDB(t)
	defer c.Close()
	ctx := context.Background()
	tx, err := c.BeginTx(ctx, driver.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := execOn(c, "INSERT INTO t(v) VALUES ('before')"); err != nil {
		t.Fatal(err)
	}
	if err := c.c.Exec("PRAGMA max_page_count=8"); err != nil {
		t.Fatal(err)
	}
	// A statement of many rows that a constraint may stop has a journal of
	// its own, so SQLITE_FULL undoes the statement and not the transaction.
	const many = "WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 100) " +
		"INSERT INTO t(v) SELECT randomblob(4000) FROM n"
	if err := execOn(c, many); !isCode(err, FULL) {
		t.Fatalf("the statement gave %v", err)
	}
	if c.c.AutoCommit() {
		t.Fatal("SQLITE_FULL ended the transaction: the test needs one it leaves open")
	}
	if err := c.c.Exec("PRAGMA max_page_count=1000000"); err != nil {
		t.Fatal(err)
	}
	if err := execOn(c, "INSERT INTO t(v) VALUES ('after')"); err != nil {
		t.Fatalf("a statement of the open transaction gave %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if c.IsValid() {
		t.Error("IsValid says true once the transaction is over")
	}
	if err := execOn(c, "INSERT INTO t(v) VALUES ('later')"); !errors.Is(err, driver.ErrBadConn) {
		t.Errorf("a statement after the transaction gave %v", err)
	}
	c.Close()
	v := oomConn(t, dsn)
	defer v.Close()
	if n := queryOne(t, v, "SELECT count(*) FROM t"); n[0] != int64(2) {
		t.Errorf("the table holds %v rows, not the 2 of the transaction", n[0])
	}
}

// A heldConn is a connection with the statements it holds prepared: enough
// of them fill SQLite's pool of small allocations (lookaside), so that the
// allocations of a statement reach the allocator the test makes fail.
type heldConn struct {
	c    *conn
	held []*csqlite.Stmt
}

func openHeld(t *testing.T, dsn string, hold int) *heldConn {
	t.Helper()
	h := &heldConn{c: oomConn(t, dsn)}
	for i := range hold {
		s, _, err := h.c.c.Prepare(fmt.Sprintf("SELECT %d, k FROM f WHERE k = ?", i))
		if err != nil {
			t.Fatal(err)
		}
		h.held = append(h.held, s)
	}
	return h
}

func (h *heldConn) close() {
	for _, s := range h.held {
		s.Finalize()
	}
	h.c.Close()
}

// TestFaultThenFTS5: two connections take turns inserting into an FTS5 table
// in batches while one allocation or one write fails, each point in turn,
// and a connection goes on as long as database/sql would keep it. The index
// matches the table afterwards.
//
// FTS5 reads its count of rows with a statement of its own as it inserts.
// When that read fails with one of the three errors SQLite rolls the
// transaction back at once, inside the INSERT, and FTS5 then marks the count
// it failed to read (zero) as read. The next INSERT of the connection wrote
// that count to the file.
func TestFaultThenFTS5(t *testing.T) {
	long := func(round, i int) string {
		var b strings.Builder
		for w := range 400 {
			fmt.Fprintf(&b, "w%dr%dx%dq%d ", w, round, i, w*7919%1000)
		}
		return b.String()
	}
	short := func(round, i int) string { return fmt.Sprintf("r%dx%d", round, i) }
	const spill = "&_pragma=cache_size(8)&_pragma=synchronous(OFF)"
	both := []string{"WAL", "DELETE"}
	// hit says the n-th call of it is the one that fails.
	type hitFunc func() bool
	cases := []struct {
		name string
		text func(round, i int) string
		dsn  string // more of the DSN
		// modes are the journal modes it runs in.
		modes []string
		// holds are the numbers of statements the connections hold (heldConn).
		holds []int
		// The points tried are step apart, and quick apart with -short.
		step, quick int64
		// on has the call for which hit says so fail. cur gives the
		// connection that runs then.
		on func(hit hitFunc, cur func() *conn)
	}{
		{"out of memory", short, "", both, []int{0, 60}, 1, 7, func(hit hitFunc, _ func() *conn) {
			csqlite.SetAllocFault(func(uintptr) bool { return hit() })
		}},
		// An interrupt is none of the three errors: the connection it stops
		// stays in use, and the index must match the table all the same.
		{"an interrupt", short, "", both, []int{0, 60}, 3, 21, func(hit hitFunc, cur func() *conn) {
			csqlite.SetAllocFault(func(uintptr) bool {
				if hit() {
					cur().c.Interrupt()
				}
				return false
			})
		}},
		// The rows are long and the page cache small, so that SQLite writes
		// pages in the middle of a statement. No sync, which the test does
		// not make fail: it only takes time.
		{"an interrupt, long rows", long, spill, both, []int{0}, 97, 997, func(hit hitFunc, cur func() *conn) {
			csqlite.SetAllocFault(func(uintptr) bool {
				if hit() {
					cur().c.Interrupt()
				}
				return false
			})
		}},
		// With rows of this length a write that fails leaves the count
		// wrong in the rollback journal modes only, so the two cases do not
		// take the time of WAL.
		{"a write fails", long, spill, []string{"DELETE"}, []int{0}, 1, 5, func(hit hitFunc, _ func() *conn) {
			vfs.SetFault(func(_ string, op vfs.Op) int {
				if op == vfs.OpWrite && hit() {
					return IOERR_WRITE
				}
				return vfs.OK
			})
		}},
		{"the disk is full", long, spill, []string{"DELETE"}, []int{0}, 1, 5, func(hit hitFunc, _ func() *conn) {
			vfs.SetFault(func(_ string, op vfs.Op) int {
				if op == vfs.OpWrite && hit() {
					return FULL
				}
				return vfs.OK
			})
		}},
	}
	off := func() {
		csqlite.SetAllocFault(nil)
		vfs.SetFault(nil)
	}
	t.Cleanup(off)
	for _, mode := range both {
		for _, tc := range cases {
			if !slices.Contains(tc.modes, mode) {
				continue
			}
			for _, hold := range tc.holds {
				t.Run(fmt.Sprintf("%s/%s/holding %d", mode, tc.name, hold), func(t *testing.T) {
					step := tc.step
					if testing.Short() {
						step = tc.quick
					}
					root := t.TempDir()
					for n := int64(1); ; n += step {
						dir, err := os.MkdirTemp(root, "n")
						if err != nil {
							t.Fatal(err)
						}
						dsn := "file:" + filepath.Join(dir, "f.db") + "?_pragma=journal_mode(" + mode + ")&_pragma=busy_timeout(200)" + tc.dsn
						setup := oomConn(t, dsn)
						if err := setup.c.Exec(`CREATE VIRTUAL TABLE f USING fts5(k); INSERT INTO f VALUES ('a b c'), ('d e f')`); err != nil {
							t.Fatal(err)
						}
						setup.Close()
						cs := []*heldConn{openHeld(t, dsn, hold), openHeld(t, dsn, hold)}
						var seen, failed int64
						hit := func() bool {
							seen++
							if seen == n {
								failed++
								return true
							}
							return false
						}
						var running *conn
						cur := func() *conn { return running }
						tc.on(hit, cur)
						var all []*request
						for round := range 6 {
							// What database/sql does with a connection that
							// says it is no longer valid. Both go before
							// either comes back: a WAL whose sync failed
							// opens again once no connection has it open.
							var gone []int
							for i, h := range cs {
								if !h.c.IsValid() {
									off()
									h.close()
									gone = append(gone, i)
								}
							}
							for _, i := range gone {
								cs[i] = openHeld(t, dsn, hold)
							}
							if len(gone) > 0 {
								tc.on(hit, cur)
							}
							h := cs[round%2]
							running = h.c
							var batch []*request
							for i := range 3 {
								batch = append(batch, batchReq(fmt.Sprintf(`INSERT INTO f VALUES ('%s')`, tc.text(round, i))))
							}
							all = append(all, batch...)
							for try := 0; len(batch) > 0 && try < 3 && h.c.canMerge(); try++ {
								batch = h.c.runBatch(batch)
							}
						}
						off()
						for _, h := range cs {
							h.close()
						}
						if failed == 0 {
							return // past the last point
						}
						v := oomConn(t, dsn)
						check := queryOne(t, v, `PRAGMA integrity_check`)
						rows := queryOne(t, v, `SELECT count(*) FROM f`)
						v.Close()
						if len(check) != 1 || check[0] != "ok" {
							for _, r := range all {
								t.Logf("%.40s: %v", r.sql, r.err)
							}
							t.Fatalf("point %d: integrity_check says %v", n, check)
						}
						told := int64(2)
						for _, r := range all {
							select {
							case <-r.done:
								if r.err == nil {
									told++
								}
							default:
							}
						}
						if rows[0] != told {
							t.Fatalf("point %d: %d statements were told they committed, and the table holds %v rows", n, told-2, rows[0])
						}
						if err := os.RemoveAll(dir); err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		}
	}
}

// TestSpentBetweenStatements: a connection that becomes spent between two
// statements of one Exec does not answer driver.ErrBadConn for the second:
// database/sql would run the whole Exec again on another connection, the
// first statement with it, which is committed.
//
// c leads a batch with its first statement and a statement of d that is too
// long to prepare with the memory the test leaves: c is spent, and its own
// statement commits.
func TestSpentBetweenStatements(t *testing.T) {
	dsn, c := spentDB(t)
	defer c.Close()
	d := oomConn(t, dsn)
	defer d.Close()
	// x holds the write lock first. It has a key of its own, so it runs
	// nothing of the queue as it gives the lock back: the lock goes to c.
	x := oomConn(t, dsn+"&_pragma=cache_size(100)")
	defer x.Close()
	ctx := context.Background()
	tx, err := x.BeginTx(ctx, driver.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := execOn(x, "INSERT INTO t(k, v) VALUES (100, 'x')"); err != nil {
		t.Fatal(err)
	}
	queued := func(n int) {
		t.Helper()
		for range 2000 {
			c.wl.mu.Lock()
			got := len(c.wl.queue)
			c.wl.mu.Unlock()
			if got == n {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("the queue never held %d requests", n)
	}
	cDone, dDone := make(chan error, 1), make(chan error, 1)
	go func() {
		cDone <- execOn(c, "INSERT INTO t(k, v) VALUES (1, 'c'); INSERT INTO t(k, v) VALUES (2, 'c')")
	}()
	queued(1)
	go func() {
		dDone <- execOn(d, "INSERT INTO t(k, v) VALUES (3, '"+strings.Repeat("d", 200000)+"')")
	}()
	queued(2)
	csqlite.SetAllocFault(func(n uintptr) bool { return n > 100000 })
	defer allocOK()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	cErr, dErr := <-cDone, <-dDone
	allocOK()
	if !isCode(dErr, NOMEM) {
		t.Fatalf("the long statement gave %v: the test needs it to run out of memory on c", dErr)
	}
	if !c.c.Spent() {
		t.Fatal("c is not spent: the test needs c to lead the batch")
	}
	if cErr == nil || errors.Is(cErr, driver.ErrBadConn) {
		t.Errorf("the Exec gave %v", cErr)
	}
	v := oomConn(t, dsn)
	defer v.Close()
	if got := queryOne(t, v, "SELECT group_concat(k) FROM (SELECT k FROM t ORDER BY k)"); got[0] != "1,100" {
		t.Errorf("the table holds the rows %v, not 1 and 100", got[0])
	}
}

// TestCheckpointFailureKeepsConnection: the automatic checkpoint that fails
// after a commit does not make the connection spent. The second statement
// of the Exec runs, and the first runs once.
func TestCheckpointFailureKeepsConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.db")
	db := mOpen(t, "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(1)")
	db.SetMaxOpenConns(1)
	mExec(t, db, "CREATE TABLE t(k INTEGER)")
	// Writes of the WAL go on, and the checkpoint cannot write the database.
	var failed atomic.Int64
	vfs.SetFault(func(p string, op vfs.Op) int {
		if op == vfs.OpWrite && strings.HasSuffix(p, "k.db") {
			failed.Add(1)
			return IOERR_WRITE
		}
		return vfs.OK
	})
	defer vfs.SetFault(nil)
	_, err := db.Exec("INSERT INTO t VALUES (1); INSERT INTO t VALUES (2)")
	vfs.SetFault(nil)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Load() == 0 {
		t.Fatal("no write of the database failed: the test needs a checkpoint that fails")
	}
	if n := mCount(t, db, "SELECT count(*) FROM t WHERE k = 1"); n != 1 {
		t.Errorf("the first statement left %d rows", n)
	}
	if n := mCount(t, db, "SELECT count(*) FROM t WHERE k = 2"); n != 1 {
		t.Errorf("the second statement left %d rows", n)
	}
}
