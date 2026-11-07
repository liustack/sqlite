// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package sqlite_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"

	"github.com/liustack/sqlite"
)

func TestQueryWriteAfterPreparedPragma(t *testing.T) {
	c := oneConn(t)
	connExec(t, c, `PRAGMA foreign_keys=OFF; CREATE TABLE p(id INTEGER PRIMARY KEY); CREATE TABLE child(pid REFERENCES p(id))`)
	const q = `INSERT INTO child VALUES (?) RETURNING pid`
	if err := drainQuery(c, q, 11); err != nil {
		t.Fatal(err)
	}
	s, err := c.PrepareContext(context.Background(), `PRAGMA foreign_keys=ON`)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := drainQuery(c, q, 22); err == nil {
		t.Fatal("a row with no parent went in with foreign_keys on")
	}
}

func TestQueryWriteAfterDeferForeignKeys(t *testing.T) {
	c := oneConn(t)
	connExec(t, c, `PRAGMA foreign_keys=ON; CREATE TABLE t(id INTEGER PRIMARY KEY, p REFERENCES t(id) ON DELETE RESTRICT);
		INSERT INTO t VALUES (1, NULL), (2, 1); PRAGMA defer_foreign_keys=ON`)
	const q = `DELETE FROM t RETURNING id`
	if err := drainQuery(c, q); err != nil {
		t.Fatal(err)
	}
	connExec(t, c, `INSERT INTO t VALUES (1, NULL), (2, 1)`)
	if err := drainQuery(c, q); err == nil {
		t.Fatal("DELETE removed a parent ON DELETE RESTRICT keeps")
	}
}

// planTable is a Go table whose plan reads *state as SQLite prepares a query
// of it: a query kept would give the state of its prepare.
type planTable struct{ state *int }

func (planTable) Schema() string                        { return "CREATE TABLE x(value)" }
func (t planTable) BestIndex(i *sqlite.IndexInfo) error { i.IdxNum = *t.state; return nil }
func (planTable) Open() (sqlite.VCursor, error)         { return &planCursor{}, nil }
func (planTable) Disconnect() error                     { return nil }

type planCursor struct {
	value int
	done  bool
}

func (c *planCursor) Filter(n int, _ string, _ []driver.Value) error {
	c.value, c.done = n, false
	return nil
}
func (c *planCursor) Next() error                      { c.done = true; return nil }
func (c *planCursor) EOF() bool                        { return c.done }
func (c *planCursor) Column(int) (driver.Value, error) { return int64(c.value), nil }
func (*planCursor) RowID() (int64, error)              { return 1, nil }
func (*planCursor) Close() error                       { return nil }

// A kept query that SQLite prepared again after a change of the schema is
// sorted again: one that now reads a Go table is no longer kept.
func TestQueryCacheAfterTableBecomesGoTable(t *testing.T) {
	state := 11
	cn, err := sqlite.NewConnector(":memory:", sqlite.WithModule("plan", func([]string) (sqlite.VTable, error) { return planTable{&state}, nil }))
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(cn)
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	read := func(q string) int {
		t.Helper()
		var x int
		if err := db.QueryRow(q).Scan(&x); err != nil {
			t.Fatal(err)
		}
		return x
	}
	const q = `SELECT value FROM t`
	if _, err := db.Exec(`CREATE TABLE t(value); INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if x := read(q); x != 1 {
		t.Fatalf("got %d, want 1", x)
	}
	if _, err := db.Exec(`DROP TABLE t; CREATE VIRTUAL TABLE t USING plan`); err != nil {
		t.Fatal(err)
	}
	if x := read(q); x != 11 {
		t.Fatalf("got %d, want 11", x)
	}
	state = 22
	if x := read(q); x != 22 {
		t.Fatalf("got %d, want 22: the query ran as prepared before", x)
	}
}
