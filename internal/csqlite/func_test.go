// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package csqlite

import (
	"bytes"
	"errors"
	"fmt"
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

func add1(args []any) (any, error) { return args[0].(int64) + 1, nil }

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
	var got []any
	err := c.CreateFunction("echo", -1, 0, func(args []any) (any, error) {
		got = args
		return args[len(args)-1], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if v := mustOne(t, c, "SELECT typeof(echo(1, 2.5, 'é', x'00ff', NULL))"); v != "null" {
		t.Fatalf("NULL came back as %s", v)
	}
	want := []any{int64(1), 2.5, "é", []byte{0, 0xff}, nil}
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
	if err := c.CreateFunction("kinds", 1, 0, func(args []any) (any, error) {
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
	err := c.CreateFunction("keep", 2, 0, func(args []any) (any, error) {
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
	if err := c.CreateFunction("fail", 0, 0, func([]any) (any, error) {
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
	if err := c.CreateFunction("boom", 0, 0, func([]any) (any, error) {
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
	if err := c.CreateFunction("large", 1, 0, func(args []any) (any, error) {
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
	if err := c.CreateFunction("pad", 2, FuncDeterministic, func(args []any) (any, error) {
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
		if err := c.CreateFunction("f", 0, 0, func([]any) (any, error) { return 1, nil }); err != nil {
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
			if err := c.CreateFunction("who", 1, 0, func(args []any) (any, error) {
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
	if err := c.CreateFunction("fill", 1, 0, func(args []any) (any, error) {
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
