// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite_test

import (
	"runtime"
	"testing"
)

// Numbers scanned into numbers are not put in an interface on the way: a
// row of them costs no allocation.
func TestScanNumbersDoNotAllocate(t *testing.T) {
	db := openDB(t, ":memory:")
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE t(a INTEGER, b REAL, c INTEGER);
		WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 1000)
		INSERT INTO t SELECT 100000 + i, i * 1.5, 1 << 40 FROM n`); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT a, b, c FROM t`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var (
		a, c int64
		b    float64
		sum  int64
	)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for rows.Next() {
		if err := rows.Scan(&a, &b, &c); err != nil {
			t.Fatal(err)
		}
		sum += a
	}
	runtime.ReadMemStats(&after)
	allocs := after.Mallocs - before.Mallocs
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if sum != 1000*100000+1000*1001/2 {
		t.Errorf("sum %d", sum)
	}
	if allocs > 10 { // for 1000 rows
		t.Errorf("%d allocations for 1000 rows", allocs)
	}
}
