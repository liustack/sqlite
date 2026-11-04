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
	"strings"
	"testing"
)

// seqTable is seq(stop): the rows 1 to stop. stop is a hidden column that
// must be given, so BestIndex refuses a plan without it.
type seqTable struct{ disconnected *int }

func (seqTable) Schema() string { return "CREATE TABLE x(value, stop HIDDEN)" }

func (seqTable) BestIndex(info *IndexInfo) error {
	for i, c := range info.Constraints {
		if c.Column == 1 && c.Op == IndexEQ && c.Usable {
			info.Usage[i] = IndexUsage{ArgvIndex: 1, Omit: true}
			info.IdxNum = 1
			info.IdxStr = "stop"
			info.EstimatedCost = 10
			return nil
		}
	}
	return ErrConstraint
}

func (t seqTable) Open() (VCursor, error) { return &seqCursor{}, nil }

func (t seqTable) Disconnect() error {
	if t.disconnected != nil {
		*t.disconnected++
	}
	return nil
}

type seqCursor struct{ i, stop int64 }

func (c *seqCursor) Filter(idxNum int, idxStr string, args []driver.Value) error {
	if idxNum != 1 || idxStr != "stop" || len(args) != 1 {
		return fmt.Errorf("Filter(%d, %q, %v)", idxNum, idxStr, args)
	}
	stop, ok := args[0].(int64)
	if !ok {
		return fmt.Errorf("stop is a %T", args[0])
	}
	if stop < 0 {
		panic("a negative stop")
	}
	c.i, c.stop = 1, stop
	return nil
}

func (c *seqCursor) Next() error { c.i++; return nil }
func (c *seqCursor) EOF() bool   { return c.i > c.stop }
func (c *seqCursor) Column(i int) (driver.Value, error) {
	switch i {
	case 0:
		if c.i == 13 {
			return nil, errors.New("unlucky")
		}
		return c.i, nil
	case 1:
		return c.stop, nil
	}
	return nil, fmt.Errorf("column %d", i)
}
func (c *seqCursor) RowID() (int64, error) { return c.i, nil }
func (c *seqCursor) Close() error          { return nil }

// wordsTable is CREATE VIRTUAL TABLE t USING words(a, b, ...): one row for
// each argument.
type wordsTable struct{ words []string }

func (wordsTable) Schema() string             { return "CREATE TABLE x(word TEXT)" }
func (wordsTable) BestIndex(*IndexInfo) error { return nil }
func (t wordsTable) Open() (VCursor, error)   { return &wordsCursor{words: t.words}, nil }
func (wordsTable) Disconnect() error          { return nil }

type wordsCursor struct {
	words []string
	i     int
}

func (c *wordsCursor) Filter(int, string, []driver.Value) error { c.i = 0; return nil }
func (c *wordsCursor) Next() error                              { c.i++; return nil }
func (c *wordsCursor) EOF() bool                                { return c.i >= len(c.words) }
func (c *wordsCursor) Column(int) (driver.Value, error)         { return c.words[c.i], nil }
func (c *wordsCursor) RowID() (int64, error)                    { return int64(c.i + 1), nil }
func (c *wordsCursor) Close() error                             { return nil }

func TestModuleEponymous(t *testing.T) {
	c, err := Open(":memory:", OpenReadWrite|OpenCreate|OpenURI, "")
	if err != nil {
		t.Fatal(err)
	}
	disconnected := 0
	seq := func(args []string) (VTable, error) { return seqTable{&disconnected}, nil }
	if err := c.CreateModule("seq", seq, ModEponymousOnly); err != nil {
		t.Fatal(err)
	}
	if got := mustOne(t, c, "SELECT group_concat(value) FROM seq(5)"); got != "1,2,3,4,5" {
		t.Errorf("seq(5) = %q", got)
	}
	if got := mustOne(t, c, "SELECT sum(value) FROM seq WHERE stop = 12"); got != "78" {
		t.Errorf("sum of seq(12) = %q", got)
	}
	// The planner finds no plan when BestIndex refuses every one.
	if _, err := one(t, c, "SELECT count(*) FROM seq"); err == nil {
		t.Error("seq without stop ran")
	}
	if err := c.Exec("CREATE VIRTUAL TABLE s USING seq"); err == nil {
		t.Error("CREATE VIRTUAL TABLE of an eponymous-only module worked")
	}
	if err := c.Exec("INSERT INTO seq(value) VALUES (1)"); err == nil || !strings.Contains(err.Error(), "may not be modified") {
		t.Errorf("INSERT into seq = %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if disconnected == 0 {
		t.Error("the table was never disconnected")
	}
}

func TestModuleErrors(t *testing.T) {
	c := open(t, ":memory:")
	seq := func(args []string) (VTable, error) { return seqTable{}, nil }
	if err := c.CreateModule("seq", seq, ModEponymousOnly); err != nil {
		t.Fatal(err)
	}
	// An error of Column fails the statement with its text.
	if _, err := one(t, c, "SELECT group_concat(value) FROM seq(20)"); err == nil || !strings.Contains(err.Error(), "unlucky") {
		t.Errorf("Column error = %v", err)
	}
	// A panic in Filter fails the statement, and the connection goes on.
	if _, err := one(t, c, "SELECT count(*) FROM seq(-1)"); err == nil || !strings.Contains(err.Error(), "a negative stop") {
		t.Errorf("Filter panic = %v", err)
	}
	// An error of Filter.
	if _, err := one(t, c, "SELECT count(*) FROM seq('x')"); err == nil || !strings.Contains(err.Error(), "stop is a string") {
		t.Errorf("Filter error = %v", err)
	}
	if got := mustOne(t, c, "SELECT count(*) FROM seq(3)"); got != "3" {
		t.Errorf("after the failures, count(seq(3)) = %q", got)
	}
}

func TestModuleCreate(t *testing.T) {
	c := open(t, ":memory:")
	var gotArgs []string
	words := func(args []string) (VTable, error) {
		gotArgs = args
		if len(args) == 0 {
			return nil, errors.New("words takes some words")
		}
		return wordsTable{args}, nil
	}
	if err := c.CreateModule("words", words, 0); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec("CREATE VIRTUAL TABLE w USING words(alpha, beta, gamma)"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(gotArgs, " ") != "alpha beta gamma" {
		t.Errorf("Connect args = %q", gotArgs)
	}
	if got := mustOne(t, c, "SELECT group_concat(word, '-') FROM (SELECT word FROM w ORDER BY word DESC)"); got != "gamma-beta-alpha" {
		t.Errorf("words = %q", got)
	}
	if got := mustOne(t, c, "SELECT rowid FROM w WHERE word = 'beta'"); got != "2" {
		t.Errorf("rowid of beta = %q", got)
	}
	if err := c.Exec("CREATE VIRTUAL TABLE e USING words"); err == nil || !strings.Contains(err.Error(), "words takes some words") {
		t.Errorf("Connect error = %v", err)
	}
	if err := c.Exec("DROP TABLE w"); err != nil {
		t.Fatal(err)
	}
}

func TestModuleBadSchema(t *testing.T) {
	c := open(t, ":memory:")
	bad := func([]string) (VTable, error) { return badSchema{}, nil }
	if err := c.CreateModule("bad", bad, 0); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec("CREATE VIRTUAL TABLE b USING bad"); err == nil {
		t.Error("a table with a bad schema was made")
	}
}

type badSchema struct{ wordsTable }

func (badSchema) Schema() string { return "CREATE TABLE x(" }

// bigTable gives strings longer than the shadow stack of a connection.
type bigTable struct {
	wordsTable
	schema, idxStr string
	panicSchema    bool
	disconnected   *int
}

func (t bigTable) Schema() string {
	if t.panicSchema {
		panic("no schema")
	}
	return t.schema
}

func (t bigTable) BestIndex(info *IndexInfo) error {
	info.IdxStr = t.idxStr
	return nil
}

func (t bigTable) Open() (VCursor, error) { return &idxStrCursor{}, nil }

func (t bigTable) Disconnect() error {
	if t.disconnected != nil {
		*t.disconnected++
	}
	return nil
}

// idxStrCursor gives one row, the length of idxStr.
type idxStrCursor struct {
	n    int
	done bool
}

func (c *idxStrCursor) Filter(_ int, idxStr string, _ []driver.Value) error {
	c.n, c.done = len(idxStr), false
	return nil
}
func (c *idxStrCursor) Next() error                      { c.done = true; return nil }
func (c *idxStrCursor) EOF() bool                        { return c.done }
func (c *idxStrCursor) Column(int) (driver.Value, error) { return int64(c.n), nil }
func (c *idxStrCursor) RowID() (int64, error)            { return 1, nil }
func (c *idxStrCursor) Close() error                     { return nil }

// Strings past the 32 MiB of the shadow stack go through malloc'd memory,
// and an error that long is cut.
func TestModuleLongStrings(t *testing.T) {
	c := open(t, ":memory:")
	const n = 40 << 20
	long := strings.Repeat("x", n)
	mod := func(tab bigTable) Module {
		return func(args []string) (VTable, error) {
			if len(args) > 0 && args[0] == "fail" {
				return nil, errors.New(long)
			}
			return tab, nil
		}
	}
	if err := c.CreateModule("big", mod(bigTable{schema: "CREATE TABLE x(a /*" + long + "*/)", idxStr: long}), 0); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec("CREATE VIRTUAL TABLE b USING big"); err != nil {
		t.Fatal(err)
	}
	if got := mustOne(t, c, "SELECT a FROM b"); got != fmt.Sprint(n) {
		t.Errorf("Filter got an idxStr of %s bytes, want %d", got, n)
	}
	err := c.Exec("CREATE VIRTUAL TABLE f USING big(fail)")
	if err == nil || len(err.Error()) > 1<<17 {
		t.Errorf("a long connect error: %d bytes", len(fmt.Sprint(err)))
	}
}

func TestModuleNUL(t *testing.T) {
	c := open(t, ":memory:")
	mod := func(tab bigTable) Module { return func([]string) (VTable, error) { return tab, nil } }
	if err := c.CreateModule("nulschema", mod(bigTable{schema: "CREATE TABLE x(a)\x00junk"}), 0); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateModule("nulidx", mod(bigTable{schema: "CREATE TABLE x(a)", idxStr: "a\x00b"}), 0); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec("CREATE VIRTUAL TABLE s USING nulschema"); err == nil || !strings.Contains(err.Error(), "NUL") {
		t.Errorf("a schema with a NUL: %v", err)
	}
	if err := c.Exec("CREATE VIRTUAL TABLE i USING nulidx"); err != nil {
		t.Fatal(err)
	}
	if _, err := one(t, c, "SELECT a FROM i"); err == nil || !strings.Contains(err.Error(), "NUL") {
		t.Errorf("an IdxStr with a NUL: %v", err)
	}
}

// A table whose Schema panics after it connected is disconnected.
func TestModuleSchemaPanic(t *testing.T) {
	c := open(t, ":memory:")
	disconnected := 0
	tab := bigTable{panicSchema: true, disconnected: &disconnected}
	if err := c.CreateModule("p", func([]string) (VTable, error) { return tab, nil }, 0); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec("CREATE VIRTUAL TABLE p1 USING p"); err == nil || !strings.Contains(err.Error(), "no schema") {
		t.Errorf("Schema panic: %v", err)
	}
	if disconnected != 1 {
		t.Errorf("disconnected %d times, want 1", disconnected)
	}
}

func TestModuleFlags(t *testing.T) {
	c := open(t, ":memory:")
	words := func([]string) (VTable, error) { return wordsTable{[]string{"a"}}, nil }
	// SQLite keeps one risk level per table: Innocuous would undo DirectOnly.
	if err := c.CreateModule("both", words, ModDirectOnly|ModInnocuous); err == nil {
		t.Error("DirectOnly and Innocuous together were taken")
	}
	// A module for CREATE VIRTUAL TABLE is not a table by its own name.
	if err := c.CreateModule("words", words, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := one(t, c, "SELECT count(*) FROM words"); err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Errorf("the module as a table: %v", err)
	}
}
