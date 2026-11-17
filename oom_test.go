// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/liustack/sqlite/internal/csqlite"
)

// The tests of this file take memory away from SQLite (csqlite.SetAllocFault)
// while the driver writes. Whatever allocation fails, a caller told that its
// statement committed finds what it wrote, a caller told that it failed finds
// nothing of it, no statement runs twice, and the connection and the file
// are sound afterwards.

// oomFault counts the allocations of the C and says which ones failed.
type oomFault struct{ seen, failed atomic.Int64 }

// failAlloc has the n-th allocation of the C from now on fail, and with
// after every one that follows it, until the test ends or allocOK.
func failAlloc(t *testing.T, n int64, after bool) *oomFault {
	f := &oomFault{}
	csqlite.SetAllocFault(func(uintptr) bool {
		k := f.seen.Add(1)
		if k == n || after && k > n {
			f.failed.Add(1)
			return true
		}
		return false
	})
	t.Cleanup(allocOK)
	return f
}

// allocOK gives SQLite its memory back.
func allocOK() { csqlite.SetAllocFault(nil) }

// oomConn is a connection of its own to the database at dsn.
func oomConn(t *testing.T, dsn string) *conn {
	t.Helper()
	cn, err := NewConnector(dsn)
	if err != nil {
		t.Fatal(err)
	}
	dc, err := cn.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return dc.(*conn)
}

// oomReq is a statement of a batch and how to tell that it is in the
// database.
type oomReq struct {
	*request
	name string
	// applied is a query that gives 1 when the database holds what the
	// statement does, and 0 when it holds nothing of it.
	applied string
	// refused says a constraint refuses the statement whatever the memory.
	refused bool
}

// oomBatch is a batch of every kind of statement the queue runs: bound
// values, a row with overflow pages, a constraint that refuses one, an
// update, a delete, and a row of a virtual table.
func oomBatch() []*oomReq {
	arg := func(v ...any) []driver.NamedValue {
		var a []driver.NamedValue
		for i, x := range v {
			a = append(a, driver.NamedValue{Ordinal: i + 1, Value: x})
		}
		return a
	}
	rs := []*oomReq{
		{request: batchReq(`INSERT INTO t(k, v) VALUES (?, ?)`), name: "insert",
			applied: `SELECT count(*) FROM t WHERE k = 'a' AND length(v) = 3000`},
		{request: batchReq(`INSERT INTO t(k) VALUES ('seed')`), name: "refused", refused: true,
			applied: `SELECT count(*) - 1 FROM t WHERE k = 'seed'`},
		{request: batchReq(`UPDATE t SET v = ? WHERE k = 'seed'`), name: "update",
			applied: `SELECT count(*) FROM t WHERE k = 'seed' AND v = 'new'`},
		{request: batchReq(`INSERT INTO t(k, v) VALUES ('b', zeroblob(30000))`), name: "large insert",
			applied: `SELECT count(*) FROM t WHERE k = 'b'`},
		{request: batchReq(`DELETE FROM t WHERE k = 'gone'`), name: "delete",
			applied: `SELECT 1 - count(*) FROM t WHERE k = 'gone'`},
		{request: batchReq(`INSERT INTO f(body) VALUES ('alpha beta')`), name: "fts5 insert",
			applied: `SELECT count(*) FROM f WHERE f MATCH 'alpha'`},
	}
	rs[0].args = arg("a", bytes.Repeat([]byte{7}, 3000))
	rs[2].args = arg("new")
	return rs
}

// oomCases are the states a batch is run in.
var oomCases = []struct {
	name string
	// hold is how many statements the connection holds prepared: enough of
	// them fill the pool SQLite keeps for the small allocations of a
	// connection (lookaside), and each one the batch makes then asks the
	// system, as on a busy connection.
	hold int
	// first is the statement of oomBatch the batch starts with.
	first int
}{
	{"new connection", 0, 0},
	{"pool full", 60, 0},
	{"pool full, virtual table first", 60, 5},
}

// TestBatchOutOfMemory runs a batch of the queue with each allocation of the
// C failing in turn, once or from then on, until a run meets no failure.
func TestBatchOutOfMemory(t *testing.T) {
	for _, mode := range []string{"WAL", "DELETE"} {
		for _, tc := range oomCases {
			for _, after := range []bool{false, true} {
				name := mode + "/" + tc.name + "/once"
				if after {
					name = mode + "/" + tc.name + "/from then on"
				}
				t.Run(name, func(t *testing.T) {
					dir := t.TempDir()
					step := int64(1)
					if testing.Short() {
						step = 7
					}
					var runs, bad int
					for n := int64(1); ; n += step {
						dsn := "file:" + filepath.Join(dir, fmt.Sprintf("n%d.db", n)) +
							"?_pragma=journal_mode(" + mode + ")&_pragma=foreign_keys(1)"
						failed, wasBad := batchOutOfMemory(t, dsn, tc.hold, tc.first, n, after)
						if t.Failed() {
							t.Fatalf("allocation %d", n)
						}
						if failed == 0 {
							break
						}
						runs++
						if wasBad {
							bad++
						}
					}
					t.Logf("%d runs, %d left the connection unusable", runs, bad)
					if runs < 100/int(step) {
						t.Fatalf("only %d runs met a failure: the batch allocates more than that", runs)
					}
				})
			}
		}
	}
}

// batchOutOfMemory runs oomBatch, from its statement first on, on a new
// connection that holds hold statements, with the n-th allocation failing.
// It gives how many allocations failed, and whether the connection was left
// unusable.
func batchOutOfMemory(t *testing.T, dsn string, hold, first int, n int64, after bool) (int64, bool) {
	t.Helper()
	setup := oomConn(t, dsn)
	if err := setup.c.Exec(`CREATE TABLE t(id INTEGER PRIMARY KEY, k TEXT UNIQUE, v);
		CREATE VIRTUAL TABLE f USING fts5(body);
		INSERT INTO t(k) VALUES ('seed'), ('gone')`); err != nil {
		t.Fatal(err)
	}
	setup.Close()

	c := oomConn(t, dsn)
	var held []*csqlite.Stmt
	// closeConn closes c with the statements it holds.
	closeConn := func() {
		for _, s := range held {
			s.Finalize()
		}
		held = nil
		c.Close()
	}
	defer func() { closeConn() }()
	for i := range hold {
		s, _, err := c.c.Prepare(fmt.Sprintf("SELECT %d, k, v FROM t WHERE k = ?", i))
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, s)
	}
	reqs := oomBatch()
	reqs = append(reqs[first:], reqs[:first]...)
	batch := make([]*request, len(reqs))
	for i, r := range reqs {
		batch[i] = r.request
	}
	fault := failAlloc(t, n, after)
	back := c.runBatch(batch)
	allocOK()

	wasBad := c.bad || c.c.Broken()
	if !after && wasBad {
		t.Errorf("one failed allocation left the connection unusable (bad %v, broken %v)", c.bad, c.c.Broken())
	}
	if !wasBad && !c.c.AutoCommit() {
		t.Errorf("the batch left its transaction open")
	}
	// The statements that never reached SQLite wait for the next batch, on
	// this connection or, when it is unusable, on another.
	for round := 0; len(back) > 0; round++ {
		if round == 3 {
			t.Fatalf("%d statements still wait after 3 batches", len(back))
		}
		if c.bad || c.c.Broken() {
			closeConn()
			c = oomConn(t, dsn)
		}
		back = c.runBatch(back)
	}
	if !c.bad && !c.c.Broken() {
		later := batchReq(`INSERT INTO t(k) VALUES ('later')`)
		if back := c.runBatch([]*request{later}); len(back) != 0 || later.err != nil {
			t.Errorf("the next batch on the connection: back %d, %v", len(back), later.err)
		}
	}
	closeConn()

	v := oomConn(t, dsn)
	defer v.Close()
	for _, r := range reqs {
		select {
		case <-r.done:
		default:
			t.Errorf("%s: not answered", r.name)
			continue
		}
		applied := batchCount(t, v, r.applied)
		if applied != 0 && applied != 1 {
			t.Errorf("%s: %s gives %d: it ran more than once, or in part", r.name, r.applied, applied)
		}
		switch {
		case r.refused && r.err == nil:
			t.Errorf("%s: no error", r.name)
		case r.err == nil && applied != 1:
			t.Errorf("%s: told committed, and the database does not hold it", r.name)
		case r.err != nil && applied != 0:
			t.Errorf("%s: told %v, and the database holds it", r.name, r.err)
		}
		if r.err != nil && !oomError(r.err) && !(r.refused && isCode(r.err, CONSTRAINT_UNIQUE)) {
			t.Errorf("%s: %v, which does not say that memory ran out", r.name, r.err)
		}
	}
	if s := queryOne(t, v, `PRAGMA integrity_check`); len(s) != 1 || s[0] != "ok" {
		t.Errorf("integrity_check: %v", s)
	}
	return fault.failed.Load(), wasBad
}

// isCode says whether err is SQLite's error of the extended code.
func isCode(err error, code int) bool {
	var e *Error
	return errors.As(err, &e) && e.ExtendedCode() == code
}

// oomError says whether err tells its caller that memory ran out: SQLite's
// own error for it, or that of a statement whose transaction was rolled back
// by it.
func oomError(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	if e.ExtendedCode() == ABORT_ROLLBACK {
		return oomError(errors.Unwrap(err))
	}
	return e.Code() == NOMEM || e.ExtendedCode() == IOERR_NOMEM
}

// TestExecOutOfMemory runs one statement alone on a connection, as Exec
// does when no other writer is there, with each allocation of the C failing
// in turn: a caller told that it failed finds nothing of it, a caller told
// that it committed finds it.
func TestExecOutOfMemory(t *testing.T) {
	for _, tc := range []struct {
		name, sql, applied string
	}{
		{"insert", `INSERT INTO t(k, v) VALUES ('a', ?)`, `SELECT count(*) FROM t WHERE k = 'a'`},
		{"insert and a newline", "INSERT INTO t(k, v) VALUES ('a', ?);\n", `SELECT count(*) FROM t WHERE k = 'a'`},
		{"insert and a comment", "INSERT INTO t(k, v) VALUES ('a', ?); -- done\n /* all */ ;", `SELECT count(*) FROM t WHERE k = 'a'`},
		{"fts5 insert", `INSERT INTO f(body) VALUES (?)`, `SELECT count(*) FROM f WHERE f MATCH 'alpha'`},
	} {
		for _, hold := range []int{0, 60} {
			t.Run(fmt.Sprintf("%s/holding %d", tc.name, hold), func(t *testing.T) {
				dir := t.TempDir()
				step := int64(1)
				if testing.Short() {
					step = 7
				}
				runs := 0
				for n := int64(1); ; n += step {
					dsn := "file:" + filepath.Join(dir, fmt.Sprintf("n%d.db", n)) + "?_pragma=journal_mode(WAL)"
					setup := oomConn(t, dsn)
					if err := setup.c.Exec(`CREATE TABLE t(id INTEGER PRIMARY KEY, k TEXT UNIQUE, v);
						CREATE VIRTUAL TABLE f USING fts5(body)`); err != nil {
						t.Fatal(err)
					}
					setup.Close()
					c := oomConn(t, dsn)
					var held []*csqlite.Stmt
					for i := range hold {
						s, _, err := c.c.Prepare(fmt.Sprintf("SELECT %d, k, v FROM t WHERE k = ?", i))
						if err != nil {
							t.Fatal(err)
						}
						held = append(held, s)
					}
					fault := failAlloc(t, n, false)
					_, err := c.ExecContext(context.Background(), tc.sql, []driver.NamedValue{{Ordinal: 1, Value: "alpha"}})
					allocOK()
					broken := c.bad || c.c.Broken() || !c.c.AutoCommit()
					for _, s := range held {
						s.Finalize()
					}
					c.Close()
					if fault.failed.Load() == 0 {
						if err != nil {
							t.Fatalf("with no failure: %v", err)
						}
						break
					}
					runs++
					v := oomConn(t, dsn)
					applied := batchCount(t, v, tc.applied)
					v.Close()
					switch {
					case broken:
						t.Fatalf("allocation %d: the connection is unusable, or in a transaction", n)
					case err == nil && applied != 1:
						t.Fatalf("allocation %d: told committed, and the database does not hold it", n)
					case err != nil && applied != 0:
						t.Fatalf("allocation %d: told %v, and the database holds it", n, err)
					case err != nil && !oomError(err):
						t.Fatalf("allocation %d: %v, which does not say that memory ran out", n, err)
					}
				}
				if runs == 0 {
					t.Fatal("no run met a failure")
				}
			})
		}
	}
}
