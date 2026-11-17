// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package csqlite

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// Once the automatic checkpoints of a connection are taken, SQLite runs
// none, PRAGMA wal_autocheckpoint still reads what the program set, and a
// PRAGMA that sets it gives them back to SQLite with the new value.
func TestTakeAutoCheckpoint(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	c := open(t, name)
	exec(t, c, "PRAGMA journal_mode=WAL; CREATE TABLE t(x)")
	read := func(want int) {
		t.Helper()
		if got := mustOne(t, c, "PRAGMA wal_autocheckpoint"); got != strconv.Itoa(want) {
			t.Fatalf("PRAGMA wal_autocheckpoint reads %s, want %d", got, want)
		}
	}
	frames := func() int64 {
		t.Helper()
		st, err := os.Stat(name + "-wal")
		if err != nil {
			t.Fatal(err)
		}
		return (st.Size() - 32) / 4120
	}
	write := func(n int) {
		t.Helper()
		for range n {
			exec(t, c, "INSERT INTO t VALUES (randomblob(3000))")
		}
	}
	read(1000)
	if n, ok := c.TakeAutoCheckpoint(); !ok || n != 1000 {
		t.Fatalf("took %d (%v), want the 1000 of SQLite", n, ok)
	}
	if n, ok := c.TakeAutoCheckpoint(); ok {
		t.Fatalf("took %d a second time", n)
	}
	read(1000)
	// A statement kept and run again reads the same: SQLite prepares it
	// again each time it runs.
	kept, _, err := c.Prepare("PRAGMA wal_autocheckpoint")
	if err != nil {
		t.Fatal(err)
	}
	defer kept.Finalize()
	keptReads := func(want int64) {
		t.Helper()
		for range 2 {
			if row, err := kept.Step(); err != nil || !row || kept.ColumnInt64(0) != want {
				t.Fatalf("the kept statement reads %d (%v, %v), want %d", kept.ColumnInt64(0), row, err, want)
			}
			kept.Reset()
		}
	}
	keptReads(1000)
	// Reading it gives nothing back: SQLite still runs none.
	if n, ok := c.TakeAutoCheckpoint(); ok {
		t.Fatalf("took %d after a read", n)
	}
	exec(t, c, "PRAGMA wal_autocheckpoint=5")
	if n, ok := c.TakeAutoCheckpoint(); !ok || n != 5 {
		t.Fatalf("took %d (%v) after PRAGMA wal_autocheckpoint=5", n, ok)
	}
	read(5)
	keptReads(5)
	write(40)
	if n := frames(); n < 40 {
		t.Fatalf("the WAL has %d frames after 40 writes: SQLite ran a checkpoint", n)
	}
	// The largest value SQLite takes is one as any other, and so is 0.
	for _, v := range []int{2147483647, 0} {
		exec(t, c, "PRAGMA wal_autocheckpoint="+strconv.Itoa(v))
		if n, ok := c.TakeAutoCheckpoint(); !ok || n != v {
			t.Fatalf("took %d (%v) after PRAGMA wal_autocheckpoint=%d", n, ok, v)
		}
		read(v)
		keptReads(int64(v))
	}
	// Given back, SQLite runs them with the value they were taken with.
	exec(t, c, "PRAGMA wal_autocheckpoint=5")
	c.TakeAutoCheckpoint()
	if !c.GiveAutoCheckpoint() || c.GiveAutoCheckpoint() {
		t.Fatal("GiveAutoCheckpoint: want true once, then false")
	}
	read(5)
	before := frames()
	write(40)
	if n := frames(); n > before+10 {
		t.Fatalf("the WAL went from %d to %d frames in 40 writes: SQLite ran no checkpoint", before, n)
	}
}

func TestAttached(t *testing.T) {
	c := open(t, filepath.Join(t.TempDir(), "x.db"))
	exec(t, c, "CREATE TABLE t(x); CREATE TEMP TABLE tt(x)")
	if c.Attached() {
		t.Fatal("attached with main and temp alone")
	}
	exec(t, c, "ATTACH ':memory:' AS a")
	if !c.Attached() {
		t.Fatal("not attached after ATTACH")
	}
	exec(t, c, "DETACH a")
	if c.Attached() {
		t.Fatal("attached after DETACH")
	}
}

// BusyTimeoutNow reads what PRAGMA busy_timeout reads, with no statement.
func TestBusyTimeoutNow(t *testing.T) {
	c := open(t, filepath.Join(t.TempDir(), "x.db"))
	for _, ms := range []int{123, 0, 5000} {
		exec(t, c, "PRAGMA busy_timeout="+strconv.Itoa(ms))
		if got, ok := c.BusyTimeoutNow(); !ok || got != ms {
			t.Fatalf("BusyTimeoutNow is %d (%v), want %d", got, ok, ms)
		}
		if got := mustOne(t, c, "PRAGMA busy_timeout"); got != strconv.Itoa(ms) {
			t.Fatalf("PRAGMA busy_timeout reads %s, want %d", got, ms)
		}
	}
	if err := c.BusyTimeout(77); err != nil {
		t.Fatal(err)
	}
	if got, ok := c.BusyTimeoutNow(); !ok || got != 77 {
		t.Fatalf("BusyTimeoutNow is %d (%v) after BusyTimeout(77)", got, ok)
	}
}
