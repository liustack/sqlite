// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package bench

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
)

// open returns a new database in a fresh temp directory. The pool is closed
// when the benchmark ends.
func open(tb testing.TB) *sql.DB {
	tb.Helper()
	db, err := sql.Open(DriverName, DSN(filepath.Join(tb.TempDir(), "bench.db")))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { db.Close() })
	return db
}

func exec(tb testing.TB, db interface {
	Exec(string, ...any) (sql.Result, error)
}, q string, args ...any) {
	tb.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		tb.Fatal(err)
	}
}

func prepare(tb testing.TB, db *sql.DB, q string) *sql.Stmt {
	tb.Helper()
	st, err := db.Prepare(q)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { st.Close() })
	return st
}

const (
	pointRows = 100_000
	userRows  = 10_000
	postRows  = 100_000
)

// fillItems makes table items(id, name, score, created) with n rows.
func fillItems(tb testing.TB, db *sql.DB, n int) {
	tb.Helper()
	exec(tb, db, `CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT NOT NULL, score REAL NOT NULL, created INTEGER NOT NULL)`)
	tx, err := db.Begin()
	if err != nil {
		tb.Fatal(err)
	}
	st, err := tx.Prepare(`INSERT INTO items (id, name, score, created) VALUES (?, ?, ?, ?)`)
	if err != nil {
		tb.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		if _, err := st.Exec(i, fmt.Sprintf("item-%015d", i), float64(i)*0.5, 1_700_000_000+i); err != nil {
			tb.Fatal(err)
		}
	}
	st.Close()
	if err := tx.Commit(); err != nil {
		tb.Fatal(err)
	}
}

const insertSQL = `INSERT INTO t (name, score, created) VALUES (?, ?, ?)`

func insertTable(tb testing.TB, db *sql.DB) {
	tb.Helper()
	exec(tb, db, `CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT NOT NULL, score REAL NOT NULL, created INTEGER NOT NULL)`)
}

// Insert: one autocommit INSERT per op, one goroutine.
func BenchmarkInsert(b *testing.B) {
	db := open(b)
	insertTable(b, db)
	st := prepare(b, db, insertSQL)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := st.Exec("name-0123456789abcd", 1.5, i); err != nil {
			b.Fatal(err)
		}
	}
}

// InsertTx: 1000 rows in one transaction per op.
func BenchmarkInsertTx(b *testing.B) {
	db := open(b)
	insertTable(b, db)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tx, err := db.Begin()
		if err != nil {
			b.Fatal(err)
		}
		st, err := tx.Prepare(insertSQL)
		if err != nil {
			b.Fatal(err)
		}
		for j := 0; j < 1000; j++ {
			if _, err := st.Exec("name-0123456789abcd", 1.5, j); err != nil {
				b.Fatal(err)
			}
		}
		if err := st.Close(); err != nil {
			b.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			b.Fatal(err)
		}
	}
}

// Point: SELECT one row by primary key, one goroutine.
func BenchmarkPoint(b *testing.B) {
	db := open(b)
	fillItems(b, db, pointRows)
	st := prepare(b, db, `SELECT id, name, score, created FROM items WHERE id = ?`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var (
			id, created int64
			name        string
			score       float64
		)
		if err := st.QueryRow(i%pointRows+1).Scan(&id, &name, &score, &created); err != nil {
			b.Fatal(err)
		}
	}
}

// Scan: read and scan every column of 100k rows per op.
func BenchmarkScan(b *testing.B) {
	db := open(b)
	fillItems(b, db, pointRows)
	st := prepare(b, db, `SELECT id, name, score, created FROM items`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := st.Query()
		if err != nil {
			b.Fatal(err)
		}
		n := 0
		for rows.Next() {
			var (
				id, created int64
				name        string
				score       float64
			)
			if err := rows.Scan(&id, &name, &score, &created); err != nil {
				rows.Close()
				b.Fatal(err)
			}
			n++
		}
		if err := rows.Err(); err != nil {
			b.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			b.Fatal(err)
		}
		if n != pointRows {
			b.Fatalf("got %d rows, want %d", n, pointRows)
		}
	}
}

const aggregateSQL = `
SELECT u.id, u.name, COUNT(*) AS posts, SUM(p.score) AS total, MAX(p.created) AS last
FROM users u JOIN posts p ON p.user_id = u.id
WHERE u.id % 40 = 0
GROUP BY u.id, u.name
ORDER BY total DESC, u.id`

// Aggregate: join 10k users with 100k posts, group and sort, 250 rows back.
func BenchmarkAggregate(b *testing.B) {
	db := open(b)
	exec(b, db, `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`)
	exec(b, db, `CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, score REAL NOT NULL, created INTEGER NOT NULL)`)
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	for i := 1; i <= userRows; i++ {
		exec(b, tx, `INSERT INTO users (id, name) VALUES (?, ?)`, i, fmt.Sprintf("user-%d", i))
	}
	for i := 1; i <= postRows; i++ {
		exec(b, tx, `INSERT INTO posts (id, user_id, score, created) VALUES (?, ?, ?, ?)`,
			i, i%userRows+1, float64(i%1000)/10, 1_700_000_000+i)
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	// The posts.user_id index is part of the schema, not the timed query.
	exec(b, db, `CREATE INDEX posts_user ON posts (user_id)`)
	st := prepare(b, db, aggregateSQL)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := st.Query()
		if err != nil {
			b.Fatal(err)
		}
		n := 0
		for rows.Next() {
			var (
				id, posts, last int64
				name            string
				total           float64
			)
			if err := rows.Scan(&id, &name, &posts, &total, &last); err != nil {
				rows.Close()
				b.Fatal(err)
			}
			n++
		}
		if err := rows.Err(); err != nil {
			b.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			b.Fatal(err)
		}
		if n != userRows/40 {
			b.Fatalf("got %d rows, want %d", n, userRows/40)
		}
	}
}

// PointParallel: point lookups on GOMAXPROCS*2 connections, all kept idle
// between uses (database/sql keeps 2 by default). RunParallel
// starts GOMAXPROCS goroutines (parallelism 1), each doing lookups.
func BenchmarkPointParallel(b *testing.B) {
	db := open(b)
	fillItems(b, db, pointRows)
	db.SetMaxOpenConns(runtime.GOMAXPROCS(0) * 2)
	db.SetMaxIdleConns(runtime.GOMAXPROCS(0) * 2)
	st := prepare(b, db, `SELECT id, name, score, created FROM items WHERE id = ?`)
	var next atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			var (
				id, created int64
				name        string
				score       float64
			)
			k := next.Add(1)
			if err := st.QueryRow(k%pointRows+1).Scan(&id, &name, &score, &created); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

// InsertParallel: autocommit INSERTs from exactly g goroutines (we start them
// ourselves, not RunParallel, so g does not depend on GOMAXPROCS). The pool
// is capped at g connections. Each op is one autocommit INSERT, durable per
// the DSN settings.
func BenchmarkInsertParallel(b *testing.B) {
	for _, g := range []int{1, 8, 64} {
		b.Run(fmt.Sprintf("g=%d", g), func(b *testing.B) {
			db := open(b)
			insertTable(b, db)
			db.SetMaxOpenConns(g)
			db.SetMaxIdleConns(g)
			st := prepare(b, db, insertSQL)
			var left atomic.Int64
			left.Store(int64(b.N))
			errc := make(chan error, g)
			b.ReportAllocs()
			b.ResetTimer()
			for w := 0; w < g; w++ {
				go func() {
					for left.Add(-1) >= 0 {
						if _, err := st.Exec("name-0123456789abcd", 1.5, 1); err != nil {
							left.Store(-1)
							errc <- err
							return
						}
					}
					errc <- nil
				}()
			}
			for w := 0; w < g; w++ {
				if err := <-errc; err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
