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
	"time"

	"golang.org/x/sys/unix"
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
// good sync, and stops every File of it. It opens again once all are
// closed.
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
	if !WALsStopped() || !WALStopped(strings.TrimSuffix(name, "-wal")) {
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
	if _, rc := openWAL(t, name); rc != IOErr {
		t.Fatalf("Open while a File has the WAL open = %d", rc)
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
	if WALsStopped() {
		t.Fatal("the WAL is still stopped once open again")
	}
	if rc := c.ReadAt(p, 0); rc != OK || p[0] != 1 {
		t.Fatalf("ReadAt = %d, %v", rc, p)
	}
}

// stopWAL opens the WAL name, writes to it and fails its sync, with the
// cut failing too when cutFails. It returns the File, still open.
func stopWAL(t *testing.T, name string, cutFails bool) *File {
	t.Helper()
	f, rc := openWAL(t, name)
	if rc != OK {
		t.Fatalf("Open = %d", rc)
	}
	f.WriteAt(make([]byte, 100), 0)
	SetFault(func(path string, op Op) int {
		switch {
		case op == OpSync:
			return IOErrFsync
		case op == OpTruncate && cutFails:
			return IOErrTruncate
		}
		return OK
	})
	defer SetFault(nil)
	if rc := f.Sync(SyncNormal); rc != IOErrFsync {
		t.Fatalf("Sync = %d", rc)
	}
	return f
}

// A write that comes while a sync runs waits for it, and is not taken as
// synced by it.
func TestWALWriteDuringSync(t *testing.T) {
	name := filepath.Join(t.TempDir(), "test.db-wal")
	f, rc := openWAL(t, name)
	if rc != OK {
		t.Fatalf("Open = %d", rc)
	}
	defer f.Close()
	f.WriteAt(make([]byte, 100), 0)
	wrote := make(chan struct{})
	// The fault runs inside Sync, after the WAL took what it syncs.
	SetFault(func(path string, op Op) int {
		if op == OpSync {
			go func() {
				f.WriteAt(make([]byte, 50), 100)
				close(wrote)
			}()
			select {
			case <-wrote:
				t.Error("a write landed while a sync ran")
			case <-time.After(20 * time.Millisecond):
			}
		}
		return OK
	})
	rc = f.Sync(SyncNormal)
	SetFault(nil)
	if rc != OK {
		t.Fatalf("Sync = %d", rc)
	}
	<-wrote
	if d := f.wal.dirty.Load(); d != 100 {
		t.Fatalf("dirty from %d, want 100", d)
	}
}

// A WAL whose cut failed does not open again, until it is deleted: a new
// WAL of that name opens.
func TestWALCutFails(t *testing.T) {
	name := filepath.Join(t.TempDir(), "test.db-wal")
	stopWAL(t, name, true).Close()
	if _, rc := openWAL(t, name); rc != IOErr {
		t.Fatalf("Open = %d", rc)
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	f, rc := openWAL(t, name)
	if rc != OK {
		t.Fatalf("Open of a new WAL = %d", rc)
	}
	f.Close()
	if WALsStopped() {
		t.Fatal("the deleted WAL is still stopped")
	}
}

// A stopped WAL does not open while another process has the -shm open: the
// WAL index there still counts the frames cut.
func TestWALShmInUse(t *testing.T) {
	name := filepath.Join(t.TempDir(), "test.db-wal")
	shm := strings.TrimSuffix(name, "-wal") + "-shm"
	if err := os.WriteFile(shm, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	// Another open file description holds the DMS lock, as another
	// process would.
	other, err := os.OpenFile(shm, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var fl unix.Flock_t
	if err := setLock(int(other.Fd()), &fl, unix.F_RDLCK, shmDMS, 1); err != nil {
		t.Fatal(err)
	}
	stopWAL(t, name, false).Close()
	if _, rc := openWAL(t, name); rc != IOErr {
		t.Fatalf("Open while the -shm is in use = %d", rc)
	}
	if err := setLock(int(other.Fd()), &fl, unix.F_UNLCK, shmDMS, 1); err != nil {
		t.Fatal(err)
	}
	f, rc := openWAL(t, name)
	if rc != OK {
		t.Fatalf("Open once the -shm is free = %d", rc)
	}
	f.Close()
}
