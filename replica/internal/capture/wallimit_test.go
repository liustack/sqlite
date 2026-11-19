// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package capture

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/liustack/sqlite/internal/replhook"
	"github.com/superfly/ltx"
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

// The checkpoint that waits for the readers comes with the commit that takes
// the WAL past the limit, not at the next CheckpointFrames frames. After a
// wait in vain, the writers leave the WAL alone until it has grown by a
// quarter of the limit or by CheckpointFrames.
func TestWALLimitAtCommit(t *testing.T) {
	const most = 1048576 / (4096 + 24)
	name := filepath.Join(t.TempDir(), "x.db")
	opt := testOptions(t)
	opt.CheckpointFrames = 100
	c := open(t, name, opt)
	db, err := sql.Open("sqlite", "file:"+name+"?_pragma=synchronous(NORMAL)&_wal_limit=1048576")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	run(t, db, "CREATE TABLE t(x)", "INSERT INTO t VALUES (0)")
	frames := func() int64 {
		st, err := os.Stat(name + "-wal")
		if err != nil {
			t.Fatal(err)
		}
		return max(st.Size()-32, 0) / (4096 + 24)
	}
	staged := func() ltx.TXID {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.txid
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil {
		t.Fatal(err)
	}
	for c.walTries.Load() == 0 {
		if n := frames(); n > most+10 {
			t.Fatalf("%d frames in the WAL and no wait for the reader, the limit is %d", n, most)
		}
		run(t, db, "INSERT INTO t VALUES (1)")
	}
	at := frames()
	if at < most {
		t.Fatalf("a wait for the reader at %d frames, the limit is %d", at, most)
	}
	// The next wait comes most/4 = 63 frames later, the next checkpoint
	// without one 100 frames later. A step in between changes neither.
	step(t, c)
	first := staged()
	for frames() < at+55 {
		run(t, db, "INSERT INTO t VALUES (2)")
	}
	if n := staged() - first; n != 0 || c.walTries.Load() != 1 {
		t.Fatalf("%d files staged and %d waits in %d frames after a wait in vain at %d", n, c.walTries.Load()-1, frames()-at, at)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	step(t, c)
	if n := c.FullCopies(); n != 0 {
		t.Fatalf("%d full copies: the WAL was lost", n)
	}
	sameAs(t, c, name)
}

// A limit under CheckpointFrames frames holds too: the writer that takes the
// WAL past it stages and checkpoints, and the WAL starts over.
func TestWALLimitUnderCheckpointFrames(t *testing.T) {
	const most = 1048576 / (4096 + 24)
	name := filepath.Join(t.TempDir(), "x.db")
	c := open(t, name, testOptions(t))
	db, err := sql.Open("sqlite", "file:"+name+"?_pragma=synchronous(NORMAL)&_wal_limit=1048576")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	run(t, db, "CREATE TABLE t(x)")
	for range 3 * most {
		run(t, db, "INSERT INTO t VALUES (1)")
	}
	st, err := os.Stat(name + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if n := (st.Size() - 32) / (4096 + 24); n > most+10 {
		t.Fatalf("%d frames in the WAL, the limit is %d", n, most)
	}
	if tries, restarts := c.walTries.Load(), c.walRestarts.Load(); restarts < 2 || tries != restarts {
		t.Fatalf("%d checkpoints at the limit started the WAL over %d times", tries, restarts)
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

// A connection that writes only its temporary tables gives the write lock
// back too, and has not looked at the WAL for a while. The capturer is told
// the WAL as it is, not as that connection knows it: no wait at each commit.
func TestWALLimitStagingStoppedTempWriter(t *testing.T) {
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
	// A write is about 100 KiB of frames, 25 of them.
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
	write(2)
	// It reads the WAL once, of some 50 frames, and then writes its own
	// table alone.
	ctx := context.Background()
	temp, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer temp.Close()
	if err := temp.QueryRowContext(ctx, "SELECT count(*) FROM t").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if _, err := temp.ExecContext(ctx, "CREATE TEMP TABLE tt(x)"); err != nil {
		t.Fatal(err)
	}
	tries0 := c.walTries.Load()
	for k := 0; c.walTries.Load() == tries0; k++ {
		if k > 30 {
			t.Fatal("no try to start the WAL over past the limit")
		}
		write(1)
	}
	// The next try comes a quarter of the limit later: 63 frames.
	for range 2 {
		if _, err := temp.ExecContext(ctx, "INSERT INTO tt VALUES (1)"); err != nil {
			t.Fatal(err)
		}
		write(1)
	}
	if tries := c.walTries.Load() - tries0; tries != 1 {
		t.Fatalf("%d tries in 2 writes after the first, beside a connection that writes its temporary table, want 1", tries)
	}
}
