// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// upto is upto(stop): the rows 1 to stop, each with its value, its square
// as an int, and a time. stop is a hidden column that must be given.
type upto struct{ connects *atomic.Int64 }

func (upto) Schema() string { return "CREATE TABLE x(value, square, at, stop HIDDEN)" }

func (upto) BestIndex(info *IndexInfo) error {
	for i, c := range info.Constraints {
		if c.Column == 3 && c.Op == IndexEQ && c.Usable {
			info.Usage[i] = IndexUsage{ArgvIndex: 1, Omit: true}
			info.IdxStr = "stop"
			info.EstimatedCost = 10
			return nil
		}
	}
	return ErrUnusablePlan
}

func (upto) Open() (VCursor, error) { return &uptoCursor{}, nil }
func (upto) Disconnect() error      { return nil }

type uptoCursor struct{ i, stop int64 }

func (c *uptoCursor) Filter(_ int, idxStr string, args []driver.Value) error {
	if idxStr != "stop" || len(args) != 1 {
		return fmt.Errorf("Filter(%q, %v)", idxStr, args)
	}
	stop, ok := args[0].(int64)
	if !ok {
		return fmt.Errorf("stop is a %T", args[0])
	}
	c.i, c.stop = 1, stop
	return nil
}

func (c *uptoCursor) Next() error { c.i++; return nil }
func (c *uptoCursor) EOF() bool   { return c.i > c.stop }

func (c *uptoCursor) Column(i int) (driver.Value, error) {
	switch i {
	case 0:
		return c.i, nil
	case 1:
		return int(c.i * c.i), nil // an int, as a bound argument may be
	case 2:
		return time.Date(2026, 1, int(c.i), 0, 0, 0, 0, time.UTC), nil
	case 3:
		return c.stop, nil
	}
	return nil, fmt.Errorf("column %d", i)
}

func (c *uptoCursor) RowID() (int64, error) { return c.i, nil }
func (c *uptoCursor) Close() error          { return nil }

func withUpto(connects *atomic.Int64, flags ...ModuleFlag) Option {
	return WithModule("upto", func(args []string) (VTable, error) {
		if connects != nil {
			connects.Add(1)
		}
		return upto{connects}, nil
	}, append([]ModuleFlag{EponymousOnly}, flags...)...)
}

func TestWithModule(t *testing.T) {
	db := fOpen(t, ":memory:", withUpto(nil))
	if got := fString(t, db, `SELECT group_concat(value || ':' || square || ':' || at, ' ') FROM upto(3)`); got != "1:1:2026-01-01 00:00:00+00:00 2:4:2026-01-02 00:00:00+00:00 3:9:2026-01-03 00:00:00+00:00" {
		t.Errorf("upto(3) = %q", got)
	}
	if got := fString(t, db, `SELECT sum(value) FROM upto WHERE stop = ?`, 10); got != "55" {
		t.Errorf("sum of upto(10) = %q", got)
	}
	// The hidden column is not in SELECT *.
	rows, err := db.Query(`SELECT * FROM upto(1)`)
	if err != nil {
		t.Fatal(err)
	}
	cols, _ := rows.Columns()
	rows.Close()
	if strings.Join(cols, ",") != "value,square,at" {
		t.Errorf("SELECT * columns = %v", cols)
	}
	// Without stop, BestIndex has no plan.
	if _, err := db.Exec(`SELECT count(*) FROM upto`); err == nil {
		t.Error("upto without stop ran")
	}
	if _, err := db.Exec(`SELECT count(*) FROM upto('x')`); err == nil || !strings.Contains(err.Error(), "stop is a string") {
		t.Errorf("Filter error = %v", err)
	}
	if _, err := db.Exec(`INSERT INTO upto(value) VALUES (1)`); err == nil {
		t.Error("INSERT into a module worked")
	}
}

// words is CREATE VIRTUAL TABLE t USING words(a, b, ...): one row for each
// argument.
type words []string

func (words) Schema() string                  { return "CREATE TABLE x(word TEXT)" }
func (words) BestIndex(info *IndexInfo) error { return nil }
func (w words) Open() (VCursor, error)        { return &wordsCursor{w: w}, nil }
func (words) Disconnect() error               { return nil }

type wordsCursor struct {
	w words
	i int
}

func (c *wordsCursor) Filter(int, string, []driver.Value) error { c.i = 0; return nil }
func (c *wordsCursor) Next() error                              { c.i++; return nil }
func (c *wordsCursor) EOF() bool                                { return c.i >= len(c.w) }
func (c *wordsCursor) Column(int) (driver.Value, error)         { return c.w[c.i], nil }
func (c *wordsCursor) RowID() (int64, error)                    { return int64(c.i + 1), nil }
func (c *wordsCursor) Close() error                             { return nil }

func withWords(connects *atomic.Int64) Option {
	return WithModule("words", func(args []string) (VTable, error) {
		connects.Add(1)
		return words(args), nil
	})
}

// A table made by CREATE VIRTUAL TABLE is in the file, and each connection
// connects it when it first uses it.
func TestModuleCreateVirtualTable(t *testing.T) {
	p := mPath(t)
	var connects atomic.Int64
	db := fOpen(t, "file:"+p, withWords(&connects))
	db.SetMaxOpenConns(2)
	mExec(t, db, `CREATE VIRTUAL TABLE w USING words(alpha, beta, gamma)`)
	c1, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	c2, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	before := connects.Load()
	var a, b string
	if err := c1.QueryRowContext(t.Context(), `SELECT group_concat(word) FROM w`).Scan(&a); err != nil {
		t.Fatal(err)
	}
	if err := c2.QueryRowContext(t.Context(), `SELECT group_concat(word) FROM w`).Scan(&b); err != nil {
		t.Fatal(err)
	}
	c1.Close()
	c2.Close()
	if a != "alpha,beta,gamma" || b != a {
		t.Errorf("w = %q and %q", a, b)
	}
	if n := connects.Load() - before; n < 1 {
		t.Errorf("%d connects for two connections", n)
	}
	// A connector without the module cannot use the table.
	plain := mOpen(t, "file:"+p)
	if _, err := plain.Exec(`SELECT * FROM w`); err == nil || !strings.Contains(err.Error(), "no such module") {
		t.Errorf("w without the module: %v", err)
	}
	mExec(t, db, `DROP TABLE w`)
}

// A DirectOnly module may not be used from a view.
func TestModuleDirectOnly(t *testing.T) {
	db := fOpen(t, ":memory:", withUpto(nil, ModuleDirectOnly))
	db.SetMaxOpenConns(1)
	if got := fString(t, db, `SELECT count(*) FROM upto(4)`); got != "4" {
		t.Errorf("count(upto(4)) = %q", got)
	}
	mExec(t, db, `CREATE VIEW v AS SELECT value FROM upto(4)`)
	if _, err := db.Exec(`SELECT * FROM v`); err == nil || !strings.Contains(err.Error(), "unsafe use") {
		t.Errorf("a DirectOnly module from a view: %v", err)
	}
	// Innocuous lets a view use it with trusted_schema off, DirectOnly not.
	in := fOpen(t, ":memory:", withUpto(nil, ModuleInnocuous))
	in.SetMaxOpenConns(1)
	mExec(t, in, `PRAGMA trusted_schema = OFF`)
	mExec(t, in, `CREATE VIEW v AS SELECT value FROM upto(4)`)
	if got := fString(t, in, `SELECT count(*) FROM v`); got != "4" {
		t.Errorf("an Innocuous module from a view = %q", got)
	}
}

func TestWithModuleRefused(t *testing.T) {
	connect := func([]string) (VTable, error) { return words(nil), nil }
	for _, opt := range []Option{
		WithModule("", connect),
		WithModule("a\x00b", connect),
		WithModule(strings.Repeat("m", maxFuncName+1), connect),
		WithModule("m", nil),
		WithModule("m", connect, ModuleFlag(8)),
		WithModule("m", connect, ModuleDirectOnly, ModuleInnocuous),
	} {
		if _, err := NewConnector(":memory:", opt); err == nil {
			t.Errorf("a bad module was taken")
		}
	}
}

// A connect that fails or gives nil fails the statement, and a table that
// fails to open fails the scan.
func TestModuleErrors(t *testing.T) {
	db := fOpen(t, ":memory:",
		WithModule("broken", func([]string) (VTable, error) { return nil, errors.New("cannot connect") }),
		WithModule("nothing", func([]string) (VTable, error) { return nil, nil }),
		WithModule("closed", func([]string) (VTable, error) { return closed{}, nil }),
	)
	db.SetMaxOpenConns(1)
	mExec(t, db, `CREATE VIRTUAL TABLE c USING closed`)
	for q, want := range map[string]string{
		`CREATE VIRTUAL TABLE b USING broken`:  "cannot connect",
		`CREATE VIRTUAL TABLE n USING nothing`: "",
		`SELECT * FROM c`:                      "cannot open",
		`SELECT * FROM closed`:                 "no such table",
	} {
		_, err := db.Exec(q)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", q, err)
		}
	}
}

type closed struct{ words }

func (closed) Open() (VCursor, error) { return nil, errors.New("cannot open") }

// An INSERT that reads a module merges with others: every connection of the
// connector has the module.
func TestMergeWithModule(t *testing.T) {
	p := mPath(t)
	db := fOpen(t, "file:"+p, withUpto(nil))
	db.SetMaxOpenConns(8)
	mExec(t, db, `CREATE TABLE h(x); CREATE TABLE t(n INTEGER)`)
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
	errs := make(chan error, n)
	tx, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	mExec(t, tx, `INSERT INTO h VALUES (1)`)
	for i := range n {
		go func() {
			r, err := db.Exec(`INSERT INTO t SELECT value FROM upto(?)`, i+1)
			if err == nil {
				var k int64
				if k, err = r.RowsAffected(); err == nil && k != int64(i+1) {
					err = fmt.Errorf("upto(%d) inserted %d rows", i+1, k)
				}
			}
			errs <- err
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
	for range n {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
	if c, m := wl.stats.commits.Load()-commits0, wl.stats.merged.Load()-merged0; c != 1 || m != n {
		t.Errorf("%d batch commits of %d statements, want 1 of %d", c, m, n)
	}
	if got := mCount(t, db, `SELECT count(*) FROM t`); got != n*(n+1)/2 {
		t.Errorf("%d rows, want %d", got, n*(n+1)/2)
	}
}
