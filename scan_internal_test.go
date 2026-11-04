// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build go1.27 && (((linux || darwin) && (amd64 || arm64)) || (windows && amd64))

package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// ScanColumn gives what sql.ConvertAssign gives with the value of Next, for
// every column type and every kind of destination.
func TestScanColumnMatchesNext(t *testing.T) {
	cn, err := (&Driver{}).Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer cn.Close()
	c := cn.(*conn)
	if _, err := c.ExecContext(context.Background(), `CREATE TABLE t(i1 INTEGER, i2 INTEGER, i3 INTEGER, f REAL,
		s1 TEXT, s2 TEXT, s3 TEXT, b BLOB, n, t1 DATETIME, t2 TIMESTAMP, t3 DATE, t4 DATETIME);
		INSERT INTO t VALUES (42, -7, 1 << 40, 2.5, 'text', '17', '', x'0102', NULL,
		1700000000, '2026-11-04 08:00:00', 'not a time', 1.5)`, nil); err != nil {
		t.Fatal(err)
	}
	r, err := c.QueryContext(context.Background(), `SELECT * FROM t`, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	rs := r.(*rows)
	if err := rs.NextRow(); err != nil {
		t.Fatal(err)
	}
	for i := 9; i < 13; i++ {
		if !rs.isTime(i) {
			t.Fatalf("column %d is not read as a time", i)
		}
	}
	dests := []func() any{
		func() any { return new(int64) }, func() any { return new(int) }, func() any { return new(int32) },
		func() any { return new(float64) }, func() any { return new(string) }, func() any { return new([]byte) },
		func() any { return new(bool) }, func() any { return new(any) }, func() any { return new(time.Time) },
		func() any { return new(sql.NullInt64) }, func() any { return new(sql.NullFloat64) },
		func() any { return new(sql.NullString) }, func() any { return new(sql.NullTime) },
		func() any { return new(sql.RawBytes) },
	}
	var ctx driver.ScanContext
	for i := range rs.Columns() {
		for _, mk := range dests {
			got, want := mk(), mk()
			gerr := rs.ScanColumn(ctx, i, got)
			werr := sql.ConvertAssign(ctx, want, rs.value(i))
			if fmt.Sprint(gerr) != fmt.Sprint(werr) || !reflect.DeepEqual(got, want) {
				t.Errorf("column %d into %T: got %v (%v), want %v (%v)", i, got,
					reflect.ValueOf(got).Elem(), gerr, reflect.ValueOf(want).Elem(), werr)
			}
		}
	}
}
