// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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

// TestMergeOutOfMemory: many goroutines insert through database/sql while
// one allocation of the C in every few hundred fails, whichever goroutine
// asks for it: in a statement of a batch, in the batch's own statements, on
// the connection that leads it or one that waits. Every caller told that its
// row committed finds it once, and no caller told that it failed finds its
// row.
func TestMergeOutOfMemory(t *testing.T) {
	path := mPath(t)
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(20000)"
	db := mOpen(t, dsn)
	const writers, each = 16, 200
	db.SetMaxOpenConns(writers)
	db.SetMaxIdleConns(writers)
	mExec(t, db, `CREATE TABLE t(k TEXT, v BLOB); CREATE VIRTUAL TABLE f USING fts5(k)`)

	every := int64(211)
	if testing.Short() {
		every = 97
	}
	var seen, failed atomic.Int64
	csqlite.SetAllocFault(func(uintptr) bool {
		if seen.Add(1)%every == 0 {
			failed.Add(1)
			return true
		}
		return false
	})
	t.Cleanup(allocOK)

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		ok   = map[string]bool{}
		errs = map[string]error{}
	)
	for g := range writers {
		wg.Go(func() {
			for i := range each {
				k := fmt.Sprintf("g%dx%d", g, i)
				var err error
				if g%2 == 0 {
					_, err = db.Exec(`INSERT INTO t VALUES (?, ?)`, k, make([]byte, 100+37*i))
				} else {
					_, err = db.Exec(`INSERT INTO f VALUES (?)`, k)
				}
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
	allocOK()
	if failed.Load() < 20 {
		t.Fatalf("%d allocations failed of %d: too few to test anything", failed.Load(), seen.Load())
	}

	check := func(db *sql.DB, when string) {
		t.Helper()
		have := map[string]int{}
		rows, err := db.Query(`SELECT k FROM t UNION ALL SELECT k FROM f`)
		if err != nil {
			t.Fatalf("%s: %v", when, err)
		}
		for rows.Next() {
			var k string
			rows.Scan(&k)
			have[k]++
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("%s: %v", when, err)
		}
		var lost, ghost, twice int
		for k, n := range have {
			if n > 1 {
				twice++
			}
			if !ok[k] {
				ghost++
				t.Logf("%s: %s was told %v", when, k, errs[k])
			}
		}
		for k := range ok {
			if have[k] == 0 {
				lost++
			}
		}
		if lost+ghost+twice > 0 {
			t.Errorf("%s: of %d told committed and %d told failed, %d rows told committed are missing, %d told failed are there, %d are there twice",
				when, len(ok), len(errs), lost, ghost, twice)
		}
	}
	kinds := map[string]int{}
	for _, err := range errs {
		if !oomError(err) {
			kinds[err.Error()]++
		}
	}
	if len(kinds) > 0 {
		t.Errorf("errors that do not say that memory ran out: %v", kinds)
	}
	t.Logf("%d of %d allocations failed: %d inserts committed, %d failed", failed.Load(), seen.Load(), len(ok), len(errs))
	if len(ok) == 0 || len(errs) == 0 {
		t.Errorf("%d committed and %d failed: want some of each", len(ok), len(errs))
	}
	check(db, "after the failures")
	db.Close()
	db2 := mOpen(t, dsn)
	check(db2, "opened again")
	var ic string
	if err := db2.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
		t.Fatalf("integrity_check: %q %v", ic, err)
	}
	if _, err := db2.Exec(`INSERT INTO f(f) VALUES ('integrity-check')`); err != nil {
		t.Fatalf("the integrity check of the fts5 table: %v", err)
	}
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

// TestQueryOutOfMemory: a query reads rows while one allocation of the C
// fails, each in turn. Each row it gives without an error is the row of the
// database, also when the next one fails: never a value cut short or empty
// in place of the one in the database. In a UTF-16 database SQLite allocates
// as the driver asks for a text, to give it as UTF-8.
func TestQueryOutOfMemory(t *testing.T) {
	const q = `SELECT k, v, b, zeroblob(70000), printf('%s-%d', substr(v, 1, 20), k), CAST(k AS TEXT), b || zeroblob(10) FROM q ORDER BY k`
	// read runs q on c and gives the rows it read, with the error that
	// ended it.
	read := func(c *conn) ([][]driver.Value, error) {
		r, err := c.QueryContext(context.Background(), q, nil)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		var out [][]driver.Value
		for {
			row := make([]driver.Value, len(r.Columns()))
			if err := r.Next(row); err == io.EOF {
				return out, nil
			} else if err != nil {
				return out, err
			}
			out = append(out, row)
		}
	}
	for _, tc := range []struct {
		encoding string
		hold     int
	}{{"UTF-8", 0}, {"UTF-8", 60}, {"UTF-16", 0}, {"UTF-16", 60}} {
		hold := tc.hold
		t.Run(fmt.Sprintf("%s/holding %d", tc.encoding, hold), func(t *testing.T) {
			// No PRAGMA in the DSN: the encoding is set before anything
			// else makes the file.
			dsn := "file:" + filepath.Join(t.TempDir(), "q.db")
			setup := oomConn(t, dsn)
			if err := setup.c.Exec(`PRAGMA encoding = '` + tc.encoding + `'; CREATE TABLE q(k INTEGER PRIMARY KEY, v TEXT, b BLOB);
				INSERT INTO q VALUES (1, 'short', x'0102'), (2, printf('%.*c', 9000, 'y'), randomblob(9000)), (3, '', x''), (4, 'héllo', zeroblob(3000))`); err != nil {
				t.Fatal(err)
			}
			if enc := queryOne(t, setup, "PRAGMA encoding"); !strings.HasPrefix(enc[0].(string), tc.encoding) {
				t.Fatalf("the database is in %v", enc[0])
			}
			setup.Close()
			open := func() (*conn, func()) {
				c := oomConn(t, dsn)
				var held []*csqlite.Stmt
				for i := range hold {
					s, _, err := c.c.Prepare(fmt.Sprintf("SELECT %d, k FROM q WHERE k = ?", i))
					if err != nil {
						t.Fatal(err)
					}
					held = append(held, s)
				}
				return c, func() {
					for _, s := range held {
						s.Finalize()
					}
					c.Close()
				}
			}
			c, closeConn := open()
			want, err := read(c)
			closeConn()
			if err != nil || len(want) != 4 {
				t.Fatalf("%d rows, %v", len(want), err)
			}
			step := int64(1)
			if testing.Short() {
				step = 5
			}
			for n := int64(1); ; n += step {
				c, closeConn := open()
				fault := failAlloc(t, n, false)
				got, err := read(c)
				allocOK()
				closeConn()
				if fault.failed.Load() == 0 {
					return
				}
				if err != nil && !oomError(err) {
					t.Fatalf("allocation %d: %v, which does not say that memory ran out", n, err)
				}
				if err == nil && len(got) != len(want) {
					t.Fatalf("allocation %d: the query gave no error and %d rows of %d", n, len(got), len(want))
				}
				for i := range got {
					for j := range got[i] {
						if !reflect.DeepEqual(got[i][j], want[i][j]) {
							t.Fatalf("allocation %d: row %d came without an error, and its column %d is %.40v, not %.40v", n, i, j, got[i][j], want[i][j])
						}
					}
				}
			}
		})
	}
}
