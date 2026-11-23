// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package capture

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// A write transaction that keeps the driver's write lock holds off neither
// Sync nor the staging of what was committed before it, also when the WAL
// is long enough for a checkpoint, which needs that lock.
func TestStageBesideWriteTx(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	opt := testOptions(t)
	opt.CheckpointFrames = 8
	c := open(t, name, opt)
	db := openApp(t, name)
	run(t, db, "CREATE TABLE t(x)")
	// A reader keeps the WAL from starting over: each step finds it past
	// CheckpointFrames.
	ctx := context.Background()
	rd, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	rtx, err := rd.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer rtx.Rollback()
	var n int
	if err := rtx.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		run(t, db, "INSERT INTO t VALUES (randomblob(3000))")
	}
	step(t, c)
	run(t, db, "INSERT INTO t VALUES (1)")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO t VALUES (2)"); err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := c.Sync(wait); err != nil {
		t.Fatalf("Sync beside a write transaction: %v", err)
	}
	out, _ := rebuild(t, c.Dir())
	staged, err := sql.Open("sqlite", out)
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Close()
	if err := staged.QueryRow("SELECT count(*) FROM t WHERE x = 1").Scan(&n); err != nil || n != 1 {
		t.Fatalf("the commit before the write transaction is staged %d times (%v)", n, err)
	}
}

// Writers that keep the driver's write lock busy, beside steps that keep the
// capturer busy, still checkpoint the WAL: it stays near CheckpointFrames.
func TestWALBoundedBesideSteps(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	opt := testOptions(t)
	opt.Interval = time.Millisecond
	opt.CheckpointFrames = 32
	c := open(t, name, opt)
	db := openApp(t, name)
	run(t, db, "CREATE TABLE t(x)")
	deadline := time.Now().Add(time.Second)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() {
			for time.Now().Before(deadline) {
				if _, err := db.Exec("INSERT INTO t VALUES (randomblob(500))"); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	step(t, c)
	fi, err := os.Stat(name + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	frames := (fi.Size() - 32) / (4096 + 24)
	t.Logf("the WAL reached %d frames", frames)
	if frames > 4*int64(opt.CheckpointFrames) {
		t.Fatalf("the WAL reached %d frames, CheckpointFrames is %d", frames, opt.CheckpointFrames)
	}
	sameAs(t, c, name)
}

// A Snapshot that has the driver's write lock does not wait there for a
// long step: the writers would wait as long.
func TestSnapshotBesideLongStep(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	c := open(t, name, testOptions(t))
	db := openApp(t, name)
	run(t, db, "CREATE TABLE t(x)")
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO t VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	snapped := make(chan error, 1)
	go func() {
		_, _, err := c.Snapshot(context.Background())
		snapped <- err
	}()
	// The Snapshot waits for the write lock, then a step holds the capturer.
	time.Sleep(20 * time.Millisecond)
	c.mu.Lock()
	release := sync.OnceFunc(c.mu.Unlock)
	t.Cleanup(release)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	wrote := make(chan error, 1)
	go func() {
		_, err := db.Exec("INSERT INTO t VALUES (2)")
		wrote <- err
	}()
	select {
	case err := <-wrote:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("a writer waits for the step behind the Snapshot")
	}
	release()
	if err := <-snapped; err != nil {
		t.Fatal(err)
	}
}

// Writers whose WAL is long wait for a long step once, not at each commit.
func TestWritersWaitOnceForStep(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	opt := testOptions(t)
	opt.CheckpointFrames = 4
	c := open(t, name, opt)
	db := openApp(t, name)
	run(t, db, "CREATE TABLE t(x)")
	commits := func() time.Duration {
		start := time.Now()
		for range 30 {
			run(t, db, "INSERT INTO t VALUES (randomblob(3000))")
		}
		return time.Since(start)
	}
	// The commits alone: a sync of the WAL each, slow on some machines.
	alone := commits()
	c.mu.Lock()
	t.Cleanup(sync.OnceFunc(c.mu.Unlock))
	// A wait at each commit would add 30 stepWaits.
	if d := commits(); d-alone > 10*stepWait {
		t.Fatalf("30 commits beside a long step took %v, %v alone", d, alone)
	}
}

// A second Close returns once the first is done.
func TestCloseTwice(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	c := open(t, name, testOptions(t))
	db := openApp(t, name)
	run(t, db, "CREATE TABLE t(x)")
	wait := closeWait
	closeWait = 300 * time.Millisecond
	t.Cleanup(func() { closeWait = wait })
	// The write lock stays taken: the first Close waits closeWait for it.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO t VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	go c.Close()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	c.Close()
	if d := time.Since(start); d < 150*time.Millisecond {
		t.Fatalf("the second Close returned after %v, before the first", d)
	}
}
