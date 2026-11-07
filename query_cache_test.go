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
	"math"
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

// A read of the temp database or of an attached one is not kept: what such a
// name means belongs to the connection.
func TestQueryCacheSkipsOtherSchemas(t *testing.T) {
	c := memConn(t)
	mustExec(t, c, `ATTACH ':memory:' AS aux; CREATE TABLE aux.u(x); CREATE TEMP TABLE tt(x); CREATE TABLE m(x)`)
	for _, q := range []string{`SELECT x FROM aux.u`, `SELECT x FROM tt`, `SELECT x FROM temp.tt`} {
		queryOne(t, c, q)
		if c.queries[q] != nil {
			t.Errorf("%s was kept", q)
		}
	}
	queryOne(t, c, `SELECT x FROM m`)
	if len(c.queries) != 1 {
		t.Errorf("%d statements kept, want only the read of main", len(c.queries))
	}
}

// A statement that fails is not kept, whether it fails at its first step or
// after rows, and the next query of the same SQL prepares a new one.
func TestQueryCacheDropsFailed(t *testing.T) {
	c := memConn(t)
	const first = `SELECT abs(?)`
	if _, err := c.QueryContext(context.Background(), first, []driver.NamedValue{{Ordinal: 1, Value: int64(math.MinInt64)}}); err == nil {
		t.Fatal("abs of the least int64 gave no error")
	}
	if c.queries[first] != nil {
		t.Error("a statement that failed at its first step was kept")
	}
	if got := queryOne(t, c, first, int64(-2)); fmt.Sprint(got) != "[2]" {
		t.Fatalf("got %v", got)
	}
	const later = `SELECT abs(x) FROM (SELECT 1 AS x UNION ALL SELECT ?)`
	r := openRows(t, c, later, int64(math.MinInt64))
	dest := make([]driver.Value, 1)
	if err := r.Next(dest); err != nil {
		t.Fatal(err)
	}
	if err := r.Next(dest); err == nil {
		t.Fatal("abs of the least int64 gave no error")
	}
	r.Close()
	if c.queries[later] != nil {
		t.Error("a statement that failed after a row was kept")
	}
}

// When every statement kept has open rows, a new one is not kept and the
// open ones stay. Once rows close, their statement is the one let go.
func TestQueryCacheFullOfOpenRows(t *testing.T) {
	c := memConn(t)
	open := make([]*rows, maxQueries)
	for i := range open {
		open[i] = openRows(t, c, fmt.Sprintf("SELECT %d UNION ALL SELECT 0", i))
	}
	if got := queryOne(t, c, `SELECT 'new'`); fmt.Sprint(got) != "[new]" {
		t.Fatalf("got %v", got)
	}
	if c.queries[`SELECT 'new'`] != nil || len(c.queries) != maxQueries {
		t.Fatalf("a statement was kept with every one in use: %d kept", len(c.queries))
	}
	open[5].Close()
	queryOne(t, c, `SELECT 'new'`)
	if c.queries[`SELECT 'new'`] == nil || c.queries[`SELECT 5 UNION ALL SELECT 0`] != nil {
		t.Fatal("the statement with closed rows was not the one let go")
	}
	for i, r := range open {
		if i == 5 {
			continue
		}
		if got := drain(t, r); fmt.Sprint(got) != fmt.Sprintf("[%d 0]", i) {
			t.Fatalf("rows %d gave %v", i, got)
		}
		r.Close()
	}
}

// SQLite asks the authorizer about no column of a table named only in USING,
// so a read of an attached database there is kept. That is safe: ATTACH and
// DETACH empty the cache, so a kept statement never reads a database that
// another one replaced.
func TestQueryCacheJoinOfAttached(t *testing.T) {
	c := memConn(t)
	mustExec(t, c, `CREATE TABLE m(x); INSERT INTO m VALUES (1), (2); ATTACH ':memory:' AS aux; CREATE TABLE aux.u(x); INSERT INTO aux.u VALUES (1)`)
	const q = `SELECT x FROM m JOIN aux.u USING (x)`
	if got := queryOne(t, c, q); fmt.Sprint(got) != "[1]" {
		t.Fatalf("got %v", got)
	}
	mustExec(t, c, `DETACH aux; ATTACH ':memory:' AS aux; CREATE TABLE aux.u(x); INSERT INTO aux.u VALUES (2)`)
	if got := queryOne(t, c, q); fmt.Sprint(got) != "[2]" {
		t.Fatalf("got %v after aux was attached again", got)
	}
}
