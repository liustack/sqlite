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
