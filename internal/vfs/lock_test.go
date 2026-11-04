// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package vfs

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

// lockT checks the result of a Lock or Unlock call.
func lockT(t *testing.T, what string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %d, want %d", what, got, want)
	}
}

// twoFiles opens the same database twice, as two connections would.
func twoFiles(t *testing.T) (a, b *File, name string) {
	name = filepath.Join(t.TempDir(), "lock.db")
	return openT(t, name, mainDB), openT(t, name, mainDB), name
}

func TestLockSharedShared(t *testing.T) {
	a, b, _ := twoFiles(t)
	lockT(t, "a SHARED", a.Lock(LockShared), OK)
	lockT(t, "b SHARED", b.Lock(LockShared), OK)
	if a.LockState() != LockShared || b.LockState() != LockShared {
		t.Errorf("LockState = %d, %d", a.LockState(), b.LockState())
	}
	// Asking for a level already held does nothing.
	lockT(t, "a SHARED again", a.Lock(LockShared), OK)
}

func TestLockReserved(t *testing.T) {
	a, b, _ := twoFiles(t)
	lockT(t, "a SHARED", a.Lock(LockShared), OK)
	lockT(t, "b SHARED", b.Lock(LockShared), OK)

	if r, rc := b.CheckReservedLock(); rc != OK || r {
		t.Errorf("b CheckReservedLock = %v, %d before RESERVED", r, rc)
	}
	lockT(t, "a RESERVED", a.Lock(LockReserved), OK)
	lockT(t, "b RESERVED", b.Lock(LockReserved), Busy)
	if b.LockState() != LockShared {
		t.Errorf("b LockState = %d after a failed RESERVED", b.LockState())
	}
	if r, rc := b.CheckReservedLock(); rc != OK || !r {
		t.Errorf("b CheckReservedLock = %v, %d, want true", r, rc)
	}
	if r, rc := a.CheckReservedLock(); rc != OK || !r {
		t.Errorf("a CheckReservedLock = %v, %d, want true", r, rc)
	}

	// RESERVED does not keep new readers out.
	c := openT(t, a.path, mainDB)
	lockT(t, "c SHARED", c.Lock(LockShared), OK)

	lockT(t, "a unlock to SHARED", a.Unlock(LockShared), OK)
	if r, _ := b.CheckReservedLock(); r {
		t.Error("b CheckReservedLock after a released RESERVED = true")
	}
	lockT(t, "b RESERVED", b.Lock(LockReserved), OK)
}

func TestLockExclusive(t *testing.T) {
	a, b, name := twoFiles(t)
	lockT(t, "a SHARED", a.Lock(LockShared), OK)
	lockT(t, "b SHARED", b.Lock(LockShared), OK)
	lockT(t, "a RESERVED", a.Lock(LockReserved), OK)

	// b still reads, so a stops at PENDING.
	lockT(t, "a EXCLUSIVE", a.Lock(LockExclusive), Busy)
	if a.LockState() != LockPending {
		t.Errorf("a LockState = %d, want PENDING", a.LockState())
	}

	// PENDING keeps new readers out, but not the one already in.
	c := openT(t, name, mainDB)
	lockT(t, "c SHARED", c.Lock(LockShared), Busy)
	if c.LockState() != LockNone {
		t.Errorf("c LockState = %d after a failed SHARED", c.LockState())
	}

	lockT(t, "b unlock", b.Unlock(LockNone), OK)
	lockT(t, "a EXCLUSIVE", a.Lock(LockExclusive), OK)
	if a.LockState() != LockExclusive {
		t.Errorf("a LockState = %d, want EXCLUSIVE", a.LockState())
	}
	lockT(t, "b SHARED", b.Lock(LockShared), Busy)

	// Down to SHARED: readers come back, writers still wait for a.
	lockT(t, "a unlock to SHARED", a.Unlock(LockShared), OK)
	lockT(t, "b SHARED", b.Lock(LockShared), OK)
	lockT(t, "c SHARED", c.Lock(LockShared), OK)
	lockT(t, "b RESERVED", b.Lock(LockReserved), OK)
	lockT(t, "b EXCLUSIVE", b.Lock(LockExclusive), Busy)

	lockT(t, "a unlock", a.Unlock(LockNone), OK)
	lockT(t, "c unlock", c.Unlock(LockNone), OK)
	lockT(t, "b EXCLUSIVE", b.Lock(LockExclusive), OK)
	lockT(t, "b unlock", b.Unlock(LockNone), OK)
	if b.LockState() != LockNone {
		t.Errorf("b LockState = %d, want NONE", b.LockState())
	}
	lockT(t, "a EXCLUSIVE from SHARED", func() int {
		if rc := a.Lock(LockShared); rc != OK {
			return rc
		}
		return a.Lock(LockExclusive)
	}(), OK)
}

func TestLockExclusiveFromShared(t *testing.T) {
	// A hot journal is rolled back by going from SHARED straight to
	// EXCLUSIVE.
	a, b, _ := twoFiles(t)
	lockT(t, "a SHARED", a.Lock(LockShared), OK)
	lockT(t, "b SHARED", b.Lock(LockShared), OK)
	lockT(t, "a EXCLUSIVE", a.Lock(LockExclusive), Busy)
	if a.LockState() != LockShared {
		t.Errorf("a LockState = %d, want SHARED", a.LockState())
	}
	lockT(t, "b unlock", b.Unlock(LockNone), OK)
	lockT(t, "a EXCLUSIVE", a.Lock(LockExclusive), OK)
}

func TestUnlockReleasesAll(t *testing.T) {
	a, b, _ := twoFiles(t)
	lockT(t, "a SHARED", a.Lock(LockShared), OK)
	lockT(t, "a RESERVED", a.Lock(LockReserved), OK)
	lockT(t, "a EXCLUSIVE", a.Lock(LockExclusive), OK)
	lockT(t, "a unlock", a.Unlock(LockNone), OK)
	lockT(t, "b SHARED", b.Lock(LockShared), OK)
	lockT(t, "b RESERVED", b.Lock(LockReserved), OK)
	lockT(t, "b EXCLUSIVE", b.Lock(LockExclusive), OK)
	// Unlocking below the level held does nothing.
	lockT(t, "a unlock again", a.Unlock(LockNone), OK)
}

func TestCloseReleasesLocks(t *testing.T) {
	a, b, _ := twoFiles(t)
	lockT(t, "a SHARED", a.Lock(LockShared), OK)
	lockT(t, "a RESERVED", a.Lock(LockReserved), OK)
	lockT(t, "a EXCLUSIVE", a.Lock(LockExclusive), OK)
	if rc := a.Close(); rc != OK {
		t.Fatal(rc)
	}
	lockT(t, "b SHARED", b.Lock(LockShared), OK)
	lockT(t, "b EXCLUSIVE", b.Lock(LockExclusive), OK)
}

func TestCloseOtherDescriptorKeepsLocks(t *testing.T) {
	// With POSIX locks, closing any descriptor of a file drops every lock
	// the process holds on it. Locks on open file descriptions do not.
	a, b, name := twoFiles(t)
	lockT(t, "a SHARED", a.Lock(LockShared), OK)
	lockT(t, "a RESERVED", a.Lock(LockReserved), OK)
	c := openT(t, name, mainDB)
	c.Close()
	lockT(t, "b SHARED", b.Lock(LockShared), OK)
	lockT(t, "b RESERVED", b.Lock(LockReserved), Busy)
}

func TestNoLockForJournals(t *testing.T) {
	// As os_unix, only the main database is locked.
	name := filepath.Join(t.TempDir(), "x.db-journal")
	openT(t, filepath.Join(filepath.Dir(name), "x.db"), mainDB)
	a := openT(t, name, mainJrnl)
	b := openT(t, name, mainJrnl)
	lockT(t, "a SHARED", a.Lock(LockShared), OK)
	lockT(t, "a EXCLUSIVE", a.Lock(LockExclusive), OK)
	lockT(t, "b SHARED", b.Lock(LockShared), OK)
	lockT(t, "b EXCLUSIVE", b.Lock(LockExclusive), OK)
	if r, rc := b.CheckReservedLock(); rc != OK || r {
		t.Errorf("CheckReservedLock = %v, %d", r, rc)
	}
}

// TestLockCounter has many goroutines, each with its own File, add one to
// a counter in the database under an EXCLUSIVE lock. A lost update means
// two of them held EXCLUSIVE at once.
func TestLockCounter(t *testing.T) {
	name := filepath.Join(t.TempDir(), "counter.db")
	openT(t, name, mainDB).WriteAt(make([]byte, 8), 0)

	const workers, rounds = 8, 50
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, _, rc := Open(name, mainDB)
			if rc != OK {
				errs <- "open failed"
				return
			}
			defer f.Close()
			buf := make([]byte, 8)
			for done := 0; done < rounds; {
				if f.Lock(LockShared) != OK {
					continue
				}
				if f.Lock(LockReserved) != OK {
					f.Unlock(LockNone)
					continue
				}
				for f.Lock(LockExclusive) != OK {
				}
				if rc := f.ReadAt(buf, 0); rc != OK {
					errs <- "read failed"
					return
				}
				binary.LittleEndian.PutUint64(buf, binary.LittleEndian.Uint64(buf)+1)
				if rc := f.WriteAt(buf, 0); rc != OK {
					errs <- "write failed"
					return
				}
				if rc := f.Unlock(LockNone); rc != OK {
					errs <- "unlock failed"
					return
				}
				done++
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	f := openT(t, name, mainDB)
	buf := make([]byte, 8)
	f.ReadAt(buf, 0)
	if got := binary.LittleEndian.Uint64(buf); got != workers*rounds {
		t.Errorf("counter = %d, want %d", got, workers*rounds)
	}
}

func TestHotPathAllocs(t *testing.T) {
	a, b, _ := twoFiles(t)
	buf := make([]byte, 4096)
	a.WriteAt(buf, 0)
	for _, c := range []struct {
		name string
		fn   func()
	}{
		{"ReadAt", func() { a.ReadAt(buf, 0) }},
		{"WriteAt", func() { a.WriteAt(buf, 0) }},
		{"Lock", func() {
			a.Lock(LockShared)
			a.Lock(LockReserved)
			a.Lock(LockExclusive)
			a.Unlock(LockShared)
			a.Unlock(LockNone)
		}},
		{"CheckReservedLock", func() {
			b.Lock(LockShared)
			b.CheckReservedLock()
			b.Unlock(LockNone)
		}},
	} {
		if n := testing.AllocsPerRun(100, c.fn); n != 0 {
			t.Errorf("%s allocates %v times per call", c.name, n)
		}
	}
}

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
