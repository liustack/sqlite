// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite

import (
	"context"
	"testing"
)

// Each rows of a statement gives column names of its own: database/sql hands
// them to its caller, who may change them.
func TestColumnsOwnSlice(t *testing.T) {
	c := memConn(t)
	ds, err := c.PrepareContext(context.Background(), `SELECT 1 AS Name`)
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	s := ds.(*stmt)
	r, err := s.query(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Columns()[0] = "changed"
	r.Close()
	r, err = s.query(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := r.Columns()[0]; got != "Name" {
		t.Fatalf("column %q, want Name", got)
	}
}
