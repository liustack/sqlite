// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && (amd64 || arm64))

package sqlite

import (
	"context"
	"database/sql/driver"
	"io"
	"net/url"
	"strings"
	"testing"
)

func memConn(t *testing.T) *conn {
	t.Helper()
	cn, err := (&Driver{}).Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cn.Close() })
	return cn.(*conn)
}

func fileConn(t *testing.T, name string) *conn {
	t.Helper()
	cn, err := (&Driver{}).Open("file:" + name + "?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cn.Close() })
	return cn.(*conn)
}

func mustExec(t *testing.T, c *conn, sql string) {
	t.Helper()
	if _, err := c.ExecContext(context.Background(), sql, nil); err != nil {
		t.Fatal(err)
	}
}

func queryOne(t *testing.T, c *conn, sql string, args ...any) []driver.Value {
	t.Helper()
	r := openRows(t, c, sql, args...)
	defer r.Close()
	return drain(t, r)
}

func openRows(t *testing.T, c *conn, sql string, args ...any) *rows {
	t.Helper()
	nv := make([]driver.NamedValue, len(args))
	for i, a := range args {
		nv[i] = driver.NamedValue{Ordinal: i + 1, Value: a}
	}
	r, err := c.QueryContext(context.Background(), sql, nv)
	if err != nil {
		t.Fatal(err)
	}
	return r.(*rows)
}

func drain(t *testing.T, r *rows) []driver.Value {
	t.Helper()
	var out []driver.Value
	dest := make([]driver.Value, len(r.Columns()))
	for {
		err := r.Next(dest)
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, dest[0])
	}
}

func TestPragmaName(t *testing.T) {
	for p, want := range map[string]string{
		"optimize":           "optimize",
		"optimize(0x10002)":  "optimize",
		" OPTIMIZE = 2":      "OPTIMIZE",
		"main.optimize":      "optimize",
		"main . optimize(2)": "optimize",
		`"main"."optimize"`:  "optimize",
		"[optimize]":         "optimize",
		"`optimize`(2)":      "optimize",
		"foreign_keys(1)":    "foreign_keys",
		"journal_mode = WAL": "journal_mode",
		"(optimize)":         "",
		`"optimize`:          "",
		"":                   "",
	} {
		if got := pragmaName(p); got != want {
			t.Errorf("pragmaName(%q) = %q, want %q", p, got, want)
		}
	}
}

// PRAGMA optimize may write, and a _pragma of the DSN runs as a connection
// opens, without the write lock (Connector.setUp): the driver refuses it
// there and says to run it with Exec.
func TestDSNRefusesOptimize(t *testing.T) {
	for _, p := range []string{"optimize", "optimize(0x10002)", "main.optimize=2", "OPTIMIZE"} {
		_, err := NewConnector("file:x.db?_pragma=" + url.QueryEscape(p))
		if err == nil || !strings.Contains(err.Error(), "Exec") {
			t.Errorf("_pragma=%s: err %v, want a refusal that says to use Exec", p, err)
		}
	}
	if _, err := NewConnector("file:x.db?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"); err != nil {
		t.Fatal(err)
	}
}
