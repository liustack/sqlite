// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite

import (
	"context"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestQueryReusesStatement(t *testing.T) {
	c := memConn(t)
	mustExec(t, c, `CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT); INSERT INTO t VALUES (1, 'a'), (2, 'b')`)
	const q = `SELECT v FROM t WHERE id = ?`
	r := openRows(t, c, q, int64(1))
	first := r.s
	if got := drain(t, r); fmt.Sprint(got) != "[a]" {
		t.Fatalf("got %v", got)
	}
	r.Close()
	r = openRows(t, c, q, int64(2))
	if r.s != first {
		t.Error("the query was prepared again")
	}
	if got := drain(t, r); fmt.Sprint(got) != "[b]" {
		t.Fatalf("got %v", got)
	}
	r.Close()
}

func TestQuerySameSQLWhileOpen(t *testing.T) {
	c := memConn(t)
	mustExec(t, c, `CREATE TABLE t(id INTEGER PRIMARY KEY); INSERT INTO t VALUES (1), (2), (3)`)
	const q = `SELECT id FROM t WHERE id >= ? ORDER BY id`
	queryOne(t, c, q, int64(1)) // the statement is cached now
	outer := openRows(t, c, q, int64(1))
	dest := make([]driver.Value, 1)
	var got []string
	for outer.Next(dest) == nil {
		got = append(got, fmt.Sprintf("%v%v", dest[0], queryOne(t, c, q, dest[0])))
	}
	outer.Close()
	if want := "[1[1 2 3] 2[2 3] 3[3]]"; fmt.Sprint(got) != want {
		t.Fatalf("got %v, want %s", got, want)
	}
	if got := queryOne(t, c, q, int64(2)); fmt.Sprint(got) != "[2 3]" {
		t.Fatalf("got %v", got)
	}
}

func TestQueryCacheSkipsPragma(t *testing.T) {
	c := memConn(t)
	for _, ms := range []int64{100, 200} {
		mustExec(t, c, fmt.Sprintf("PRAGMA busy_timeout = %d", ms))
		if got := queryOne(t, c, `PRAGMA busy_timeout`); fmt.Sprint(got) != fmt.Sprint([]driver.Value{ms}) {
			t.Fatalf("busy_timeout is %v, want %d", got, ms)
		}
	}
	if len(c.queries) != 0 {
		t.Errorf("%d statements cached", len(c.queries))
	}
}

func TestQueryCacheFollowsSchema(t *testing.T) {
	c := memConn(t)
	mustExec(t, c, `CREATE TABLE t(a); INSERT INTO t VALUES (1)`)
	const q = `SELECT * FROM t`
	queryOne(t, c, q)
	mustExec(t, c, `ALTER TABLE t ADD COLUMN b`)
	r := openRows(t, c, q)
	if n := len(r.Columns()); n != 2 {
		t.Errorf("%d columns after ALTER TABLE, want 2", n)
	}
	r.Close()
	mustExec(t, c, `DROP TABLE t`)
	if _, err := c.QueryContext(context.Background(), q, nil); err == nil {
		t.Fatal("query of a dropped table ran")
	}
	mustExec(t, c, `CREATE TABLE t(x); INSERT INTO t VALUES ('again')`)
	if got := queryOne(t, c, q); fmt.Sprint(got) != "[again]" {
		t.Fatalf("got %v", got)
	}
}

func TestQueryCacheBounded(t *testing.T) {
	c := memConn(t)
	for i := range 3 * maxQueries {
		queryOne(t, c, fmt.Sprintf("SELECT %d", i))
	}
	if n := len(c.queries); n != maxQueries {
		t.Errorf("%d statements cached, want %d", n, maxQueries)
	}
	// The one used last stays.
	last := fmt.Sprintf("SELECT %d", 3*maxQueries-1)
	if c.queries[last] == nil {
		t.Errorf("%q was dropped", last)
	}
}

func TestQueryCacheClose(t *testing.T) {
	cn, err := (&Driver{}).Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	c := cn.(*conn)
	queryOne(t, c, `SELECT 1`)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestQueryCacheSkipsExplain(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	a, b := fileConn(t, name), fileConn(t, name)
	mustExec(t, a, `CREATE TABLE t(a, b)`)
	const q = `EXPLAIN QUERY PLAN SELECT b FROM t WHERE a = 1`
	if got := fmt.Sprint(planOf(t, a, q)); !strings.Contains(got, "SCAN") {
		t.Fatalf("plan %s, want a scan", got)
	}
	mustExec(t, b, `CREATE INDEX ta ON t(a, b)`)
	queryOne(t, a, `SELECT count(*) FROM t`) // a reads the new schema
	if got := fmt.Sprint(planOf(t, a, q)); !strings.Contains(got, "INDEX ta") {
		t.Fatalf("plan %s after CREATE INDEX, want ta", got)
	}
	if a.queries[q] != nil {
		t.Error("the EXPLAIN was cached")
	}
}

func planOf(t *testing.T, c *conn, q string) []driver.Value {
	t.Helper()
	r := openRows(t, c, q)
	defer r.Close()
	var out []driver.Value
	dest := make([]driver.Value, len(r.Columns()))
	for r.Next(dest) == nil {
		out = append(out, dest[3])
	}
	return out
}

func TestQueryCacheEmptiedByChange(t *testing.T) {
	c := memConn(t)
	mustExec(t, c, `CREATE TABLE t(a)`)
	queryOne(t, c, `SELECT a FROM t`)
	open := openRows(t, c, `SELECT 1 UNION ALL SELECT 2`) // kept, and busy
	queryOne(t, c, `SELECT count(*) FROM t`)
	if n := len(c.queries); n != 3 {
		t.Fatalf("%d statements cached, want 3", n)
	}
	s, err := c.PrepareContext(context.Background(), `PRAGMA reverse_unordered_selects = 1`)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if n := len(c.queries); n != 0 {
		t.Errorf("%d statements cached after a PRAGMA, want 0", n)
	}
	if got := drain(t, open); fmt.Sprint(got) != "[1 2]" {
		t.Fatalf("open rows gave %v", got)
	}
	open.Close()
	if n := len(c.queries); n != 0 {
		t.Errorf("%d statements cached after the open rows closed, want 0", n)
	}
}

func TestQueryCacheSkipsWrites(t *testing.T) {
	c := memConn(t)
	mustExec(t, c, `CREATE TABLE t(a)`)
	queryOne(t, c, `INSERT INTO t VALUES (1) RETURNING a`)
	if n := len(c.queries); n != 0 {
		t.Errorf("%d statements cached", n)
	}
}
