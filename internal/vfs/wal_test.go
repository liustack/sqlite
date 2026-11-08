// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package vfs

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openWAL(t *testing.T, name string) (*File, int) {
	t.Helper()
	// A WAL takes the mode of its database, which must be there.
	if err := os.WriteFile(strings.TrimSuffix(name, "-wal"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f, _, rc := Open(name, OpenWAL|OpenReadWrite|OpenCreate)
	return f, rc
}

// A failed sync of a WAL cuts it at the first byte written since its last
// good sync, and stops every File of it until all are closed.
func TestWALStops(t *testing.T) {
	name := filepath.Join(t.TempDir(), "test.db-wal")
	a, rc := openWAL(t, name)
	if rc != OK {
		t.Fatalf("Open = %d", rc)
	}
	b, rc := openWAL(t, name)
	if rc != OK {
		t.Fatalf("Open = %d", rc)
	}
	if rc := a.WriteAt(bytes.Repeat([]byte{1}, 100), 0); rc != OK {
		t.Fatalf("WriteAt = %d", rc)
	}
	if rc := a.Sync(SyncNormal); rc != OK {
		t.Fatalf("Sync = %d", rc)
	}
	// Written after the good sync, the second through another File.
	a.WriteAt(bytes.Repeat([]byte{2}, 50), 150)
	b.WriteAt(bytes.Repeat([]byte{3}, 50), 120)

	SetFault(func(path string, op Op) int {
		if op == OpSync {
			return IOErrFsync
		}
		return OK
	})
	rc = a.Sync(SyncNormal)
	SetFault(nil)
	if rc != IOErrFsync {
		t.Fatalf("Sync = %d", rc)
	}
	if !WALsStopped() {
		t.Fatal("the WAL is not stopped")
	}
	if size, _ := b.Size(); size != 120 {
		t.Fatalf("size %d, want the WAL cut at 120", size)
	}
	p := make([]byte, 10)
	for _, f := range []*File{a, b} {
		if rc := f.ReadAt(p, 0); rc != IOErrRead {
			t.Errorf("ReadAt = %d", rc)
		}
		if rc := f.WriteAt(p, 0); rc != IOErrWrite {
			t.Errorf("WriteAt = %d", rc)
		}
		if rc := f.Truncate(0); rc != IOErrTruncate {
			t.Errorf("Truncate = %d", rc)
		}
		if rc := f.Sync(SyncNormal); rc != IOErrFsync {
			t.Errorf("Sync = %d", rc)
		}
	}
	if _, rc := openWAL(t, name); rc != IOErr {
		t.Fatalf("Open of the stopped WAL = %d", rc)
	}
	a.Close()
	if !WALsStopped() {
		t.Fatal("the WAL opens again while a File has it open")
	}
	b.Close()
	if WALsStopped() {
		t.Fatal("the WAL is still stopped with no File open")
	}
	c, rc := openWAL(t, name)
	if rc != OK {
		t.Fatalf("Open once all were closed = %d", rc)
	}
	defer c.Close()
	if rc := c.ReadAt(p, 0); rc != OK || p[0] != 1 {
		t.Fatalf("ReadAt = %d, %v", rc, p)
	}
}

// A write made while a sync runs is not taken as synced by it.
func TestWALWriteDuringSync(t *testing.T) {
	name := filepath.Join(t.TempDir(), "test.db-wal")
	f, rc := openWAL(t, name)
	if rc != OK {
		t.Fatalf("Open = %d", rc)
	}
	defer f.Close()
	f.WriteAt(make([]byte, 100), 0)
	// The fault runs inside Sync, after the WAL took what it syncs: the
	// write lands while the sync runs.
	SetFault(func(path string, op Op) int {
		if op == OpSync {
			SetFault(nil)
			f.WriteAt(make([]byte, 50), 100)
		}
		return OK
	})
	defer SetFault(nil)
	if rc := f.Sync(SyncNormal); rc != OK {
		t.Fatalf("Sync = %d", rc)
	}
	if d := f.wal.dirty.Load(); d != 100 {
		t.Fatalf("dirty from %d, want 100", d)
	}
}
