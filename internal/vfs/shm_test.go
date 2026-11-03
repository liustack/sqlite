// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package vfs

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
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

// countFcntl counts the lock fcntl calls made until the test ends.
func countFcntl(t *testing.T) *int {
	n := new(int)
	fcntlHook = func() { *n++ }
	t.Cleanup(func() { fcntlHook = nil })
	return n
}

// heldBy tells which lock another open file description of the -shm sees
// on slot i (or on the DMS byte for i == ShmNLock): F_UNLCK, F_RDLCK or
// F_WRLCK. It stands for another process.
func heldBy(t *testing.T, name string, i int) int16 {
	t.Helper()
	fd, err := unix.Open(name+"-shm", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	var fl unix.Flock_t
	typ, err := testLock(fd, &fl, int64(shmBase+i), 1)
	if err != nil {
		t.Fatal(err)
	}
	return typ
}

func TestShmLockCounted(t *testing.T) {
	a, b, name := twoFiles(t)
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false); b.ShmUnmap(false) })
	calls := countFcntl(t)

	const lockShared, unShared = ShmLock | ShmShared, ShmUnlock | ShmShared
	lockT(t, "a shared 3", a.ShmLock(3, 1, lockShared), OK)
	if *calls != 1 {
		t.Errorf("first shared lock made %d fcntl calls, want 1", *calls)
	}
	lockT(t, "b shared 3", b.ShmLock(3, 1, lockShared), OK)
	if *calls != 1 {
		t.Errorf("second shared lock made %d fcntl calls, want 0", *calls-1)
	}

	// Exclusive is refused in the process, without asking the kernel.
	lockT(t, "b excl 4", b.ShmLock(4, 1, ShmLock|ShmExclusive), OK)
	*calls = 0
	lockT(t, "a excl 3", a.ShmLock(3, 1, ShmLock|ShmExclusive), Busy)
	lockT(t, "a excl 3..5", a.ShmLock(3, 3, ShmLock|ShmExclusive), Busy)
	lockT(t, "a shared 4", a.ShmLock(4, 1, lockShared), Busy)
	if *calls != 0 {
		t.Errorf("refusals made %d fcntl calls, want 0", *calls)
	}
	lockT(t, "b unlock 4", b.ShmLock(4, 1, ShmUnlock|ShmExclusive), OK)

	// One holder unlocking keeps the lock for the other.
	lockT(t, "a unlock 3", a.ShmLock(3, 1, unShared), OK)
	if *calls != 1 {
		t.Errorf("unlocks made %d fcntl calls, want 1 (for the exclusive)", *calls)
	}
	if got := heldBy(t, name, 3); got != unix.F_RDLCK {
		t.Errorf("slot 3 seen from outside = %d, want F_RDLCK", got)
	}
	// Unlocking what a no longer holds does not take b's share.
	lockT(t, "a unlock 3 again", a.ShmLock(3, 1, unShared), OK)
	if got := heldBy(t, name, 3); got != unix.F_RDLCK {
		t.Errorf("slot 3 seen from outside = %d, want F_RDLCK", got)
	}
	lockT(t, "b unlock 3", b.ShmLock(3, 1, unShared), OK)
	if got := heldBy(t, name, 3); got != unix.F_UNLCK {
		t.Errorf("slot 3 seen from outside = %d, want F_UNLCK", got)
	}
}

func TestShmNodeDMS(t *testing.T) {
	a, b, name := twoFiles(t)
	shmMapT(t, a, 0, true)
	calls := countFcntl(t)
	// The second connection of the process finds the -shm open and does
	// no DMS check.
	shmMapT(t, b, 0, false)
	if *calls != 0 {
		t.Errorf("second ShmMap made %d fcntl calls, want 0", *calls)
	}
	fcntlHook = nil

	// The DMS lock stays while any connection of the process has the -shm.
	if got := heldBy(t, name, ShmNLock); got != unix.F_RDLCK {
		t.Errorf("DMS seen from outside = %d, want F_RDLCK", got)
	}
	a.ShmUnmap(false)
	if got := heldBy(t, name, ShmNLock); got != unix.F_RDLCK {
		t.Errorf("DMS after a unmapped = %d, want F_RDLCK", got)
	}
	b.ShmUnmap(false)
	if _, err := os.Stat(name + "-shm"); err != nil {
		t.Fatal(err)
	}
	if got := heldBy(t, name, ShmNLock); got != unix.F_UNLCK {
		t.Errorf("DMS after all unmapped = %d, want F_UNLCK", got)
	}
}

func TestShmCloseReleasesLocks(t *testing.T) {
	a, b, name := twoFiles(t)
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	t.Cleanup(func() { b.ShmUnmap(false) })
	lockT(t, "a excl 0", a.ShmLock(0, 1, ShmLock|ShmExclusive), OK)
	lockT(t, "a shared 3", a.ShmLock(3, 1, ShmLock|ShmShared), OK)
	// a goes away without unlocking: the process must not keep its locks.
	a.Close()
	lockT(t, "b excl 0", b.ShmLock(0, 1, ShmLock|ShmExclusive), OK)
	lockT(t, "b excl 3", b.ShmLock(3, 1, ShmLock|ShmExclusive), OK)
	if got := heldBy(t, name, 3); got != unix.F_WRLCK {
		t.Errorf("slot 3 seen from outside = %d, want F_WRLCK", got)
	}
}

// TestShmLockParallel has goroutines, each with its own File, take and drop
// shared read marks and the exclusive write lock at once, as readers and a
// writer do. The writer must never hold the write lock while another does.
func TestShmLockParallel(t *testing.T) {
	name := filepath.Join(t.TempDir(), "par.db")
	files := make([]*File, 8)
	for i := range files {
		files[i] = openT(t, name, mainDB)
		shmMapT(t, files[i], 0, true)
	}
	var inWriter atomic.Int32
	var wg sync.WaitGroup
	for i, f := range files {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slot := 3 + i%5
			for range 2000 {
				if f.ShmLock(slot, 1, ShmLock|ShmShared) == OK {
					f.ShmLock(slot, 1, ShmUnlock|ShmShared)
				}
				if f.ShmLock(0, 1, ShmLock|ShmExclusive) == OK {
					if inWriter.Add(1) != 1 {
						t.Error("two writers at once")
					}
					inWriter.Add(-1)
					f.ShmLock(0, 1, ShmUnlock|ShmExclusive)
				}
			}
		}()
	}
	wg.Wait()
	for _, f := range files {
		f.ShmUnmap(false)
	}
	if got := heldBy(t, name, 0); got != unix.F_UNLCK {
		t.Errorf("write lock left behind: %d", got)
	}
}
