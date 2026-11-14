// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package capture

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/liustack/sqlite/internal/replhook"
)

// While a reader holds frames of the WAL, a PASSIVE checkpoint copies what
// it can but the WAL never starts over. Past the _wal_limit of the writer's
// connection, the checkpoint the capturer runs for it waits for the readers:
// not at each commit, but once in each quarter of the limit the WAL grows.
// When the reader ends, the WAL starts over. Nothing is lost on the way.
func TestWALLimit(t *testing.T) {
	const limit = 1 << 20
	name := filepath.Join(t.TempDir(), "x.db")
	opt := testOptions(t)
	opt.CheckpointFrames = 20
	c := open(t, name, opt)
	db, err := sql.Open("sqlite", "file:"+name+"?_pragma=synchronous(NORMAL)&_wal_limit=1048576")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	run(t, db, "CREATE TABLE t(k INTEGER PRIMARY KEY, v BLOB)")
	// A write is about 100 KiB of frames.
	write := func(n int) {
		t.Helper()
		for k := range n {
			if _, err := db.Exec("INSERT OR REPLACE INTO t VALUES (?, randomblob(100000))", k%8); err != nil {
				t.Fatal(err)
			}
		}
	}
	counts := func() (tries, restarts int64) { return c.walTries.Load(), c.walRestarts.Load() }
	// The capturer waits for readers at those checkpoints alone.
	timeout := func() {
		t.Helper()
		c.mu.Lock()
		defer c.mu.Unlock()
		if ms, err := pragma(c.conn, "busy_timeout"); err != nil || ms != "0" {
			t.Fatalf("the busy timeout of the capturer is %s after a try (%v)", ms, err)
		}
	}
	pin := func() *sql.Tx {
		t.Helper()
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := tx.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil || n != 8 {
			tx.Rollback()
			t.Fatalf("%d rows, %v", n, err)
		}
		return tx
	}
	write(8)
	step(t, c)
	// The reader's snapshot is of a WAL that is not empty.
	write(1)
	reader := pin()
	defer reader.Rollback()
	tries0, restarts0 := counts()
	// 2 MiB of frames: a wait at 1 MiB and then at each quarter more.
	start := time.Now()
	write(21)
	tries, restarts := counts()
	tries, restarts = tries-tries0, restarts-restarts0
	if tries < 3 || tries > 7 || restarts != 0 {
		t.Fatalf("%d tries and %d restarts while a reader holds the WAL, want 3 to 7 tries and no restart", tries, restarts)
	}
	if d := time.Since(start); d > time.Duration(tries)*replhook.WALWait+5*time.Second {
		t.Fatalf("the writes took %v with %d tries", d, tries)
	}
	timeout()
	if err := reader.Rollback(); err != nil {
		t.Fatal(err)
	}
	write(4)
	if _, n := counts(); n-restarts0 != 1 {
		t.Fatalf("%d restarts once the reader ended, want 1", n-restarts0)
	}
	st, err := os.Stat(name + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	write(5)
	if st2, err := os.Stat(name + "-wal"); err != nil || st2.Size() != st.Size() {
		t.Fatalf("the WAL is %d bytes after %d, %v: it did not start over", st2.Size(), st.Size(), err)
	}
	timeout()
	// The WAL started over: the next waits come at the limit again, and not
	// where the last one that failed put them. A reader that ends during
	// the wait lets the WAL start over at that try.
	reader = pin()
	defer reader.Rollback()
	tries0, restarts0 = counts()
	ended, written := make(chan error, 1), make(chan struct{})
	go func() {
		for c.walTries.Load() == tries0 {
			select {
			case <-written:
				ended <- nil
				return
			case <-time.After(time.Millisecond):
			}
		}
		ended <- reader.Rollback()
	}()
	write(12)
	close(written)
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
	if tries, restarts := counts(); tries-tries0 != 1 || restarts-restarts0 != 1 {
		t.Fatalf("%d tries and %d restarts for a reader that ends during the wait, want one of each", tries-tries0, restarts-restarts0)
	}
	step(t, c)
	if n := c.FullCopies(); n != 0 {
		t.Fatalf("%d full copies: the WAL was lost", n)
	}
	sameAs(t, c, name)
}

// A connection whose _wal_limit is 0 has no limit: its checkpoints never
// wait for readers.
func TestWALLimitOff(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	opt := testOptions(t)
	opt.CheckpointFrames = 20
	c := open(t, name, opt)
	db, err := sql.Open("sqlite", "file:"+name+"?_pragma=synchronous(NORMAL)&_wal_limit=0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	run(t, db, "CREATE TABLE t(k INTEGER PRIMARY KEY, v BLOB)", "INSERT INTO t VALUES (0, randomblob(100000))")
	reader, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	var n int
	if err := reader.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d rows, %v", n, err)
	}
	for range 15 {
		run(t, db, "INSERT OR REPLACE INTO t VALUES (0, randomblob(100000))")
	}
	if tries := c.walTries.Load(); tries != 0 {
		t.Fatalf("%d tries with the limit off", tries)
	}
}

// Once staging stopped at its limit, the checkpoints that go on follow the
// limit of the WAL too.
func TestWALLimitStagingStopped(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	opt := testOptions(t)
	opt.CheckpointFrames = 20
	opt.Limit = 64 << 10
	c := open(t, name, opt)
	db, err := sql.Open("sqlite", "file:"+name+"?_pragma=synchronous(NORMAL)&_wal_limit=1048576")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	run(t, db, "CREATE TABLE t(k INTEGER PRIMARY KEY, v BLOB)")
	write := func(n int) {
		t.Helper()
		for k := range n {
			run(t, db, fmt.Sprintf("INSERT OR REPLACE INTO t VALUES (%d, randomblob(100000))", k%8))
		}
	}
	write(9)
	c.mu.Lock()
	full := c.full
	c.mu.Unlock()
	if !full {
		t.Fatal("staging goes on past its limit")
	}
	reader, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	var n int
	if err := reader.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil || n != 8 {
		t.Fatalf("%d rows, %v", n, err)
	}
	tries0, restarts0 := c.walTries.Load(), c.walRestarts.Load()
	ended, written := make(chan error, 1), make(chan struct{})
	go func() {
		for c.walTries.Load() == tries0 {
			select {
			case <-written:
				ended <- nil
				return
			case <-time.After(time.Millisecond):
			}
		}
		ended <- reader.Rollback()
	}()
	write(12)
	close(written)
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
	if tries, restarts := c.walTries.Load()-tries0, c.walRestarts.Load()-restarts0; tries != 1 || restarts != 1 {
		t.Fatalf("%d tries and %d restarts past the limit of the WAL, want one of each", tries, restarts)
	}
}
