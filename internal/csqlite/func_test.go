// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package csqlite

import (
	"bytes"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// one runs a query of one row and one column and gives its text, or the error
// of the step.
func one(t *testing.T, c *Conn, sql string) (string, error) {
	t.Helper()
	s, _, err := c.Prepare(sql)
	if err != nil {
		return "", err
	}
	defer s.Finalize()
	row, err := s.Step()
	if err != nil {
		return "", err
	}
	if !row {
		t.Fatalf("%s: no row", sql)
	}
	return s.ColumnText(0), nil
}

func mustOne(t *testing.T, c *Conn, sql string) string {
	t.Helper()
	v, err := one(t, c, sql)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func add1(args []driver.Value) (driver.Value, error) { return args[0].(int64) + 1, nil }

func TestFunctionScalar(t *testing.T) {
	c := open(t, ":memory:")
	if err := c.CreateFunction("add1", 1, FuncDeterministic, add1); err != nil {
		t.Fatal(err)
	}
	if v := mustOne(t, c, "SELECT add1(41)"); v != "42" {
		t.Fatalf("add1(41) = %s", v)
	}
	// Calls inside calls: each one runs from the shadow pointer of the C
	// that called it.
	if v := mustOne(t, c, "SELECT add1(add1(add1(add1(0))))"); v != "4" {
		t.Fatalf("nested add1 = %s", v)
	}
}

func TestFunctionValues(t *testing.T) {
	c := open(t, ":memory:")
	var got []driver.Value
	err := c.CreateFunction("echo", -1, 0, func(args []driver.Value) (driver.Value, error) {
		got = args
		return args[len(args)-1], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if v := mustOne(t, c, "SELECT typeof(echo(1, 2.5, 'é', x'00ff', NULL))"); v != "null" {
		t.Fatalf("NULL came back as %s", v)
	}
	want := []driver.Value{int64(1), 2.5, "é", []byte{0, 0xff}, nil}
	if fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", want) {
		t.Fatalf("arguments %#v, want %#v", got, want)
	}
	for sql, want := range map[string]string{
		"SELECT typeof(echo(x''))":            "blob",
		"SELECT quote(echo(x'0102'))":         "X'0102'",
		"SELECT echo('')":                     "",
		"SELECT typeof(echo(''))":             "text",
		"SELECT echo(1.5) * 2":                "3.0",
		"SELECT echo(-9223372036854775808)":   "-9223372036854775808",
		"SELECT length(echo(zeroblob(1000)))": "1000",
	} {
		if v := mustOne(t, c, sql); v != want {
			t.Errorf("%s = %q, want %q", sql, v, want)
		}
	}
	if err := c.CreateFunction("kinds", 1, 0, func(args []driver.Value) (driver.Value, error) {
		switch args[0].(int64) {
		case 0:
			return true, nil
		case 1:
			return 7, nil
		case 2:
			return []byte(nil), nil
		}
		return struct{}{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	for sql, want := range map[string]string{
		"SELECT kinds(0)":         "1",
		"SELECT kinds(1)":         "7",
		"SELECT typeof(kinds(2))": "null",
	} {
		if v := mustOne(t, c, sql); v != want {
			t.Errorf("%s = %q, want %q", sql, v, want)
		}
	}
	if _, err := one(t, c, "SELECT kinds(3)"); err == nil || !strings.Contains(err.Error(), "struct {}") {
		t.Errorf("a result of an unknown type: %v", err)
	}
}

// An argument handed to the function is a copy: SQLite's memory under it is
// reused after the call.
func TestFunctionArgumentsAreCopies(t *testing.T) {
	c := open(t, ":memory:")
	var kept [][]byte
	var keptText []string
	err := c.CreateFunction("keep", 2, 0, func(args []driver.Value) (driver.Value, error) {
		kept = append(kept, args[0].([]byte))
		keptText = append(keptText, args[1].(string))
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Exec("WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i < 99) " +
		"SELECT keep(randomblob(100), printf('%050d', i)) FROM n"); err != nil {
		t.Fatal(err)
	}
	for i, s := range keptText {
		if s != fmt.Sprintf("%050d", i) {
			t.Fatalf("text %d changed to %q", i, s)
		}
	}
	seen := map[string]bool{}
	for _, b := range kept {
		seen[string(b)] = true
	}
	if len(seen) != 100 {
		t.Fatalf("%d distinct blobs of 100", len(seen))
	}
}

func TestFunctionError(t *testing.T) {
	c := open(t, ":memory:")
	if err := c.CreateFunction("fail", 0, 0, func([]driver.Value) (driver.Value, error) {
		return nil, errors.New("no luck")
	}); err != nil {
		t.Fatal(err)
	}
	_, err := one(t, c, "SELECT fail()")
	var e *Error
	if !errors.As(err, &e) || e.Msg != "no luck" {
		t.Fatalf("error %v", err)
	}
	if v := mustOne(t, c, "SELECT 'still here'"); v != "still here" {
		t.Fatal(v)
	}
}

func TestFunctionPanicFailsOnlyItsStatement(t *testing.T) {
	c := open(t, ":memory:")
	if err := c.CreateFunction("boom", 0, 0, func([]driver.Value) (driver.Value, error) {
		panic("kaboom")
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec("CREATE TABLE t(x); BEGIN; INSERT INTO t VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	_, err := one(t, c, "SELECT boom()")
	if err == nil || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("error %v", err)
	}
	if c.Broken() {
		t.Fatal("a panic in a user function broke the connection")
	}
	// The transaction is still open and holds its row.
	if err := c.Exec("INSERT INTO t VALUES (2); COMMIT"); err != nil {
		t.Fatal(err)
	}
	if v := mustOne(t, c, "SELECT sum(x) FROM t"); v != "3" {
		t.Fatalf("sum %s", v)
	}
}

// A function that sets a result larger than the shadow stack takes for a
// copy goes through malloc'd memory.
func TestFunctionLargeResult(t *testing.T) {
	c := open(t, ":memory:")
	text := strings.Repeat("abcdefgh", (3*big)/8)
	blob := bytes.Repeat([]byte{1, 2, 3, 4}, big)
	if err := c.CreateFunction("large", 1, 0, func(args []driver.Value) (driver.Value, error) {
		if args[0].(int64) == 0 {
			return text, nil
		}
		return blob, nil
	}); err != nil {
		t.Fatal(err)
	}
	if v := mustOne(t, c, "SELECT large(0)"); v != text {
		t.Fatalf("text of %d bytes, want %d", len(v), len(text))
	}
	if v := mustOne(t, c, "SELECT length(large(1)) || ' ' || hex(substr(large(1), -2))"); v != fmt.Sprintf("%d 0304", len(blob)) {
		t.Fatalf("blob: %s", v)
	}
}

// A function called from triggers deep inside one statement writes its copies
// from the shadow pointer it was called with, above every frame of the C
// under it. Writing from the connection's own pointer would overwrite them.
func TestFunctionDeepInTriggers(t *testing.T) {
	c := open(t, ":memory:")
	if err := c.CreateFunction("pad", 2, FuncDeterministic, func(args []driver.Value) (driver.Value, error) {
		return strings.Repeat(args[1].(string), int(args[0].(int64))), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(`PRAGMA recursive_triggers = 1;
		CREATE TABLE t(n INTEGER, s TEXT);
		CREATE TRIGGER down AFTER INSERT ON t WHEN new.n > 0 BEGIN
			INSERT INTO t VALUES (new.n - 1, pad(new.n % 7 + 1, printf('%04d', new.n)));
		END;
		INSERT INTO t VALUES (900, pad(3, 'x'))`); err != nil {
		t.Fatal(err)
	}
	s, _, err := c.Prepare("SELECT n, s FROM t ORDER BY n")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Finalize()
	rows := 0
	for {
		row, err := s.Step()
		if err != nil {
			t.Fatal(err)
		}
		if !row {
			break
		}
		n := s.ColumnInt64(0)
		want := strings.Repeat(fmt.Sprintf("%04d", n+1), int((n+1)%7+1))
		if n == 900 {
			want = "xxx"
		}
		if got := s.ColumnText(1); got != want {
			t.Fatalf("row %d: %q, want %q", n, got, want)
		}
		rows++
	}
	if rows != 901 {
		t.Fatalf("%d rows", rows)
	}
}

func liveFuncs() int {
	funcs.mu.Lock()
	defer funcs.mu.Unlock()
	return int(funcs.n) - len(funcs.free)
}

func TestFunctionHandlesFreed(t *testing.T) {
	before := liveFuncs()
	c, err := Open(":memory:", OpenReadWrite|OpenCreate, "")
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		// Each replaces the one before it, which SQLite then destroys.
		if err := c.CreateFunction("f", 0, 0, func([]driver.Value) (driver.Value, error) { return 1, nil }); err != nil {
			t.Fatal(err)
		}
	}
	if n := liveFuncs() - before; n != 1 {
		t.Errorf("%d live handles after replacing, want 1", n)
	}
	// A call SQLite refuses destroys its handle too.
	if err := c.CreateFunction("g", -2, 0, add1); err == nil {
		t.Error("nArg -2 accepted")
	}
	if n := liveFuncs() - before; n != 1 {
		t.Errorf("%d live handles after a refused call, want 1", n)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if n := liveFuncs() - before; n != 0 {
		t.Errorf("%d live handles after close", n)
	}
}

func TestFunctionConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := Open(":memory:", OpenReadWrite|OpenCreate, "")
			if err != nil {
				t.Error(err)
				return
			}
			defer c.Close()
			if err := c.CreateFunction("who", 1, 0, func(args []driver.Value) (driver.Value, error) {
				return fmt.Sprintf("%d:%s", g, args[0]), nil
			}); err != nil {
				t.Error(err)
				return
			}
			for i := range 200 {
				v, err := one(t, c, fmt.Sprintf("SELECT who('%d')", i))
				if want := fmt.Sprintf("%d:%d", g, i); err != nil || v != want {
					t.Errorf("got %q %v, want %q", v, err, want)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// sqlite3_exec keeps the SQL text and its statement on the shadow stack while
// the statements run. A function that wrote its result from below them would
// overwrite the statements still to come.
func TestFunctionKeepsCallerFrames(t *testing.T) {
	c := open(t, ":memory:")
	if err := c.CreateFunction("fill", 1, 0, func(args []driver.Value) (driver.Value, error) {
		return strings.Repeat("\xff", int(args[0].(int64))), nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{16, 4096, big} {
		sql := fmt.Sprintf("CREATE TABLE t(x); INSERT INTO t VALUES (length(fill(%d))); "+
			"INSERT INTO t VALUES (length(fill(%[1]d)) + 1); CREATE TABLE u(y)", size)
		if err := c.Exec(sql); err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if v := mustOne(t, c, "SELECT group_concat(x) || '/' || (SELECT count(*) FROM u) FROM t"); v != fmt.Sprintf("%d,%d/0", size, size+1) {
			t.Fatalf("size %d: %s", size, v)
		}
		if err := c.Exec("DROP TABLE t; DROP TABLE u"); err != nil {
			t.Fatal(err)
		}
	}
}

// gsum adds integers. It fails at a step of 13 and panics at one of 666.
type gsum struct{ n int64 }

func (g *gsum) Step(args []driver.Value) error {
	v, _ := args[0].(int64)
	switch v {
	case 13:
		return errors.New("unlucky")
	case 666:
		panic("devilish")
	}
	g.n += v
	return nil
}

func (g *gsum) Final() (driver.Value, error) {
	if g.n == 99 {
		return nil, errors.New("final says no")
	}
	return g.n, nil
}

func liveAggs() int {
	aggs.mu.Lock()
	defer aggs.mu.Unlock()
	return int(aggs.n) - len(aggs.free)
}

func TestAggregate(t *testing.T) {
	c := open(t, ":memory:")
	if err := c.CreateAggregate("gsum", 1, 0, func() Agg { return &gsum{} }); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(`CREATE TABLE t(g, x); CREATE TABLE empty(x);
		INSERT INTO t VALUES (1, 1), (1, 2), (2, 10), (2, 20), (3, 5)`); err != nil {
		t.Fatal(err)
	}
	before := liveAggs()
	if v := mustOne(t, c, "SELECT group_concat(g || ':' || s, ' ') FROM (SELECT g, gsum(x) AS s FROM t GROUP BY g ORDER BY g)"); v != "1:3 2:30 3:5" {
		t.Fatalf("groups: %s", v)
	}
	// A group no row reached gets a state of its own for Final.
	if v := mustOne(t, c, "SELECT gsum(x) FROM empty"); v != "0" {
		t.Fatalf("empty: %s", v)
	}
	for sql, want := range map[string]string{
		"SELECT gsum(x) FROM (SELECT 1 AS x UNION ALL SELECT 13)":  "unlucky",
		"SELECT gsum(x) FROM (SELECT 1 AS x UNION ALL SELECT 666)": "devilish",
		"SELECT gsum(x) FROM (SELECT 99 AS x)":                     "final says no",
	} {
		if _, err := one(t, c, sql); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want an error with %q", sql, err, want)
		}
	}
	if c.Broken() {
		t.Fatal("an error in an aggregate broke the connection")
	}
	// Every state was dropped, also of the statements that stopped early.
	if n := liveAggs() - before; n != 0 {
		t.Fatalf("%d aggregate states left", n)
	}
}

func liveCollations() int {
	collations.mu.Lock()
	defer collations.mu.Unlock()
	return int(collations.n) - len(collations.free)
}

func TestCollation(t *testing.T) {
	before := liveCollations()
	c, err := Open(":memory:", OpenReadWrite|OpenCreate, "")
	if err != nil {
		t.Fatal(err)
	}
	reverse := func(a, b string) int { return strings.Compare(b, a) }
	if err := c.CreateCollation("rev", reverse); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(`CREATE TABLE t(x TEXT); INSERT INTO t VALUES ('b'), ('a'), ('c'), ('')`); err != nil {
		t.Fatal(err)
	}
	if v := mustOne(t, c, "SELECT group_concat(x, ',') FROM (SELECT x FROM t ORDER BY x COLLATE rev)"); v != "c,b,a," {
		t.Fatalf("order: %q", v)
	}
	// Replacing a collation while a statement runs is refused, and
	// SQLite does not drop the handle of a refused one: the host does.
	s, _, err := c.Prepare("SELECT x FROM t ORDER BY x COLLATE rev")
	if err != nil {
		t.Fatal(err)
	}
	if row, err := s.Step(); err != nil || !row {
		t.Fatalf("step: %v %v", row, err)
	}
	if err := c.CreateCollation("rev", reverse); err == nil {
		t.Error("replacing a collation in use succeeded")
	}
	s.Finalize()
	if n := liveCollations() - before; n != 1 {
		t.Errorf("%d live collations, want 1", n)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if n := liveCollations() - before; n != 0 {
		t.Errorf("%d live collations after close", n)
	}
}

// A comparison cannot fail in SQLite: after a panic in a collation SQLite is
// stopped as when memory runs out. The statement fails with the panic, and
// SQLite rolls back the statement or the whole transaction. Nothing written
// with a comparison after the panic is ever kept, and the connection and
// the file stay usable.
func TestCollationPanic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.db")
	c := open(t, path)
	other := open(t, path)
	pc := func(a, b string) int {
		if a == "zz" || b == "zz" {
			panic("cannot compare zz")
		}
		return strings.Compare(a, b)
	}
	for _, conn := range []*Conn{c, other} {
		if err := conn.CreateCollation("pc", pc); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE t(x TEXT); CREATE INDEX tx ON t(x COLLATE pc);
		CREATE TABLE u(x TEXT); INSERT INTO u VALUES ('zz'), ('a');
		CREATE TABLE r(n INTEGER);
		INSERT INTO t VALUES ('a'), ('m'), ('y')`); err != nil {
		t.Fatal(err)
	}
	rows := func(table string) string {
		t.Helper()
		return mustOne(t, other, "SELECT ifnull(group_concat(x, ','), '') FROM (SELECT x FROM "+table+" ORDER BY rowid)")
	}
	isPanic := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, ErrCollationPanic) || !strings.Contains(err.Error(), "cannot compare zz") {
			t.Fatalf("%s: %v, want the panic", what, err)
		}
		if c.Broken() {
			t.Fatalf("%s: the connection is broken", what)
		}
	}
	step := func(sql string) error {
		t.Helper()
		s, _, err := c.Prepare(sql)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Finalize()
		for {
			more, err := s.Step()
			if err != nil || !more {
				return err
			}
		}
	}

	t.Run("autocommit write", func(t *testing.T) {
		isPanic("insert", step(`INSERT INTO t VALUES ('zz')`))
		isPanic("insert returning", step(`INSERT INTO t VALUES ('zz') RETURNING x`))
		if v := rows("t"); v != "a,m,y" {
			t.Fatalf("t: %s", v)
		}
	})

	t.Run("exec stops at the statement", func(t *testing.T) {
		isPanic("exec", c.Exec(`INSERT INTO t VALUES ('c'); INSERT INTO t VALUES ('zz'); INSERT INTO t VALUES ('d')`))
		if v := rows("t"); v != "a,m,y,c" {
			t.Fatalf("t: %s", v)
		}
		if err := c.Exec(`DELETE FROM t WHERE x = 'c'`); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("write while another write is open", func(t *testing.T) {
		// r's statement holds the write transaction open. SQLite rolls
		// back the insert of zz alone, or the whole transaction: then the
		// rest of r's statement fails.
		s, _, err := c.Prepare(`INSERT INTO r VALUES (1), (2) RETURNING n`)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Finalize()
		if more, err := s.Step(); err != nil || !more {
			t.Fatalf("first row: %v %v", more, err)
		}
		isPanic("insert", c.Exec(`INSERT INTO t VALUES ('zz')`))
		for {
			if more, err := s.Step(); err != nil || !more {
				break
			}
		}
		if v := rows("t"); v != "a,m,y" {
			t.Fatalf("t: %s", v)
		}
		if !c.AutoCommit() {
			t.Fatal("a transaction is still open")
		}
		if err := c.Exec(`DELETE FROM r`); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("read in a transaction", func(t *testing.T) {
		// SQLite rolls the transaction back, as after any failed
		// allocation in a read.
		if err := c.Exec(`BEGIN; INSERT INTO t VALUES ('b')`); err != nil {
			t.Fatal(err)
		}
		isPanic("read", step(`SELECT x FROM u ORDER BY x COLLATE pc`))
		if !c.AutoCommit() {
			t.Fatal("the transaction is still open")
		}
		if v := rows("t"); v != "a,m,y" {
			t.Fatalf("t: %s", v)
		}
	})

	t.Run("write in a transaction", func(t *testing.T) {
		if err := c.Exec(`BEGIN; INSERT INTO t VALUES ('b')`); err != nil {
			t.Fatal(err)
		}
		isPanic("insert", c.Exec(`INSERT INTO t VALUES ('zz')`))
		want := "a,m,y"
		if !c.AutoCommit() {
			// Only the statement was rolled back: the rest commits.
			if err := c.Exec(`COMMIT`); err != nil {
				t.Fatal(err)
			}
			want = "a,m,y,b"
		}
		if v := rows("t"); v != want {
			t.Fatalf("t: %s, want %s", v, want)
		}
		if err := c.Exec(`DELETE FROM t WHERE x = 'b'`); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("savepoint", func(t *testing.T) {
		err := c.Exec(`SAVEPOINT a; INSERT INTO t VALUES ('b'); INSERT INTO t VALUES ('zz')`)
		isPanic("insert", err)
		if !c.AutoCommit() {
			if err := c.Exec(`RELEASE a`); err != nil {
				t.Fatal(err)
			}
		}
		if !c.AutoCommit() {
			t.Fatal("a transaction is still open")
		}
		if v := rows("t"); v != "a,m,y" && v != "a,m,y,b" {
			t.Fatalf("t: %s", v)
		}
		if err := c.Exec(`DELETE FROM t WHERE x = 'b'`); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("vacuum", func(t *testing.T) {
		// VACUUM builds the indexes again, comparing zz, and copies the
		// result into the file: it must stop before the copy.
		if err := c.Exec(`CREATE TABLE v(x TEXT); CREATE INDEX vx ON v(x COLLATE pc);
			WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 2000)
			INSERT INTO v SELECT printf('k%05d', i) FROM n`); err != nil {
			t.Fatal(err)
		}
		// zz goes in through a connection whose collation does not panic.
		calm := open(t, path)
		if err := calm.CreateCollation("pc", strings.Compare); err != nil {
			t.Fatal(err)
		}
		if err := calm.Exec(`INSERT INTO v VALUES ('zz')`); err != nil {
			t.Fatal(err)
		}
		isPanic("vacuum", c.Exec(`VACUUM`))
		into := filepath.Join(t.TempDir(), "into.db")
		isPanic("vacuum into", c.Exec(`VACUUM INTO '`+into+`'`))
		if v := mustOne(t, calm, "PRAGMA integrity_check"); v != "ok" {
			t.Fatalf("integrity_check: %s", v)
		}
		if err := calm.Exec(`DROP TABLE v`); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("read in autocommit", func(t *testing.T) {
		isPanic("read", step(`SELECT x FROM u ORDER BY x COLLATE pc`))
	})

	// Both connections write again, and the file is whole.
	if err := c.Exec(`INSERT INTO t VALUES ('e')`); err != nil {
		t.Fatal(err)
	}
	if err := other.Exec(`INSERT INTO t VALUES ('f')`); err != nil {
		t.Fatal(err)
	}
	if v := mustOne(t, other, "PRAGMA integrity_check"); v != "ok" {
		t.Fatalf("integrity_check: %s", v)
	}
	if v := rows("t"); v != "a,m,y,e,f" {
		t.Fatalf("at the end: %s", v)
	}
}
