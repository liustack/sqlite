// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

// Command native is the driver's side of the comparison with SQLite in C
// (benchrun native). It does through database/sql what c/engine.c and
// c/threads.c do through the C API, and prints the same lines:
//
//	native engine FILE REPS
//	native threads MODE N SECS SYNC CACHE CONN PATH
//
// CONN 1 gives each reader a *sql.Conn of its own, 0 has them share the
// *sql.DB.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/liustack/sqlite"
)

func main() {
	var err error
	switch {
	case len(os.Args) == 4 && os.Args[1] == "engine":
		err = engine(os.Args[2], os.Args[3])
	case len(os.Args) == 9 && os.Args[1] == "threads":
		err = threads(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "usage: native engine FILE REPS | native threads MODE N SECS SYNC CACHE CONN PATH")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "native:", err)
		os.Exit(1)
	}
}

func engine(file, repsArg string) error {
	src, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	reps, err := strconv.Atoi(repsArg)
	if err != nil || reps < 1 {
		return fmt.Errorf("REPS %q", repsArg)
	}
	ctx := context.Background()
	blocks := strings.Split(string(src), "\n@@ ")[1:]
	for _, block := range blocks {
		name, rest, _ := strings.Cut(block, "\n")
		setup, rest, ok1 := strings.Cut(rest, "@@run\n")
		run, check, ok2 := strings.Cut(rest, "@@check\n")
		if !ok1 || !ok2 {
			return fmt.Errorf("%s: want @@run and @@check", name)
		}
		if err := func() error {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				return err
			}
			defer db.Close()
			c, err := db.Conn(ctx)
			if err != nil {
				return err
			}
			defer c.Close()
			if _, err := c.ExecContext(ctx, setup); err != nil {
				return err
			}
			t := make([]time.Duration, reps)
			for i := range t {
				s := time.Now()
				if _, err := c.ExecContext(ctx, run); err != nil {
					return err
				}
				t[i] = time.Since(s)
			}
			slices.Sort(t)
			var v string
			if err := c.QueryRowContext(ctx, check).Scan(&v); err != nil {
				return fmt.Errorf("check: %w", err)
			}
			fmt.Printf("%s\t%d\t%s\n", name, t[reps/2].Nanoseconds(), v)
			return nil
		}(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func threads(a []string) error {
	mode, syncMode, cache, path := a[0], a[3], a[4], a[6]
	n, err1 := strconv.Atoi(a[1])
	secs, err2 := strconv.Atoi(a[2])
	if err1 != nil || err2 != nil || n < 1 || secs < 1 || (a[5] != "0" && a[5] != "1") {
		return fmt.Errorf("bad arguments %q", a)
	}
	pin := a[5] == "1"
	nr, nw := n, n
	switch mode {
	case "read":
		nw = 0
	case "write":
		nr = 0
	case "mixed":
	default:
		return fmt.Errorf("MODE %q", mode)
	}
	for _, s := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + s); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(" + syncMode + ")&_pragma=busy_timeout(5000)"
	if cache != "0" {
		dsn += "&_pragma=cache_size(" + cache + ")"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT); CREATE TABLE w(id INTEGER PRIMARY KEY, v TEXT);
		WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM r WHERE i < 100000) INSERT INTO t SELECT i, printf('%.100c', 'v') FROM r`); err != nil {
		return err
	}
	db.SetMaxOpenConns(nr + nw)
	db.SetMaxIdleConns(nr + nw)
	const query = `SELECT v FROM t WHERE id = ?`
	shared, err := db.Prepare(query)
	if err != nil {
		return err
	}
	defer shared.Close()
	ctx := context.Background()
	payload := strings.Repeat("x", 100)
	var stop atomic.Bool
	var reads, writes, errs atomic.Int64
	var ready, wg sync.WaitGroup
	start := make(chan struct{})
	for i := range nr {
		ready.Add(1)
		wg.Go(func() {
			q := shared
			if pin {
				c, err := db.Conn(ctx)
				if err == nil {
					defer c.Close()
					q, err = c.PrepareContext(ctx, query)
				}
				if err != nil {
					errs.Add(1)
					ready.Done()
					return
				}
				defer q.Close()
			}
			ready.Done()
			<-start
			r := rand.New(rand.NewPCG(uint64(i), 1))
			var v []byte
			var k int64
			for !stop.Load() {
				if err := q.QueryRow(r.Int64N(100000) + 1).Scan(&v); err != nil || len(v) != 100 {
					errs.Add(1)
					continue
				}
				k++
			}
			reads.Add(k)
		})
	}
	for range nw {
		ready.Add(1)
		wg.Go(func() {
			ready.Done()
			<-start
			var k int64
			for !stop.Load() {
				if _, err := db.Exec(`INSERT INTO w(v) VALUES (?)`, payload); err != nil {
					errs.Add(1)
					continue
				}
				k++
			}
			writes.Add(k)
		})
	}
	ready.Wait()
	close(start)
	time.Sleep(time.Duration(secs) * time.Second)
	stop.Store(true)
	wg.Wait()
	fmt.Printf("reads/s %.0f writes/s %.0f errors %d\n",
		float64(reads.Load())/float64(secs), float64(writes.Load())/float64(secs), errs.Load())
	return nil
}
