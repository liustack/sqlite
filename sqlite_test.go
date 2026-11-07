// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/liustack/sqlite"
)

func openDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestRoundTrip(t *testing.T) {
	db := openDB(t, ":memory:")
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE t(i INTEGER, f REAL, s TEXT, b BLOB, d DATETIME, n); CREATE TABLE u(x)`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 4, 12, 30, 15, 123456789, time.UTC)
	r, err := db.Exec(`INSERT INTO t VALUES (?, ?, ?, ?, ?, ?)`, 42, 2.5, "héllo", []byte{0, 1, 2}, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := r.LastInsertId(); id != 1 {
		t.Errorf("last insert id %d", id)
	}
	var (
		i int64
		f float64
		s string
		b []byte
		d time.Time
		n sql.NullString
	)
	if err := db.QueryRow(`SELECT * FROM t`).Scan(&i, &f, &s, &b, &d, &n); err != nil {
		t.Fatal(err)
	}
	if i != 42 || f != 2.5 || s != "héllo" || string(b) != "\x00\x01\x02" || !d.Equal(now) || n.Valid {
		t.Errorf("got %v %v %q %x %v %v", i, f, s, b, d, n)
	}
	if err := db.QueryRow(`SELECT :a + $b`, sql.Named("a", 1), sql.Named("b", 2)).Scan(&i); err != nil || i != 3 {
		t.Errorf("named: %d %v", i, err)
	}
}

func TestTxAndRows(t *testing.T) {
	db := openDB(t, "file:"+filepath.Join(t.TempDir(), "t.db")+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_txlock=immediate")
	if _, err := db.Exec(`CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	st, err := tx.Prepare(`INSERT INTO t(v) VALUES (?)`)
	if err != nil {
		t.Fatal(err)
	}
	for k := 0; k < 1000; k++ {
		if _, err := st.Exec("v"); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT id, v FROM t ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	k := 0
	for rows.Next() {
		var id int64
		var v string
		if err := rows.Scan(&id, &v); err != nil {
			t.Fatal(err)
		}
		k++
	}
	if err := rows.Err(); err != nil || k != 1000 {
		t.Fatalf("%d rows, %v", k, err)
	}
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal mode %q %v", mode, err)
	}
}

func TestCancel(t *testing.T) {
	db := openDB(t, ":memory:")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var n int64
	err := db.QueryRowContext(ctx, `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c) SELECT count(*) FROM c`).Scan(&n)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

func oneConn(t *testing.T) *sql.Conn {
	t.Helper()
	db := openDB(t, ":memory:")
	c, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func connExec(t *testing.T, c *sql.Conn, q string) {
	t.Helper()
	if _, err := c.ExecContext(context.Background(), q); err != nil {
		t.Fatal(err)
	}
}

func drainQuery(c *sql.Conn, q string, args ...any) error {
	rows, err := c.QueryContext(context.Background(), q, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
	}
	err = rows.Err()
	if cerr := rows.Close(); err == nil {
		err = cerr
	}
	return err
}
