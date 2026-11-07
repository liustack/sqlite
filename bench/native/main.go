// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

// Command native is the Go side of the comparison with SQLite in C
// (benchrun native), built with the tag of a driver of bench (liustack or
// mattn). It does what c/engine.c and c/threads.c do through the C API, and
// prints the same lines:
//
//	native engine FILE REPS
//	native threads MODE N SECS SYNC CACHE RAW PATH
//
// Each reader has a connection of its own, as each thread of c/threads.c
// does. RAW 1 has it call the driver itself, as the C calls SQLite: it
// prepares the query once and runs it again and again. RAW 0 has it go
// through database/sql as a program would: a *sql.Conn of its own and
// QueryRowContext, with no *sql.Stmt, whose every query takes the mutex of
// the *sql.DB.
package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/liustack/sqlite/bench"
)

func main() {
	var err error
	switch {
	case len(os.Args) == 4 && os.Args[1] == "engine":
		err = engine(os.Args[2], os.Args[3])
	case len(os.Args) == 9 && os.Args[1] == "threads":
		err = threads(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "usage: native engine FILE REPS | native threads MODE N SECS SYNC CACHE RAW PATH")
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
			db, err := sql.Open(bench.DriverName, ":memory:")
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
	raw := a[5] == "1"
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
	db, err := sql.Open(bench.DriverName, bench.DSN(path, syncMode))
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT); CREATE TABLE w(id INTEGER PRIMARY KEY, v TEXT);
		WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM r WHERE i < 100000) INSERT INTO t SELECT i, printf('%.100c', 'v') FROM r`); err != nil {
		return err
	}
	db.SetMaxOpenConns(nr + nw + 1)
	db.SetMaxIdleConns(nr + nw + 1)
	ctx := context.Background()
	payload := strings.Repeat("x", 100)
	var stop atomic.Bool
	var reads, writes, errs atomic.Int64
	var ready, wg sync.WaitGroup
	start := make(chan struct{})
	for i := range nr {
		ready.Add(1)
		wg.Go(func() {
			read, done, err := reader(ctx, db, cache, raw)
			ready.Done()
			if err != nil {
				fmt.Fprintln(os.Stderr, "native: reader:", err)
				errs.Add(1)
				return
			}
			defer done()
			<-start
			r := rand.New(rand.NewPCG(uint64(i), 1))
			var k int64
			for !stop.Load() {
				if read(r.Int64N(100000)+1) != 100 {
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

const query = `SELECT v FROM t WHERE id = ?`

// reader gives a connection of its own to a reader, with cache_size set as
// c/threads.c sets it, and read, which reads the row id and says the length
// of its value (-1 for an error). done gives the connection back.
func reader(ctx context.Context, db *sql.DB, cache string, raw bool) (read func(id int64) int, done func(), err error) {
	c, err := db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	if cache != "0" {
		if _, err := c.ExecContext(ctx, "PRAGMA cache_size="+cache); err != nil {
			c.Close()
			return nil, nil, err
		}
	}
	if !raw {
		var v []byte
		return func(id int64) int {
			if err := c.QueryRowContext(ctx, query, id).Scan(&v); err != nil {
				return -1
			}
			return len(v)
		}, func() { c.Close() }, nil
	}
	// The driver's own statement, prepared once, as c/threads.c prepares its
	// sqlite3_stmt: database/sql only lends the connection.
	var s driver.Stmt
	err = c.Raw(func(dc any) error {
		var err error
		s, err = dc.(driver.ConnPrepareContext).PrepareContext(ctx, query)
		return err
	})
	if err != nil {
		c.Close()
		return nil, nil, err
	}
	q := s.(driver.StmtQueryContext)
	args := []driver.NamedValue{{Ordinal: 1}}
	dest := make([]driver.Value, 1)
	return func(id int64) int {
			args[0].Value = id
			rows, err := q.QueryContext(ctx, args)
			if err != nil {
				return -1
			}
			n := -1
			if rows.Next(dest) == nil {
				switch v := dest[0].(type) {
				case string:
					n = len(v)
				case []byte:
					n = len(v)
				}
			}
			rows.Close()
			return n
		}, func() {
			s.Close()
			c.Close()
		}, nil
}
