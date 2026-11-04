// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package vfs

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// failedExclusiveFromShared is the lock left when SHARED -> EXCLUSIVE
// fails: os_unix goes back to SHARED.
const failedExclusiveFromShared = LockShared

// dbHeldBy tells which lock another open file description of the database
// sees on n bytes from start: F_UNLCK, F_RDLCK or F_WRLCK. It stands for
// another process.
func dbHeldBy(t *testing.T, name string, start, n int64) int16 {
	t.Helper()
	fd, err := unix.Open(name, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	var fl unix.Flock_t
	typ, err := testLock(fd, &fl, start, n)
	if err != nil {
		t.Fatal(err)
	}
	return typ
}

func TestLockInProcessNoFcntl(t *testing.T) {
	a, b, name := twoFiles(t)
	c := openT(t, name, mainDB)
	calls := countFcntl(t)

	lockT(t, "a SHARED", a.Lock(LockShared), OK)
	first := calls.Load()
	lockT(t, "b SHARED", b.Lock(LockShared), OK)
	if calls.Load() != first {
		t.Errorf("second SHARED made %d fcntl calls, want 0", calls.Load()-first)
	}

	lockT(t, "a RESERVED", a.Lock(LockReserved), OK)
	calls.Reset()
	// Conflicts with another File of the process are found without the
	// kernel.
	lockT(t, "b RESERVED", b.Lock(LockReserved), Busy)
	lockT(t, "b EXCLUSIVE", b.Lock(LockExclusive), Busy)
	if r, rc := b.CheckReservedLock(); rc != OK || !r {
		t.Errorf("b CheckReservedLock = %v, %d, want true", r, rc)
	}
	if calls.Load() != 0 {
		t.Errorf("in-process conflicts made %d fcntl calls, want 0", calls.Load())
	}

	// a reaches PENDING, then b's SHARED keeps it from EXCLUSIVE.
	lockT(t, "a EXCLUSIVE", a.Lock(LockExclusive), Busy)
	if a.LockState() != LockPending {
		t.Errorf("a LockState = %d, want PENDING", a.LockState())
	}
	calls.Reset()
	lockT(t, "c SHARED under PENDING", c.Lock(LockShared), Busy)
	lockT(t, "b unlock", b.Unlock(LockNone), OK)
	if calls.Load() != 0 {
		t.Errorf("refused SHARED and a shared unlock made %d fcntl calls, want 0", calls.Load())
	}
	lockT(t, "a EXCLUSIVE", a.Lock(LockExclusive), OK)
	calls.Reset()
	lockT(t, "b SHARED under EXCLUSIVE", b.Lock(LockShared), Busy)
	if calls.Load() != 0 {
		t.Errorf("refused SHARED made %d fcntl calls, want 0", calls.Load())
	}
}

func TestLockSeenFromOutside(t *testing.T) {
	a, b, name := twoFiles(t)
	lockT(t, "a SHARED", a.Lock(LockShared), OK)
	lockT(t, "b SHARED", b.Lock(LockShared), OK)
	lockT(t, "a unlock", a.Unlock(LockNone), OK)
	// b still reads: the process keeps its read lock.
	if got := dbHeldBy(t, name, sharedFirst, sharedSize); got != unix.F_RDLCK {
		t.Errorf("shared range seen from outside = %d, want F_RDLCK", got)
	}
	lockT(t, "b RESERVED", b.Lock(LockReserved), OK)
	if got := dbHeldBy(t, name, reservedByte, 1); got != unix.F_WRLCK {
		t.Errorf("reserved byte seen from outside = %d, want F_WRLCK", got)
	}
	lockT(t, "b unlock", b.Unlock(LockNone), OK)
	if got := dbHeldBy(t, name, 0, 0); got != unix.F_UNLCK {
		t.Errorf("file seen from outside = %d, want F_UNLCK", got)
	}
}

func TestCloseKeepsOtherFilesLocks(t *testing.T) {
	// The first File of the inode is closed while it reads and the second
	// writes. The second keeps its locks, as with os_unix's pending close.
	a, b, name := twoFiles(t)
	lockT(t, "a SHARED", a.Lock(LockShared), OK)
	lockT(t, "b SHARED", b.Lock(LockShared), OK)
	lockT(t, "b RESERVED", b.Lock(LockReserved), OK)
	if rc := a.Close(); rc != OK {
		t.Fatal(rc)
	}
	if got := dbHeldBy(t, name, reservedByte, 1); got != unix.F_WRLCK {
		t.Errorf("reserved byte after close = %d, want F_WRLCK", got)
	}
	if got := dbHeldBy(t, name, sharedFirst, sharedSize); got != unix.F_RDLCK {
		t.Errorf("shared range after close = %d, want F_RDLCK", got)
	}
	// a's share is gone: b is alone and may write.
	lockT(t, "b EXCLUSIVE", b.Lock(LockExclusive), OK)
	if got := dbHeldBy(t, name, sharedFirst, sharedSize); got != unix.F_WRLCK {
		t.Errorf("shared range = %d, want F_WRLCK", got)
	}
}

func TestLockReadOnlyAndReadWrite(t *testing.T) {
	// A read-only connection opened first must not keep a read-write one
	// of the process from writing.
	name := filepath.Join(t.TempDir(), "rorw.db")
	openT(t, name, mainDB).WriteAt([]byte("x"), 0)
	ro := openT(t, name, readOnlyD)
	rw := openT(t, name, mainDB)
	lockT(t, "ro SHARED", ro.Lock(LockShared), OK)
	lockT(t, "ro RESERVED", ro.Lock(LockReserved), IOErrLock)
	lockT(t, "rw SHARED", rw.Lock(LockShared), OK)
	lockT(t, "rw RESERVED", rw.Lock(LockReserved), OK)
	lockT(t, "ro unlock", ro.Unlock(LockNone), OK)
	lockT(t, "rw EXCLUSIVE", rw.Lock(LockExclusive), OK)
	if got := dbHeldBy(t, name, sharedFirst, sharedSize); got != unix.F_WRLCK {
		t.Errorf("shared range = %d, want F_WRLCK", got)
	}
}

func TestLockNodePerInode(t *testing.T) {
	// A File of another file at the same name, after a rename, is another
	// inode and conflicts through the kernel.
	a, _, name := twoFiles(t)
	lockT(t, "a SHARED", a.Lock(LockShared), OK)
	lockT(t, "a RESERVED", a.Lock(LockReserved), OK)
	if err := os.Rename(name, name+"-old"); err != nil {
		t.Fatal(err)
	}
	c := openT(t, name, mainDB)
	lockT(t, "c SHARED", c.Lock(LockShared), OK)
	lockT(t, "c RESERVED", c.Lock(LockReserved), OK)
	if !a.HasMoved() || c.HasMoved() {
		t.Errorf("HasMoved = %v, %v", a.HasMoved(), c.HasMoved())
	}
}

func TestLockReadOnlyFirstThenWritable(t *testing.T) {
	if isRoot() {
		t.Skip("root can write a read-only file")
	}
	// The first File could only read the file, so the inode's descriptor
	// cannot take write locks. A read-write File that comes while no lock
	// is held gives the inode its own.
	name := filepath.Join(t.TempDir(), "rofirst.db")
	if err := os.WriteFile(name, []byte("x"), 0444); err != nil {
		t.Fatal(err)
	}
	ro := openT(t, name, readOnlyD)
	if !ro.inode.readOnly {
		t.Fatal("inode of a file that cannot be written has a read-write descriptor")
	}
	if err := os.Chmod(name, 0644); err != nil {
		t.Fatal(err)
	}
	rw := openT(t, name, mainDB)
	if rw.inode != ro.inode || rw.inode.readOnly {
		t.Fatal("read-write File did not give the inode a writable descriptor")
	}
	lockT(t, "ro SHARED", ro.Lock(LockShared), OK)
	lockT(t, "rw SHARED", rw.Lock(LockShared), OK)
	lockT(t, "rw RESERVED", rw.Lock(LockReserved), OK)
	lockT(t, "ro unlock", ro.Unlock(LockNone), OK)
	lockT(t, "rw EXCLUSIVE", rw.Lock(LockExclusive), OK)
}

func TestLockReadOnlyHoldsSharedThenWritable(t *testing.T) {
	if isRoot() {
		t.Skip("root can write a read-only file")
	}
	// The read-only File holds SHARED, as a WAL connection always does,
	// when a read-write File of the database comes. The read-write File
	// must still be able to write.
	name := filepath.Join(t.TempDir(), "roshared.db")
	if err := os.WriteFile(name, []byte("x"), 0444); err != nil {
		t.Fatal(err)
	}
	ro := openT(t, name, readOnlyD)
	lockT(t, "ro SHARED", ro.Lock(LockShared), OK)
	if err := os.Chmod(name, 0644); err != nil {
		t.Fatal(err)
	}
	rw := openT(t, name, mainDB)
	// The process still reads: no other process can write.
	if got := dbHeldBy(t, name, sharedFirst, sharedSize); got != unix.F_RDLCK {
		t.Errorf("shared range = %d, want F_RDLCK", got)
	}
	lockT(t, "ro RESERVED", ro.Lock(LockReserved), IOErrLock)
	lockT(t, "rw SHARED", rw.Lock(LockShared), OK)
	lockT(t, "rw RESERVED", rw.Lock(LockReserved), OK)
	if got := dbHeldBy(t, name, reservedByte, 1); got != unix.F_WRLCK {
		t.Errorf("reserved byte = %d, want F_WRLCK", got)
	}
	// No read lock is left behind on the old descriptor: once ro goes,
	// rw gets the whole file.
	lockT(t, "ro unlock", ro.Unlock(LockNone), OK)
	lockT(t, "rw EXCLUSIVE", rw.Lock(LockExclusive), OK)
	if got := dbHeldBy(t, name, sharedFirst, sharedSize); got != unix.F_WRLCK {
		t.Errorf("shared range = %d, want F_WRLCK", got)
	}
}
