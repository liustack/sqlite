// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package vfs

import (
	"os"
	"testing"
	"unsafe"
)

// region returns the mapped memory at addr as a byte slice. The memory is
// not Go memory, so turning the address back into a pointer is safe. It is
// done through a pointer to the uintptr so that vet does not flag it.
func region(addr uintptr) []byte {
	p := *(*unsafe.Pointer)(unsafe.Pointer(&addr))
	return unsafe.Slice((*byte)(p), shmRegionSize)
}

func shmMapT(t *testing.T, f *File, n int, extend bool) uintptr {
	t.Helper()
	addr, rc := f.ShmMap(n, shmRegionSize, extend)
	if rc != OK {
		t.Fatalf("ShmMap(%d, %v) = %d", n, extend, rc)
	}
	return addr
}

func TestShmMap(t *testing.T) {
	a, b, name := twoFiles(t)
	t.Cleanup(func() { a.ShmUnmap(false); b.ShmUnmap(false) })

	// Nothing there yet, and no extend: no memory, and no error.
	if addr := shmMapT(t, a, 0, false); addr != 0 {
		t.Errorf("ShmMap without extend = %#x, want 0", addr)
	}
	a0 := shmMapT(t, a, 0, true)
	a1 := shmMapT(t, a, 1, true)
	if a0 == 0 || a1 == 0 || a0 == a1 {
		t.Fatalf("ShmMap = %#x, %#x", a0, a1)
	}
	if fi, err := os.Stat(name + "-shm"); err != nil || fi.Size() != 2*shmRegionSize {
		t.Errorf("-shm file: %v, %v", fi, err)
	}
	// Asking again gives the same memory.
	if again := shmMapT(t, a, 0, false); again != a0 {
		t.Errorf("ShmMap again = %#x, want %#x", again, a0)
	}

	b1 := shmMapT(t, b, 1, false)
	b0 := shmMapT(t, b, 0, false)
	if b0 == 0 || b1 == 0 {
		t.Fatalf("b ShmMap = %#x, %#x", b0, b1)
	}
	// A region not made yet is not there for b either.
	if addr := shmMapT(t, b, 2, false); addr != 0 {
		t.Errorf("b ShmMap(2) = %#x, want 0", addr)
	}

	region(a0)[100] = 7
	region(a1)[shmRegionSize-1] = 8
	ShmBarrier()
	if region(b0)[100] != 7 || region(b1)[shmRegionSize-1] != 8 {
		t.Error("b does not see a's writes")
	}
	region(b0)[200] = 9
	ShmBarrier()
	if region(a0)[200] != 9 {
		t.Error("a does not see b's writes")
	}
}

func TestShmDeadManSwitch(t *testing.T) {
	a, b, name := twoFiles(t)
	region(shmMapT(t, a, 0, true))[100] = 42

	// The second opener finds a live -shm and keeps what is in it.
	b0 := shmMapT(t, b, 0, false)
	if b0 == 0 || region(b0)[100] != 42 {
		t.Fatalf("second opener lost the -shm content")
	}
	if rc := a.ShmUnmap(false); rc != OK {
		t.Fatal(rc)
	}
	if rc := b.ShmUnmap(false); rc != OK {
		t.Fatal(rc)
	}
	if _, err := os.Stat(name + "-shm"); err != nil {
		t.Fatalf("-shm deleted without delete: %v", err)
	}

	// With no one left, the next opener starts the -shm afresh.
	c := openT(t, name, mainDB)
	if addr := shmMapT(t, c, 0, false); addr != 0 {
		t.Errorf("first opener mapped stale memory at %#x", addr)
	}
	if c0 := shmMapT(t, c, 0, true); region(c0)[100] != 0 {
		t.Errorf("first opener sees stale content %d", region(c0)[100])
	}
	if rc := c.ShmUnmap(true); rc != OK {
		t.Fatal(rc)
	}
	if _, err := os.Stat(name + "-shm"); !os.IsNotExist(err) {
		t.Errorf("-shm still there after ShmUnmap(true): %v", err)
	}
	// Unmapping twice is harmless.
	if rc := c.ShmUnmap(false); rc != OK {
		t.Errorf("second ShmUnmap = %d", rc)
	}
}

func TestShmMode(t *testing.T) {
	a, _, name := twoFiles(t)
	if err := os.Chmod(name, 0600); err != nil {
		t.Fatal(err)
	}
	shmMapT(t, a, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false) })
	if got := modeOf(t, name+"-shm"); got != 0600 {
		t.Errorf("mode of -shm = %v, want 0600", got)
	}
}

func TestShmLock(t *testing.T) {
	a, b, _ := twoFiles(t)
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false); b.ShmUnmap(false) })

	const (
		lockShared = ShmLock | ShmShared
		lockExcl   = ShmLock | ShmExclusive
		unShared   = ShmUnlock | ShmShared
		unExcl     = ShmUnlock | ShmExclusive
	)
	lockT(t, "a shared 3", a.ShmLock(3, 1, lockShared), OK)
	lockT(t, "b shared 3", b.ShmLock(3, 1, lockShared), OK)
	lockT(t, "a shared 3 again", a.ShmLock(3, 1, lockShared), OK)
	lockT(t, "b excl 3", b.ShmLock(3, 1, lockExcl), Busy)
	lockT(t, "b unlock 3", b.ShmLock(3, 1, unShared), OK)
	lockT(t, "b excl 3", b.ShmLock(3, 1, lockExcl), Busy)
	lockT(t, "a unlock 3", a.ShmLock(3, 1, unShared), OK)
	lockT(t, "b excl 3", b.ShmLock(3, 1, lockExcl), OK)
	lockT(t, "a shared 3", a.ShmLock(3, 1, lockShared), Busy)

	// The write lock.
	lockT(t, "a excl 0", a.ShmLock(0, 1, lockExcl), OK)
	lockT(t, "b excl 0", b.ShmLock(0, 1, lockExcl), Busy)
	lockT(t, "b shared 0", b.ShmLock(0, 1, lockShared), Busy)
	lockT(t, "a unlock 0", a.ShmLock(0, 1, unExcl), OK)
	lockT(t, "b excl 0", b.ShmLock(0, 1, lockExcl), OK)

	// Many slots at once, as a checkpoint takes the read locks.
	lockT(t, "a excl 4..7", a.ShmLock(4, 4, lockExcl), OK)
	lockT(t, "b shared 5", b.ShmLock(5, 1, lockShared), Busy)
	lockT(t, "a unlock 4..7", a.ShmLock(4, 4, unExcl), OK)
	lockT(t, "b shared 5", b.ShmLock(5, 1, lockShared), OK)
	lockT(t, "a excl 4..7", a.ShmLock(4, 4, lockExcl), Busy)

	// Unlocking a slot not held does nothing.
	lockT(t, "a unlock 1", a.ShmLock(1, 1, unExcl), OK)

	// Unmapping drops every lock.
	if rc := b.ShmUnmap(false); rc != OK {
		t.Fatal(rc)
	}
	lockT(t, "a excl 0..7", a.ShmLock(0, 8, lockExcl), OK)
	if rc := b.ShmLock(0, 1, lockShared); rc != IOErrShmLock {
		t.Errorf("ShmLock without shm = %d, want IOErrShmLock", rc)
	}
}

func TestShmBlocksExclusive(t *testing.T) {
	// As os_unix's unixIsSharingShmNode: a connection that wants to delete
	// the WAL and -shm on close must not do it while another still uses the
	// -shm.
	a, b, _ := twoFiles(t)
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false); b.ShmUnmap(false) })

	lockT(t, "a SHARED", a.Lock(LockShared), OK)
	lockT(t, "a EXCLUSIVE", a.Lock(LockExclusive), Busy)
	b.ShmUnmap(false)
	lockT(t, "a EXCLUSIVE", a.Lock(LockExclusive), OK)
}

func TestShmLockAllocs(t *testing.T) {
	a, _, _ := twoFiles(t)
	shmMapT(t, a, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false) })
	n := testing.AllocsPerRun(100, func() {
		a.ShmLock(3, 1, ShmLock|ShmShared)
		a.ShmLock(3, 1, ShmUnlock|ShmShared)
		a.ShmLock(0, 1, ShmLock|ShmExclusive)
		a.ShmLock(0, 1, ShmUnlock|ShmExclusive)
		a.ShmMap(0, shmRegionSize, false)
	})
	if n != 0 {
		t.Errorf("ShmLock allocates %v times per call", n)
	}
}
