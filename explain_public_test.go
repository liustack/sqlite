// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package sqlite_test

import (
	"context"
	"testing"
)

func TestExecExplainCloses(t *testing.T) {
	db := openDB(t, ":memory:")
	db.SetMaxOpenConns(1)
	for _, q := range []string{`EXPLAIN SELECT 1`, `EXPLAIN QUERY PLAN SELECT 1`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExplainExecWhileRowsOpen(t *testing.T) {
	c := oneConn(t)
	s, err := c.PrepareContext(context.Background(), `EXPLAIN SELECT ?`)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, err := s.QueryContext(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	addr := func() int64 {
		t.Helper()
		if !r.Next() {
			t.Fatal("no row", r.Err())
		}
		cols, _ := r.Columns()
		vals := make([]any, len(cols))
		dest := make([]any, len(cols))
		for i := range dest {
			dest[i] = &vals[i]
		}
		if err := r.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		return vals[0].(int64)
	}
	if a := addr(); a != 0 {
		t.Fatalf("address %d, want 0", a)
	}
	if _, err := s.ExecContext(context.Background(), 2); err == nil {
		t.Error("Exec ran while the rows were open")
	}
	if a := addr(); a != 1 {
		t.Fatalf("address %d after Exec, want 1", a)
	}
}
