// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

// Package sqltest runs the queries of the tests of ext/.
package sqltest

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/liustack/sqlite"
)

// Open opens a memory database with opt on one connection.
func Open(t testing.TB, opt sqlite.Option) *sql.DB {
	t.Helper()
	c, err := sqlite.NewConnector(":memory:", opt)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(c)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// Want checks that each query gives its text, "NULL" for NULL.
func Want(t *testing.T, db *sql.DB, cases map[string]string) {
	t.Helper()
	for q, want := range cases {
		var v sql.NullString
		if err := db.QueryRow(q).Scan(&v); err != nil {
			t.Errorf("%s: %v", q, err)
			continue
		}
		got := v.String
		if !v.Valid {
			got = "NULL"
		}
		if got != want {
			t.Errorf("%s = %q, want %q", q, got, want)
		}
	}
}

// Fails checks that each query fails with an error that holds its text.
func Fails(t *testing.T, db *sql.DB, cases map[string]string) {
	t.Helper()
	for q, want := range cases {
		var v any
		err := db.QueryRow(q).Scan(&v)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %v, want one with %q", q, err, want)
		}
	}
}
