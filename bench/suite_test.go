// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package bench

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The workloads of this file follow cvilsmeier/go-sqlite-bench (real, many,
// large, concurrent) with its schema, at sizes that let six rounds of four
// drivers run in minutes, plus a mix of readers and a writer.

const appSchema = `
CREATE TABLE users (
	id INTEGER PRIMARY KEY NOT NULL,
	created INTEGER NOT NULL,
	email TEXT NOT NULL,
	active INTEGER NOT NULL);
CREATE INDEX users_created ON users(created);
CREATE TABLE articles (
	id INTEGER PRIMARY KEY NOT NULL,
	created INTEGER NOT NULL,
	userId INTEGER NOT NULL REFERENCES users(id),
	text TEXT NOT NULL);
CREATE INDEX articles_created ON articles(created);
CREATE INDEX articles_userId ON articles(userId);
CREATE TABLE comments (
	id INTEGER PRIMARY KEY NOT NULL,
	created INTEGER NOT NULL,
	articleId INTEGER NOT NULL REFERENCES articles(id),
	text TEXT NOT NULL);
CREATE INDEX comments_created ON comments(created);
CREATE INDEX comments_articleId ON comments(articleId);`

const (
	realUsers    = 100
	realArticles = 20 // per user
	realComments = 20 // per article
	realText     = "text text text text text text text text text text text text"
	baseTime     = 1_755_388_800 // 2025-08-17
)

func email(u int) string { return fmt.Sprintf("user%08d@example.com", u) }

// insertRealUser writes user u with its articles and their comments in one
// transaction, as go-sqlite-bench's real does.
func insertRealUser(tb testing.TB, db *sql.DB, u int) {
	tx, err := db.Begin()
	if err != nil {
		tb.Fatal(err)
	}
	created := baseTime + (u-1)*(1+realArticles*(1+realComments))
	exec(tb, tx, `INSERT INTO users (id, created, email, active) VALUES (?, ?, ?, ?)`, u, created, email(u), true)
	art, err := tx.Prepare(`INSERT INTO articles (id, created, userId, text) VALUES (?, ?, ?, ?)`)
	if err != nil {
		tb.Fatal(err)
	}
	com, err := tx.Prepare(`INSERT INTO comments (id, created, articleId, text) VALUES (?, ?, ?, ?)`)
	if err != nil {
		tb.Fatal(err)
	}
	for a := range realArticles {
		created++
		aid := (u-1)*realArticles + a + 1
		if _, err := art.Exec(aid, created, u, realText); err != nil {
			tb.Fatal(err)
		}
		for c := range realComments {
			created++
			if _, err := com.Exec((aid-1)*realComments+c+1, created, aid, realText); err != nil {
				tb.Fatal(err)
			}
		}
	}
	art.Close()
	com.Close()
	if err := tx.Commit(); err != nil {
		tb.Fatal(err)
	}
}

func fillReal(tb testing.TB, db *sql.DB) {
	exec(tb, db, appSchema)
	for u := 1; u <= realUsers; u++ {
		insertRealUser(tb, db, u)
	}
}

const realQuery = `
SELECT users.id, users.created, users.email, users.active,
	articles.id, articles.created, articles.userId, articles.text,
	comments.id, comments.created, comments.articleId, comments.text
FROM users
LEFT JOIN articles ON articles.userId = users.id
LEFT JOIN comments ON comments.articleId = articles.id
WHERE users.email = ?
ORDER BY users.created, articles.created, comments.created`

// Real/insert: one user with 20 articles and 400 comments in one
// transaction per op. Real/query: one user found by email with its articles
// and comments (two LEFT JOINs, 400 rows) per op.
func BenchmarkReal(b *testing.B) {
	b.Run("insert", func(b *testing.B) {
		db := open(b)
		db.SetMaxOpenConns(1)
		exec(b, db, appSchema)
		b.ReportAllocs()
		b.ResetTimer()
		for i := range b.N {
			insertRealUser(b, db, i+1)
		}
		b.StopTimer()
		if n := count(b, db, "comments"); n != b.N*realArticles*realComments {
			b.Fatalf("%d comments, want %d", n, b.N*realArticles*realComments)
		}
	})
	b.Run("query", func(b *testing.B) {
		db := fixture(b, "real", fillReal)
		st := prepare(b, db, realQuery)
		b.ReportAllocs()
		b.ResetTimer()
		for i := range b.N {
			u := i%realUsers + 1
			rows, err := st.Query(email(u))
			if err != nil {
				b.Fatal(err)
			}
			n := 0
			var lastComment int64
			for rows.Next() {
				var (
					uid, ucreated, aid, acreated, auser, cid, ccreated, carticle int64
					uemail, atext, ctext                                         string
					active                                                       bool
				)
				if err := rows.Scan(&uid, &ucreated, &uemail, &active, &aid, &acreated, &auser, &atext,
					&cid, &ccreated, &carticle, &ctext); err != nil {
					rows.Close()
					b.Fatal(err)
				}
				if uid != int64(u) || auser != int64(u) || ccreated <= lastComment || ctext != realText {
					rows.Close()
					b.Fatalf("user %d: row %d is user %d, article of %d, comment created %d after %d",
						u, n, uid, auser, ccreated, lastComment)
				}
				lastComment = ccreated
				n++
			}
			if err := rows.Close(); err != nil {
				b.Fatal(err)
			}
			if n != realArticles*realComments {
				b.Fatalf("user %d: %d rows, want %d", u, n, realArticles*realComments)
			}
		}
	})
}

// fillUsers makes the schema with n users whose email is text(i).
func fillUsers(n int, text func(i int) string) func(tb testing.TB, db *sql.DB) {
	return func(tb testing.TB, db *sql.DB) {
		exec(tb, db, appSchema)
		tx, err := db.Begin()
		if err != nil {
			tb.Fatal(err)
		}
		st, err := tx.Prepare(`INSERT INTO users (id, created, email, active) VALUES (?, ?, ?, ?)`)
		if err != nil {
			tb.Fatal(err)
		}
		for i := 1; i <= n; i++ {
			if _, err := st.Exec(i, baseTime+60*i, text(i), true); err != nil {
				tb.Fatal(err)
			}
		}
		st.Close()
		if err := tx.Commit(); err != nil {
			tb.Fatal(err)
		}
	}
}

// scanUsers reads every user in order and checks each.
func scanUsers(b *testing.B, st *sql.Stmt, n int, check func(got string, i int) bool) {
	rows, err := st.Query()
	if err != nil {
		b.Fatal(err)
	}
	i := 0
	for rows.Next() {
		var (
			id, created int64
			email       string
			active      bool
		)
		if err := rows.Scan(&id, &created, &email, &active); err != nil {
			rows.Close()
			b.Fatal(err)
		}
		i++
		if id != int64(i) || created != int64(baseTime+60*i) || !active || !check(email, i) {
			rows.Close()
			b.Fatalf("row %d: id %d created %d active %v", i, id, created, active)
		}
	}
	if err := rows.Close(); err != nil {
		b.Fatal(err)
	}
	if i != n {
		b.Fatalf("%d rows, want %d", i, n)
	}
}

const usersQuery = `SELECT id, created, email, active FROM users ORDER BY id`

// Many: all users of a small table per op, so the cost of one query and of
// each row shows (go-sqlite-bench's many queries them 1000 times).
func BenchmarkMany(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			db := fixture(b, fmt.Sprintf("many-%d", n), fillUsers(n, email))
			st := prepare(b, db, usersQuery)
			check := func(got string, i int) bool { return got == email(i) }
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				scanUsers(b, st, n, check)
			}
		})
	}
}

// Large: 100 users whose text is size bytes, all read per op. MB/s is the
// rate of text read.
func BenchmarkLarge(b *testing.B) {
	const n = 100
	for _, size := range []int{50_000, 200_000} {
		b.Run(fmt.Sprintf("size=%dk", size/1000), func(b *testing.B) {
			text := func(i int) string { return strings.Repeat(string(rune('a'+i%26)), size) }
			db := fixture(b, fmt.Sprintf("large-%d", size), fillUsers(n, text))
			st := prepare(b, db, usersQuery)
			check := func(got string, i int) bool {
				return len(got) == size && got[0] == byte('a'+i%26) && got[size-1] == got[0]
			}
			b.SetBytes(int64(n * size))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				scanUsers(b, st, n, check)
			}
		})
	}
}

// Readers: g goroutines each scanning all 100k items, on g connections. An
// op is one scan, so ns/op falls as the scans run side by side
// (go-sqlite-bench's concurrent, on a smaller table).
func BenchmarkReaders(b *testing.B) {
	for _, g := range []int{2, 4, 8} {
		b.Run(fmt.Sprintf("g=%d", g), func(b *testing.B) {
			db := fixture(b, "items", fillItems)
			db.SetMaxOpenConns(g)
			db.SetMaxIdleConns(g)
			st := prepare(b, db, `SELECT id, name, score, created FROM items`)
			var left atomic.Int64
			left.Store(int64(b.N))
			errc := make(chan error, g)
			b.ReportAllocs()
			b.ResetTimer()
			for range g {
				go func() {
					for left.Add(-1) >= 0 {
						if err := scanItems(st); err != nil {
							left.Store(-1)
							errc <- err
							return
						}
					}
					errc <- nil
				}()
			}
			for range g {
				if err := <-errc; err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func scanItems(st *sql.Stmt) error {
	rows, err := st.Query()
	if err != nil {
		return err
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
			return err
		}
		n++
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if n != pointRows {
		return fmt.Errorf("%d rows, want %d", n, pointRows)
	}
	return nil
}

// ReadWrite: 8 goroutines read items by key while one more inserts rows in
// autocommit without pause. An op is one read. writes/s is the rate of the
// writer meanwhile: in WAL neither should stop the other.
func BenchmarkReadWrite(b *testing.B) {
	const readers = 8
	db := fixture(b, "items", fillItems)
	db.SetMaxOpenConns(readers + 1)
	db.SetMaxIdleConns(readers + 1)
	read := prepare(b, db, `SELECT id, name, score, created FROM items WHERE id = ?`)
	write := prepare(b, db, `INSERT INTO items (name, score, created) VALUES (?, ?, ?)`)
	var (
		left   atomic.Int64
		writes atomic.Int64
		stop   = make(chan struct{})
		ww, rw sync.WaitGroup
	)
	left.Store(int64(b.N))
	errc := make(chan error, readers+1)
	b.ReportAllocs()
	b.ResetTimer()
	start := time.Now()
	ww.Add(1)
	go func() {
		defer ww.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := write.Exec("name-0123456789abcd", 1.5, 1); err != nil {
				errc <- err
				return
			}
			writes.Add(1)
		}
	}()
	for range readers {
		rw.Add(1)
		go func() {
			defer rw.Done()
			for {
				k := left.Add(-1)
				if k < 0 {
					return
				}
				id := k%pointRows + 1
				var (
					got, created int64
					name         string
					score        float64
				)
				if err := read.QueryRow(id).Scan(&got, &name, &score, &created); err != nil || got != id {
					errc <- fmt.Errorf("read %d: got %d, %v", id, got, err)
					left.Store(-1)
					return
				}
			}
		}()
	}
	// The writer runs until the readers are done.
	rw.Wait()
	close(stop)
	ww.Wait()
	elapsed := time.Since(start)
	b.StopTimer()
	select {
	case err := <-errc:
		b.Fatal(err)
	default:
	}
	b.ReportMetric(float64(writes.Load())/elapsed.Seconds(), "writes/s")
}
