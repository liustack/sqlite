// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package sqlite

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func fOpen(t *testing.T, dsn string, opts ...Option) *sql.DB {
	t.Helper()
	c, err := NewConnector(dsn, opts...)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(c)
	t.Cleanup(func() { db.Close() })
	return db
}

func fString(t *testing.T, db *sql.DB, q string, args ...any) string {
	t.Helper()
	var s string
	if err := db.QueryRow(q, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return s
}

func add1(args []driver.Value) (driver.Value, error) { return args[0].(int64) + 1, nil }

func TestWithFunction(t *testing.T) {
	when := time.Date(2026, 11, 4, 12, 30, 0, 0, time.UTC)
	db := fOpen(t, ":memory:",
		WithFunction("add1", 1, add1, Deterministic),
		WithFunction("stamp", 0, func([]driver.Value) (driver.Value, error) { return when, nil }),
		WithFunction("small", 0, func([]driver.Value) (driver.Value, error) { return int32(7), nil }),
		WithFunction("flag", 0, func([]driver.Value) (driver.Value, error) { return true, nil }),
		WithFunction("fail", 0, func([]driver.Value) (driver.Value, error) { return nil, errors.New("no luck") }),
		WithFunction("boom", 0, func([]driver.Value) (driver.Value, error) { panic("kaboom") }),
		WithFunction("odd", 0, func([]driver.Value) (driver.Value, error) { return struct{}{}, nil }),
	)
	db.SetMaxOpenConns(1)
	for q, want := range map[string]string{
		"SELECT add1(41)":          "42",
		"SELECT add1(add1(1))":     "3",
		"SELECT stamp()":           when.Format(timeFormat),
		"SELECT datetime(stamp())": "2026-11-04 12:30:00",
		"SELECT small()":           "7",
		"SELECT flag()":            "1",
	} {
		if got := fString(t, db, q); got != want {
			t.Errorf("%s = %q, want %q", q, got, want)
		}
	}
	for q, want := range map[string]string{
		"SELECT fail()": "no luck",
		"SELECT boom()": "kaboom",
		"SELECT odd()":  "struct {}",
	} {
		var s any
		err := db.QueryRow(q).Scan(&s)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %v, want one with %q", q, err, want)
		}
	}
	// The one connection is still good after a panic.
	if got := fString(t, db, "SELECT add1(1)"); got != "2" {
		t.Fatal(got)
	}
}

func TestWithFunctionRefused(t *testing.T) {
	for name, opt := range map[string]Option{
		"no name":       WithFunction("", 0, add1),
		"long name":     WithFunction(strings.Repeat("f", 256), 0, add1),
		"nil function":  WithFunction("f", 0, nil),
		"nArg -2":       WithFunction("f", -2, add1),
		"nArg too many": WithFunction("f", 1001, add1),
		"unknown flag":  WithFunction("f", 0, add1, FuncFlag(1)),
	} {
		if _, err := NewConnector(":memory:", opt); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := NewConnector(":memory:", WithFunction("f", 1000, add1), WithFunction("g", -1, add1)); err != nil {
		t.Error(err)
	}
}

// resetRegistry undoes the Register calls of a test.
func resetRegistry(t *testing.T) {
	registry.Lock()
	opts, gen := registry.opts, registry.gen
	registry.Unlock()
	t.Cleanup(func() {
		registry.Lock()
		registry.opts, registry.gen = opts, gen
		registry.Unlock()
	})
}

func TestRegister(t *testing.T) {
	resetRegistry(t)
	before, err := NewConnector(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	Register(WithFunction("twice", 1, func(a []driver.Value) (driver.Value, error) { return a[0].(int64) * 2, nil }))
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := fString(t, db, "SELECT twice(21)"); got != "42" {
		t.Fatalf("twice(21) = %s", got)
	}
	// A connector made before Register does not have it.
	old := sql.OpenDB(before)
	defer old.Close()
	if err := old.QueryRow("SELECT twice(1)").Scan(new(int)); err == nil || !strings.Contains(err.Error(), "no such function") {
		t.Fatalf("a connector made before Register: %v", err)
	}
	// A bad option given to Register fails the connectors made after it.
	Register(WithFunction("", 0, add1))
	if _, err := sql.Open("sqlite", ":memory:"); err == nil {
		t.Fatal("sql.Open after a bad Register succeeded")
	}
}

// Group commit merges statements only between connections of one key, so
// connectors whose connections may have different functions never share
// one.
func TestFunctionsSeparateKeys(t *testing.T) {
	resetRegistry(t)
	dsn := "file:" + mPath(t)
	key := func(opts ...Option) string {
		t.Helper()
		c, err := NewConnector(dsn, opts...)
		if err != nil {
			t.Fatal(err)
		}
		return c.key
	}
	plain1, plain2 := key(), key()
	own1, own2 := key(WithFunction("f", 1, add1)), key(WithFunction("f", 1, add1))
	Register(WithFunction("g", 1, add1))
	reg1, reg2 := key(), key()
	regOwn := key(WithFunction("f", 1, add1))
	Register(WithFunction("h", 1, add1))
	reg3 := key()
	if plain1 != plain2 || reg1 != reg2 {
		t.Error("connectors of the same functions have different keys")
	}
	all := []string{plain1, own1, own2, reg1, regOwn, reg3}
	seen := map[string]int{}
	for i, k := range all {
		if j, ok := seen[k]; ok {
			t.Errorf("connectors %d and %d share a key %q", j, i, k)
		}
		seen[k] = i
	}
}

// A statement that calls a Go function merges, and each caller gets the
// result of its own arguments, or its own error.
func TestMergeWithFunction(t *testing.T) {
	p := mPath(t)
	tag := WithFunction("tag", 1, func(a []driver.Value) (driver.Value, error) {
		n := a[0].(int64)
		if n == 2 {
			return nil, errors.New("not two")
		}
		if n == 3 {
			panic("not three")
		}
		return fmt.Sprintf("row-%d", n), nil
	}, Deterministic)
	db := fOpen(t, "file:"+p, tag)
	db.SetMaxOpenConns(8)
	mExec(t, db, `CREATE TABLE h(x); CREATE TABLE t(id INTEGER PRIMARY KEY, s TEXT)`)
	// The lock is held by another connector, whose connections merge
	// nothing of db's.
	other := mOpen(t, "file:"+p)
	sc, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var wl *writeLock
	sc.Raw(func(dc any) error { wl = dc.(*conn).wl; return nil })
	sc.Close()
	commits0, merged0 := wl.stats.commits.Load(), wl.stats.merged.Load()

	const n = 6
	type result struct {
		i   int
		id  int64
		err error
	}
	results := make(chan result, n)
	// The holder keeps the lock until every insert waits in the queue, so
	// they run as one batch.
	tx, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	mExec(t, tx, `INSERT INTO h VALUES (1)`)
	for i := range n {
		go func() {
			r, err := db.Exec(`INSERT INTO t(s) VALUES (tag(?))`, i)
			var id int64
			if err == nil {
				id, err = r.LastInsertId()
			}
			results <- result{i, id, err}
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for waiting() < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d inserts wait", waiting(), n)
		}
		time.Sleep(time.Millisecond)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	ids := map[int]int64{}
	for range n {
		r := <-results
		switch r.i {
		case 2:
			if r.err == nil || !strings.Contains(r.err.Error(), "not two") {
				t.Errorf("tag(2): %v", r.err)
			}
		case 3:
			if r.err == nil || !strings.Contains(r.err.Error(), "not three") {
				t.Errorf("tag(3): %v", r.err)
			}
		default:
			if r.err != nil {
				t.Errorf("tag(%d): %v", r.i, r.err)
			}
			ids[r.i] = r.id
		}
	}
	// The two that failed are undone by their SAVEPOINTs, and the others
	// commit together.
	if c, m := wl.stats.commits.Load()-commits0, wl.stats.merged.Load()-merged0; c != 1 || m != n-2 {
		t.Errorf("%d batch commits of %d statements, want 1 of %d", c, m, n-2)
	}
	for i, id := range ids {
		if got := fString(t, db, `SELECT s FROM t WHERE id = ?`, id); got != fmt.Sprintf("row-%d", i) {
			t.Errorf("row %d of caller %d: %q", id, i, got)
		}
	}
	if c := mCount(t, db, `SELECT count(*) FROM t`); c != n-2 {
		t.Errorf("%d rows, want %d", c, n-2)
	}
}

// median collects numbers and gives their median.
type median struct{ xs []float64 }

func (m *median) Step(args []driver.Value) error {
	switch v := args[0].(type) {
	case int64:
		m.xs = append(m.xs, float64(v))
	case float64:
		m.xs = append(m.xs, v)
	case nil:
	default:
		return fmt.Errorf("median of a %T", v)
	}
	return nil
}

func (m *median) Final() (driver.Value, error) {
	if len(m.xs) == 0 {
		return nil, nil
	}
	slices.Sort(m.xs)
	n := len(m.xs)
	if n%2 == 1 {
		return m.xs[n/2], nil
	}
	return (m.xs[n/2-1] + m.xs[n/2]) / 2, nil
}

func TestWithAggregate(t *testing.T) {
	db := fOpen(t, ":memory:", WithAggregate("median", 1, func() Aggregate { return &median{} }, Deterministic))
	db.SetMaxOpenConns(1)
	mExec(t, db, `CREATE TABLE t(g, x); INSERT INTO t VALUES (1, 5), (1, 1), (1, 3), (2, 10), (2, 20), (3, NULL)`)
	got := fString(t, db, `SELECT group_concat(g || '=' || coalesce(m, 'null'), ' ') FROM (SELECT g, median(x) AS m FROM t GROUP BY g ORDER BY g)`)
	if got != "1=3.0 2=15.0 3=null" {
		t.Fatalf("medians: %s", got)
	}
	var s any
	if err := db.QueryRow(`SELECT median('x')`).Scan(&s); err == nil || !strings.Contains(err.Error(), "median of a string") {
		t.Fatalf("median('x'): %v", err)
	}
	// An aggregate in a write merges like any statement of its connector.
	mExec(t, db, `CREATE TABLE m(g, v); INSERT INTO m SELECT g, median(x) FROM t GROUP BY g`)
	if got := fString(t, db, `SELECT group_concat(v, ',') FROM m`); got != "3.0,15.0" {
		t.Fatalf("inserted medians: %s", got)
	}
}

func TestWithCollation(t *testing.T) {
	// Case and accents aside, then the bytes.
	fold := func(a, b string) int {
		if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	}
	db := fOpen(t, ":memory:", WithCollation("fold", fold))
	db.SetMaxOpenConns(1)
	mExec(t, db, `CREATE TABLE t(x TEXT COLLATE fold); CREATE INDEX tx ON t(x);
		INSERT INTO t VALUES ('b'), ('B'), ('a'), ('C')`)
	if got := fString(t, db, `SELECT group_concat(x, ',') FROM (SELECT x FROM t ORDER BY x)`); got != "a,B,b,C" {
		t.Fatalf("order: %s", got)
	}
	if got := fString(t, db, `SELECT count(*) FROM t WHERE x > 'b'`); got != "1" {
		t.Fatalf("x > 'b': %s rows", got)
	}
}

func TestOptionsRefused(t *testing.T) {
	newMedian := func() Aggregate { return &median{} }
	for name, opt := range map[string]Option{
		"aggregate without a name": WithAggregate("", 1, newMedian),
		"nil aggregate":            WithAggregate("m", 1, nil),
		"aggregate nArg -2":        WithAggregate("m", -2, newMedian),
		"collation without a name": WithCollation("", strings.Compare),
		"nil collation":            WithCollation("c", nil),
		"zero Option":              {},
	} {
		if _, err := NewConnector(":memory:", opt); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// intValuer is a driver.Valuer whose Value is a plain int, which a bound
// argument accepts.
type intValuer int

func (v intValuer) Value() (driver.Value, error) { return int(v), nil }

// panicError is an error whose Error method panics.
type panicError struct{}

func (panicError) Error() string { panic("no message") }

func TestFunctionResultsAndErrors(t *testing.T) {
	db := fOpen(t, ":memory:",
		WithFunction("valuer", 0, func([]driver.Value) (driver.Value, error) { return intValuer(7), nil }),
		WithFunction("badErr", 0, func([]driver.Value) (driver.Value, error) { return nil, panicError{} }))
	db.SetMaxOpenConns(1)
	if got := fString(t, db, `SELECT valuer()`); got != "7" {
		t.Fatalf("valuer() = %s", got)
	}
	var v any
	if err := db.QueryRow(`SELECT badErr()`).Scan(&v); err == nil || !strings.Contains(err.Error(), "no message") {
		t.Fatalf("badErr(): %v", err)
	}
	if got := fString(t, db, `SELECT valuer()`); got != "7" {
		t.Fatalf("after badErr(): %s", got)
	}
	for name, opt := range map[string]Option{
		"function":  WithFunction("f\x00g", 0, add1),
		"collation": WithCollation("c\x00d", strings.Compare),
	} {
		if _, err := NewConnector(":memory:", opt); err == nil {
			t.Errorf("a NUL in the name of a %s accepted", name)
		}
	}
}

// A collation that panics in one statement of a merged batch fails that
// statement, and the others commit.
func TestMergeWithCollationPanic(t *testing.T) {
	p := mPath(t)
	pc := WithCollation("pc", func(a, b string) int {
		if a == "zz" || b == "zz" {
			panic("cannot compare zz")
		}
		return strings.Compare(a, b)
	})
	db := fOpen(t, "file:"+p, pc)
	db.SetMaxOpenConns(8)
	mExec(t, db, `CREATE TABLE h(x); CREATE TABLE t(x TEXT); CREATE INDEX tx ON t(x COLLATE pc); INSERT INTO t VALUES ('m')`)
	other := mOpen(t, "file:"+p)
	sc, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var wl *writeLock
	sc.Raw(func(dc any) error { wl = dc.(*conn).wl; return nil })
	sc.Close()
	merged0 := wl.stats.merged.Load()

	words := []string{"a", "b", "zz", "c", "d"}
	errs := make(chan error, len(words))
	// The holder keeps the lock until every insert waits in the queue, so
	// they run as one batch.
	tx, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	mExec(t, tx, `INSERT INTO h VALUES (1)`)
	for _, w := range words {
		go func() {
			_, err := db.Exec(`INSERT INTO t VALUES (?)`, w)
			if w == "zz" {
				if err == nil || !strings.Contains(err.Error(), "cannot compare zz") {
					err = fmt.Errorf("zz: %v, want the panic", err)
				} else {
					err = nil
				}
			}
			errs <- err
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for waiting() < len(words) {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d inserts wait", waiting(), len(words))
		}
		time.Sleep(time.Millisecond)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for range words {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
	if m := wl.stats.merged.Load() - merged0; m < 2 {
		t.Errorf("%d statements merged", m)
	}
	if got := fString(t, db, `SELECT group_concat(x, ',') FROM (SELECT x FROM t ORDER BY x)`); got != "a,b,c,d,m" {
		t.Fatalf("rows: %s", got)
	}
	if got := fString(t, db, `PRAGMA integrity_check`); got != "ok" {
		t.Fatalf("integrity_check: %s", got)
	}
}
