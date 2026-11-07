// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite

import (
	"context"
	"database/sql/driver"
	"io"
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
