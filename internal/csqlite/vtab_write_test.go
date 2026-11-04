// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package csqlite

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
)

// kvState is the data of a kv table: what is committed, and the copy a
// transaction changes, with a copy at each savepoint.
type kvState struct {
	committed map[int64]string
	cur       map[int64]string // nil outside a transaction
	base      map[int64]string // cur at Begin
	saves     map[int]map[int64]string
	next      int64
	log       []string
	failBegin bool
}

func (s *kvState) rows() map[int64]string {
	if s.cur != nil {
		return s.cur
	}
	return s.committed
}

// kvTable is CREATE VIRTUAL TABLE t USING kv: a table of one column, v.
// The value 'bad' fails Insert.
type kvTable struct{ s *kvState }

func (kvTable) Schema() string             { return "CREATE TABLE x(v)" }
func (kvTable) BestIndex(*IndexInfo) error { return nil }
func (kvTable) Disconnect() error          { return nil }
func (t kvTable) Open() (VCursor, error)   { return &kvCursor{s: t.s}, nil }
func (t kvTable) Begin() error {
	if t.s.failBegin {
		return errors.New("no begin")
	}
	t.log("begin")
	t.s.cur, t.s.base, t.s.saves = maps.Clone(t.s.committed), maps.Clone(t.s.committed), nil
	return nil
}
func (t kvTable) Sync() error         { t.log("sync"); return nil }
func (t kvTable) Commit() error       { t.log("commit"); t.s.committed, t.s.cur = t.s.cur, nil; return nil }
func (t kvTable) Rollback() error     { t.log("rollback"); t.s.cur = nil; return nil }
func (t kvTable) log(s string)        { t.s.log = append(t.s.log, s) }
func (t kvTable) Release(n int) error { t.log("release"); clear(t.s.saves); return nil }
func (t kvTable) RollbackTo(n int) error {
	t.log("rollbackto")
	// A savepoint opened before the table joined the transaction has no
	// mark: the state then was the state at Begin.
	t.s.cur = maps.Clone(t.s.base)
	for m := n; m >= 0; m-- {
		if r, ok := t.s.saves[m]; ok {
			t.s.cur = maps.Clone(r)
			break
		}
	}
	return nil
}
func (t kvTable) Savepoint(n int) error {
	t.log("savepoint")
	if t.s.saves == nil {
		t.s.saves = map[int]map[int64]string{}
	}
	t.s.saves[n] = maps.Clone(t.s.cur)
	return nil
}

func (t kvTable) Insert(rowid driver.Value, cols []driver.Value) (int64, error) {
	if cols[0] == "bad" {
		return 0, errors.New("bad value")
	}
	id, ok := rowid.(int64)
	if !ok {
		t.s.next++
		id = t.s.next
	}
	t.s.rows()[id] = cols[0].(string)
	return id, nil
}

func (t kvTable) Update(oldRowid, newRowid driver.Value, cols []driver.Value) error {
	delete(t.s.rows(), oldRowid.(int64))
	t.s.rows()[newRowid.(int64)] = cols[0].(string)
	return nil
}

func (t kvTable) Delete(rowid driver.Value) error {
	delete(t.s.rows(), rowid.(int64))
	return nil
}

type kvCursor struct {
	s    *kvState
	keys []int64
	i    int
}

func (c *kvCursor) Filter(int, string, []driver.Value) error {
	c.keys = slices.Sorted(maps.Keys(c.s.rows()))
	c.i = 0
	return nil
}
func (c *kvCursor) Next() error                      { c.i++; return nil }
func (c *kvCursor) EOF() bool                        { return c.i >= len(c.keys) }
func (c *kvCursor) Column(int) (driver.Value, error) { return c.s.rows()[c.keys[c.i]], nil }
func (c *kvCursor) RowID() (int64, error)            { return c.keys[c.i], nil }
func (c *kvCursor) Close() error                     { return nil }

func TestModuleWritable(t *testing.T) {
	c := open(t, ":memory:")
	s := &kvState{committed: map[int64]string{}}
	if err := c.CreateModule("kv", func([]string) (VTable, error) { return kvTable{s}, nil }, ModWritable); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(`CREATE VIRTUAL TABLE t USING kv`); err != nil {
		t.Fatal(err)
	}
	all := func() string {
		return mustOne(t, c, `SELECT coalesce(group_concat(rowid || '=' || v, ' '), '') FROM t`)
	}
	steps := []struct{ sql, want string }{
		{`INSERT INTO t(v) VALUES ('a'), ('b')`, "1=a 2=b"},
		{`INSERT INTO t(rowid, v) VALUES (10, 'x')`, "1=a 2=b 10=x"},
		{`UPDATE t SET v = 'B' WHERE rowid = 2`, "1=a 2=B 10=x"},
		{`UPDATE t SET rowid = 20 WHERE v = 'x'`, "1=a 2=B 20=x"},
		{`DELETE FROM t WHERE v = 'a'`, "2=B 20=x"},
		// A statement that fails is undone, alone and in a transaction.
		{`INSERT INTO t(v) VALUES ('c'), ('bad')`, "2=B 20=x"},
		{`BEGIN; INSERT INTO t(v) VALUES ('d'); ROLLBACK`, "2=B 20=x"},
	}
	for _, st := range steps {
		err := c.Exec(st.sql)
		if err != nil && !strings.Contains(err.Error(), "bad value") {
			t.Fatalf("%s: %v", st.sql, err)
		}
		if got := all(); got != st.want {
			t.Errorf("after %s: %s, want %s", st.sql, got, st.want)
		}
	}
	if got := mustOne(t, c, `SELECT last_insert_rowid()`); got == "0" {
		t.Errorf("last_insert_rowid() = %s", got)
	}
	if err := c.Exec(`BEGIN; INSERT INTO t(v) VALUES ('e')`); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(`INSERT INTO t(v) VALUES ('f'), ('bad')`); err == nil {
		t.Fatal("a bad value was taken")
	}
	if err := c.Exec(`COMMIT`); err != nil {
		t.Fatal(err)
	}
	if got := all(); got != "2=B 5=e 20=x" { // the rowids of undone rows are not given again
		t.Errorf("after a statement failed in a transaction: %s", got)
	}
	for _, want := range []string{"begin", "sync", "commit", "rollback", "savepoint", "rollbackto"} {
		if !slices.Contains(s.log, want) {
			t.Errorf("%s never ran: %v", want, s.log)
		}
	}
}

func TestModuleWritableRefused(t *testing.T) {
	c := open(t, ":memory:")
	words := func([]string) (VTable, error) { return wordsTable{[]string{"a"}}, nil }
	if err := c.CreateModule("e", words, ModWritable|ModEponymousOnly); err == nil {
		t.Error("a writable eponymous module was taken")
	}
	// A writable module whose table is no VUpdater refuses writes.
	if err := c.CreateModule("w", words, ModWritable); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(`CREATE VIRTUAL TABLE w1 USING w`); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(`INSERT INTO w1 VALUES ('b')`); err == nil || !strings.Contains(err.Error(), "takes no writes") {
		t.Errorf("a write to a table that takes none: %v", err)
	}
	if got := mustOne(t, c, `SELECT group_concat(word) FROM w1`); got != "a" {
		t.Errorf("w1 = %s", got)
	}
}

// SQLite puts a table it creates in the transaction without calling xBegin:
// the table gets its Begin as it is made, a Savepoint inside a savepoint,
// and the end of the transaction.
func TestModuleWritableCreate(t *testing.T) {
	cases := []struct{ sql, rows, log string }{
		{`CREATE VIRTUAL TABLE t USING kv`, "", "begin sync commit"},
		{`BEGIN; CREATE VIRTUAL TABLE t USING kv; INSERT INTO t(v) VALUES ('a'); ROLLBACK`, "", "begin savepoint release rollback"},
		{`BEGIN; CREATE VIRTUAL TABLE t USING kv; INSERT INTO t(v) VALUES ('a'); COMMIT`, "1=a", "begin savepoint release sync commit"},
		{`BEGIN; CREATE VIRTUAL TABLE t USING kv; SAVEPOINT s; INSERT INTO t(v) VALUES ('a'); ROLLBACK TO s; COMMIT`, "", ""},
		// ROLLBACK TO undoes the writes of a table made inside the savepoint.
		{`BEGIN; SAVEPOINT a; CREATE VIRTUAL TABLE t USING kv; INSERT INTO t(v) VALUES ('x'); ROLLBACK TO a; COMMIT`, "", "begin savepoint release rollbackto sync commit"},
		{`SAVEPOINT a; CREATE VIRTUAL TABLE t USING kv; INSERT INTO t(v) VALUES ('x'); RELEASE a`, "1=x", ""},
	}
	for _, tc := range cases {
		c := open(t, ":memory:")
		s := &kvState{committed: map[int64]string{}}
		if err := c.CreateModule("kv", func([]string) (VTable, error) { return kvTable{s}, nil }, ModWritable); err != nil {
			t.Fatal(err)
		}
		if err := c.Exec(tc.sql); err != nil {
			t.Fatalf("%s: %v", tc.sql, err)
		}
		var rows []string
		for _, k := range slices.Sorted(maps.Keys(s.committed)) {
			rows = append(rows, fmt.Sprint(k, "=", s.committed[k]))
		}
		if got := strings.Join(rows, " "); got != tc.rows {
			t.Errorf("%s: committed %q, want %q", tc.sql, got, tc.rows)
		}
		if got := strings.Join(s.log, " "); tc.log != "" && got != tc.log {
			t.Errorf("%s: calls %q, want %q", tc.sql, got, tc.log)
		}
		if s.cur != nil {
			t.Errorf("%s: a transaction of the table is left open: %v", tc.sql, s.log)
		}
	}
	// A Begin that fails fails the CREATE, and leaves no table.
	c := open(t, ":memory:")
	s := &kvState{committed: map[int64]string{}, failBegin: true}
	if err := c.CreateModule("kv", func([]string) (VTable, error) { return kvTable{s}, nil }, ModWritable); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(`CREATE VIRTUAL TABLE t USING kv`); err == nil || !strings.Contains(err.Error(), "no begin") {
		t.Errorf("a create whose Begin fails: %v", err)
	}
	if got := mustOne(t, c, `SELECT count(*) FROM sqlite_schema`); got != "0" {
		t.Errorf("%s tables after a failed create", got)
	}
}
