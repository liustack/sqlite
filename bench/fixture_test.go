// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package bench

import (
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// fixtures holds the databases the read workloads start from. Each is made
// once per process, then copied for every run of a benchmark, so that
// testing.B calling a benchmark again with a larger b.N does not fill the
// tables again.
var fixtures struct {
	sync.Mutex
	dir   string
	paths map[string]string
}

func TestMain(m *testing.M) {
	code := m.Run()
	if fixtures.dir != "" {
		os.RemoveAll(fixtures.dir)
	}
	os.Exit(code)
}

// fixture opens a fresh copy of the database name, made by fill on first
// use. fill gets a pool of one connection.
func fixture(tb testing.TB, name string, fill func(tb testing.TB, db *sql.DB)) *sql.DB {
	tb.Helper()
	src := fixturePath(tb, name, fill)
	dst := filepath.Join(tb.TempDir(), "bench.db")
	copyFile(tb, src, dst)
	db, err := sql.Open(DriverName, DSN(dst, "NORMAL"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { db.Close() })
	return db
}

func fixturePath(tb testing.TB, name string, fill func(tb testing.TB, db *sql.DB)) string {
	fixtures.Lock()
	defer fixtures.Unlock()
	if p, ok := fixtures.paths[name]; ok {
		return p
	}
	if fixtures.dir == "" {
		d, err := os.MkdirTemp("", "bench-fixtures-")
		if err != nil {
			tb.Fatal(err)
		}
		fixtures.dir = d
		fixtures.paths = map[string]string{}
	}
	p := filepath.Join(fixtures.dir, name+".db")
	db, err := sql.Open(DriverName, DSN(p, "NORMAL"))
	if err != nil {
		tb.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	fill(tb, db)
	// Everything into the main file, so that a copy of it alone is whole.
	exec(tb, db, `PRAGMA wal_checkpoint(TRUNCATE)`)
	if err := db.Close(); err != nil {
		tb.Fatal(err)
	}
	fixtures.paths[name] = p
	return p
}

func copyFile(tb testing.TB, src, dst string) {
	tb.Helper()
	in, err := os.Open(src)
	if err != nil {
		tb.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		tb.Fatal(err)
	}
	if err := out.Close(); err != nil {
		tb.Fatal(err)
	}
}
