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
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreparedExplainFollowsSchema(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	a, b := fileConn(t, name), fileConn(t, name)
	mustExec(t, a, `CREATE TABLE t(a, b)`)
	ds, err := a.PrepareContext(context.Background(), `EXPLAIN QUERY PLAN SELECT b FROM t WHERE a = ?`)
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	s := ds.(*stmt)
	plan := func() string {
		r, err := s.query(context.Background(), []driver.NamedValue{{Ordinal: 1, Value: int64(1)}})
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		var out []driver.Value
		dest := make([]driver.Value, len(r.Columns()))
		for r.Next(dest) == nil {
			out = append(out, dest[3])
		}
		return fmt.Sprint(out)
	}
	if got := plan(); !strings.Contains(got, "SCAN") {
		t.Fatalf("plan %s, want a scan", got)
	}
	mustExec(t, b, `CREATE INDEX ta ON t(a, b)`)
	queryOne(t, a, `SELECT count(*) FROM t`) // a reads the new schema
	if got := plan(); !strings.Contains(got, "INDEX ta") {
		t.Fatalf("plan %s after CREATE INDEX, want ta", got)
	}
	mustExec(t, b, `DROP INDEX ta`)
	queryOne(t, a, `SELECT count(*) FROM t`)
	if got := plan(); !strings.Contains(got, "SCAN") {
		t.Fatalf("plan %s after DROP INDEX, want a scan", got)
	}
}

func TestPreparedExplainAfterDrop(t *testing.T) {
	c := memConn(t)
	mustExec(t, c, `CREATE TABLE t(a)`)
	ds, err := c.PrepareContext(context.Background(), `EXPLAIN INSERT INTO t VALUES (1)`)
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	s := ds.(*stmt)
	run := func() error {
		r, err := s.query(context.Background(), nil)
		if err != nil {
			return err
		}
		defer r.Close()
		dest := make([]driver.Value, len(r.Columns()))
		for {
			if err := r.Next(dest); err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
		}
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	mustExec(t, c, `DROP TABLE t`)
	for i := range 50 {
		mustExec(t, c, fmt.Sprintf("CREATE TABLE zz%d(x, y, z)", i))
	}
	if err := run(); err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Fatalf("got %v, want no such table", err)
	}
}
