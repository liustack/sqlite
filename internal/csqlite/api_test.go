// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package csqlite

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func open(t *testing.T, name string) *Conn {
	t.Helper()
	c, err := Open(name, OpenReadWrite|OpenCreate|OpenURI, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	return c
}

func TestSelectOne(t *testing.T) {
	c := open(t, ":memory:")
	s, tail, err := c.Prepare("SELECT 1, 'two', 3.5, x'0405', NULL; SELECT 2")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Finalize()
	if tail != " SELECT 2" {
		t.Errorf("tail %q", tail)
	}
	row, err := s.Step()
	if err != nil || !row {
		t.Fatalf("step: %v %v", row, err)
	}
	if n := s.ColumnCount(); n != 5 {
		t.Fatalf("%d columns", n)
	}
	if v := s.ColumnInt64(0); v != 1 {
		t.Errorf("column 0: %d", v)
	}
	if v := s.ColumnText(1); v != "two" {
		t.Errorf("column 1: %q", v)
	}
	if v := s.ColumnDouble(2); v != 3.5 {
		t.Errorf("column 2: %v", v)
	}
	if v := s.ColumnBlob(3); string(v) != "\x04\x05" {
		t.Errorf("column 3: %x", v)
	}
	if v := s.ColumnType(4); v != TypeNull {
		t.Errorf("column 4 type %d", v)
	}
	if row, err := s.Step(); row || err != nil {
		t.Fatalf("second step: %v %v", row, err)
	}
}

func TestVersion(t *testing.T) {
	v, err := LibVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v != "3.54.1" {
		t.Errorf("version %q", v)
	}
}

func TestBindAndError(t *testing.T) {
	c := open(t, ":memory:")
	if err := c.Exec("CREATE TABLE t(id INTEGER PRIMARY KEY, s TEXT NOT NULL, b BLOB, f REAL)"); err != nil {
		t.Fatal(err)
	}
	s, _, err := c.Prepare("INSERT INTO t(s, b, f) VALUES (?, ?, ?)")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Finalize()
	long := strings.Repeat("x", 3<<20) // past the shadow-stack staging
	for i, v := range []string{"a", "", long} {
		s.BindText(1, v)
		s.BindBlob(2, []byte(v))
		s.BindDouble(3, float64(i)+0.5)
		if _, err := s.Step(); err != nil {
			t.Fatal(err)
		}
		if id := c.LastInsertRowID(); id != int64(i+1) {
			t.Errorf("rowid %d", id)
		}
		s.Reset()
	}
	s.BindNull(1)
	_, err = s.Step()
	var e *Error
	if !errors.As(err, &e) || e.Primary() != 19 || !strings.Contains(e.Msg, "NOT NULL") {
		t.Fatalf("NULL into NOT NULL: %v", err)
	}
	s.Reset()
	q, _, err := c.Prepare("SELECT length(s), length(b), f FROM t ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Finalize()
	for i, want := range []int64{1, 0, 3 << 20} {
		if row, err := q.Step(); !row || err != nil {
			t.Fatal(row, err)
		}
		if q.ColumnInt64(0) != want || q.ColumnInt64(1) != want || q.ColumnDouble(2) != float64(i)+0.5 {
			t.Errorf("row %d: %d %d %v", i, q.ColumnInt64(0), q.ColumnInt64(1), q.ColumnDouble(2))
		}
	}
	if _, _, err := c.Prepare("SELEC 1"); err == nil || !strings.Contains(err.Error(), "syntax error") {
		t.Errorf("syntax error: %v", err)
	}
}

// A file database in WAL mode, written by one connection and read by
// others on other goroutines at the same time.
func TestFileWALConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	w := open(t, path)
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=NORMAL",
		"CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT)"} {
		if err := w.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	if err := w.Exec("BEGIN; WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 10000) INSERT INTO t SELECT i, 'v' || i FROM n; COMMIT"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		r := open(t, path)
		if err := r.BusyTimeout(5000); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 50; k++ {
				s, _, err := r.Prepare("SELECT count(*), sum(length(v)) FROM t")
				if err != nil {
					t.Error(err)
					return
				}
				if row, err := s.Step(); !row || err != nil {
					t.Error(row, err)
				} else if n := s.ColumnInt64(0); n < 10000 {
					t.Errorf("count %d", n)
				}
				s.Finalize()
			}
		}()
	}
	if err := w.BusyTimeout(5000); err != nil {
		t.Fatal(err)
	}
	for k := 0; k < 100; k++ {
		if err := w.Exec("INSERT INTO t(v) VALUES ('more')"); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

// Only DML that changes main and touches nothing of its own connection is
// mergeable.
func TestMergeable(t *testing.T) {
	c := open(t, filepath.Join(t.TempDir(), "t.db"))
	if err := c.Exec("CREATE TABLE t(a, b); CREATE TEMP TABLE tt(a); CREATE TABLE trig(a); " +
		"CREATE TRIGGER tr AFTER INSERT ON trig BEGIN INSERT INTO t VALUES (new.a, 0); END"); err != nil {
		t.Fatal(err)
	}
	for sql, want := range map[string]bool{
		"INSERT INTO t VALUES (1, 2)":                                true,
		"UPDATE t SET b = b + 1 WHERE a = ?":                         true,
		"DELETE FROM t WHERE a IN (SELECT a FROM t)":                 true,
		"INSERT INTO main.t SELECT a, b FROM t":                      true,
		"REPLACE INTO t VALUES (1, 2)":                               true,
		"SELECT * FROM t":                                            false,
		"INSERT INTO tt VALUES (1)":                                  false,
		"INSERT INTO t SELECT a, 0 FROM tt":                          false,
		"INSERT INTO t VALUES (last_insert_rowid(), 0)":              false,
		"INSERT INTO trig VALUES (1)":                                false,
		"CREATE TABLE u(a)":                                          false,
		"PRAGMA user_version = 3":                                    false,
		"BEGIN":                                                      false,
		"WITH x(v) AS (VALUES (1)) INSERT INTO t SELECT v, v FROM x": true,
	} {
		s, _, err := c.Prepare(sql)
		if err != nil {
			t.Fatal(sql, err)
		}
		if got := s.Mergeable(); got != want {
			t.Errorf("%s: mergeable %v", sql, got)
		}
		s.Finalize()
	}
}

// A statement that writes and was running when SQLite rolled back its
// transaction, because another statement of the connection failed, ends
// with ErrRolledBack: it must not end as if what it wrote were kept.
func TestRolledBackWhileRunning(t *testing.T) {
	c := open(t, filepath.Join(t.TempDir(), "r.db"))
	if err := c.Exec(`CREATE TABLE r(n); CREATE TABLE stop(x);
		CREATE TRIGGER stop_all BEFORE INSERT ON stop BEGIN SELECT RAISE(ROLLBACK, 'stopped'); END`); err != nil {
		t.Fatal(err)
	}
	for _, end := range []string{"step", "reset"} {
		s, _, err := c.Prepare(`INSERT INTO r VALUES (1), (2) RETURNING n`)
		if err != nil {
			t.Fatal(err)
		}
		if more, err := s.Step(); err != nil || !more {
			t.Fatalf("first row: %v %v", more, err)
		}
		if err := c.Exec(`INSERT INTO stop VALUES (1)`); err == nil {
			t.Fatal("the trigger did not stop the insert")
		}
		switch end {
		case "step":
			for {
				more, e := s.Step()
				if e != nil || !more {
					err = e
					break
				}
			}
		case "reset":
			err = s.Reset()
		}
		s.Finalize()
		if !errors.Is(err, ErrRolledBack) {
			t.Fatalf("%s: %v, want ErrRolledBack", end, err)
		}
		if v := mustOne(t, c, "SELECT count(*) FROM r"); v != "0" {
			t.Fatalf("r has %s rows", v)
		}
	}
	// A statement that runs alone is not touched.
	if err := c.Exec(`INSERT INTO r VALUES (3)`); err != nil {
		t.Fatal(err)
	}
}

// SQLite rolls back and runs a statement again inside one step when the
// schema changed under it. That rollback is not another statement's: the
// write commits and must end with no error.
func TestSchemaRetryIsNotRolledBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	a := open(t, path)
	b := open(t, path)
	if err := a.Exec(`CREATE TABLE t(x)`); err != nil {
		t.Fatal(err)
	}
	s, _, err := b.Prepare(`INSERT INTO t VALUES (1)`)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Finalize()
	if err := a.Exec(`CREATE TABLE other(x)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Step(); err != nil {
		t.Fatalf("insert after a change of the schema: %v", err)
	}
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	if v := mustOne(t, a, "SELECT count(*) FROM t"); v != "1" {
		t.Fatalf("t has %s rows", v)
	}
}
