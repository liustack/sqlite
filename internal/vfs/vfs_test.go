// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

// The tests that hold on every system. Those of one system are in the
// files of that system.

package vfs

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"unsafe"
)

const (
	mainDB    = OpenReadWrite | OpenCreate | OpenMainDB
	mainJrnl  = OpenReadWrite | OpenCreate | OpenMainJournal
	walFile   = OpenReadWrite | OpenCreate | OpenWAL
	tempJrnl  = OpenReadWrite | OpenCreate | OpenExclusive | OpenDeleteOnClose | OpenTempJournal
	readOnlyD = OpenReadOnly | OpenMainDB
)

// openT opens name and fails the test unless it opens.
func openT(t *testing.T, name string, flags int) *File {
	t.Helper()
	f, _, rc := Open(name, flags)
	if rc != OK {
		t.Fatalf("Open(%q, %#x) = %d", name, flags, rc)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestOpenMissing(t *testing.T) {
	name := filepath.Join(t.TempDir(), "missing.db")
	if _, _, rc := Open(name, OpenReadWrite|OpenMainDB); rc != CantOpen {
		t.Errorf("Open read-write without create = %d, want CantOpen", rc)
	}
	if _, _, rc := Open(name, readOnlyD); rc != CantOpen {
		t.Errorf("Open read-only = %d, want CantOpen", rc)
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Errorf("file was created: %v", err)
	}
}

func TestOpenDirectory(t *testing.T) {
	if _, _, rc := Open(t.TempDir(), mainDB); rc&0xff != CantOpen {
		t.Errorf("Open of a directory = %d, want CantOpen", rc)
	}
}

func TestOpenExclusive(t *testing.T) {
	name := filepath.Join(t.TempDir(), "excl")
	flags := OpenReadWrite | OpenCreate | OpenExclusive | OpenTempDB
	openT(t, name, flags)
	if _, _, rc := Open(name, flags); rc != CantOpen {
		t.Errorf("second exclusive Open = %d, want CantOpen", rc)
	}
}

func TestDeleteOnClose(t *testing.T) {
	name := filepath.Join(t.TempDir(), "doc")
	f, _, rc := Open(name, OpenReadWrite|OpenCreate|OpenDeleteOnClose|OpenSubjournal)
	if rc != OK {
		t.Fatalf("Open = %d", rc)
	}
	if rc := f.WriteAt([]byte("x"), 0); rc != OK {
		t.Fatalf("WriteAt = %d", rc)
	}
	if rc := f.Close(); rc != OK {
		t.Fatalf("Close = %d", rc)
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Errorf("file still there after Close: %v", err)
	}
}

func TestShortRead(t *testing.T) {
	f := openT(t, filepath.Join(t.TempDir(), "short.db"), mainDB)
	if rc := f.WriteAt([]byte("0123456789"), 0); rc != OK {
		t.Fatalf("WriteAt = %d", rc)
	}
	buf := bytes.Repeat([]byte{0xff}, 20)
	if rc := f.ReadAt(buf, 4); rc != IOErrShortRead {
		t.Errorf("ReadAt = %d, want IOErrShortRead", rc)
	}
	want := append([]byte("456789"), make([]byte, 14)...)
	if !bytes.Equal(buf, want) {
		t.Errorf("buf = %q, want %q", buf, want)
	}

	buf = bytes.Repeat([]byte{0xff}, 8)
	if rc := f.ReadAt(buf, 1000); rc != IOErrShortRead {
		t.Errorf("ReadAt past the end = %d, want IOErrShortRead", rc)
	}
	if !bytes.Equal(buf, make([]byte, 8)) {
		t.Errorf("buf = %q, want zeros", buf)
	}
}

func TestTruncateAndSize(t *testing.T) {
	f := openT(t, filepath.Join(t.TempDir(), "trunc.db"), mainDB)
	if rc := f.WriteAt(make([]byte, 8192), 0); rc != OK {
		t.Fatal(rc)
	}
	if rc := f.Truncate(4096); rc != OK {
		t.Fatalf("Truncate = %d", rc)
	}
	if size, rc := f.Size(); rc != OK || size != 4096 {
		t.Errorf("Size = %d, %d, want 4096", size, rc)
	}
	// With a chunk size, Truncate rounds up to whole chunks.
	f.SetChunkSize(3000)
	if rc := f.Truncate(1000); rc != OK {
		t.Fatalf("Truncate = %d", rc)
	}
	if size, _ := f.Size(); size != 3000 {
		t.Errorf("Size = %d, want 3000", size)
	}
}

func TestSizeHint(t *testing.T) {
	f := openT(t, filepath.Join(t.TempDir(), "hint.db"), mainDB)
	// Without a chunk size the hint does nothing, as os_unix.
	if rc := f.SizeHint(10000); rc != OK {
		t.Fatalf("SizeHint = %d", rc)
	}
	if size, _ := f.Size(); size != 0 {
		t.Errorf("Size = %d, want 0", size)
	}
	f.SetChunkSize(4096)
	if rc := f.SizeHint(10000); rc != OK {
		t.Fatalf("SizeHint = %d", rc)
	}
	if size, _ := f.Size(); size != 12288 {
		t.Errorf("Size = %d, want 12288", size)
	}
	// A hint never makes the file smaller.
	if rc := f.SizeHint(100); rc != OK {
		t.Fatalf("SizeHint = %d", rc)
	}
	if size, _ := f.Size(); size != 12288 {
		t.Errorf("Size = %d, want 12288", size)
	}
}

func TestDelete(t *testing.T) {
	name := filepath.Join(t.TempDir(), "del")
	os.WriteFile(name, []byte("x"), 0644)
	if rc := Delete(name, true); rc != OK {
		t.Fatalf("Delete = %d", rc)
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Errorf("file still there: %v", err)
	}
	if rc := Delete(name, false); rc != IOErrDeleteNoent {
		t.Errorf("Delete of a missing file = %d, want IOErrDeleteNoent", rc)
	}
}

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
	if a.LockState() != failedExclusiveFromShared {
		t.Errorf("a LockState = %d, want %d", a.LockState(), failedExclusiveFromShared)
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

// The helper process takes the locks named in its environment, says
// "ready" and holds them until its stdin is closed.
const helperEnv = "VFS_TEST_HELPER_DB"

func helper(name string) int {
	f, _, rc := Open(name, mainDB)
	if rc != OK {
		fmt.Println("open", rc)
		return 1
	}
	steps := []struct {
		what string
		rc   int
	}{
		{"SHARED", f.Lock(LockShared)},
		{"RESERVED", f.Lock(LockReserved)},
	}
	addr, rc := f.ShmMap(0, shmRegionSize, true)
	steps = append(steps, struct {
		what string
		rc   int
	}{"ShmMap", rc})
	if rc == OK {
		region(addr)[0] = 99
		steps = append(steps, struct {
			what string
			rc   int
		}{"ShmLock", f.ShmLock(0, 1, ShmLock|ShmExclusive)})
	}
	for _, s := range steps {
		if s.rc != OK {
			fmt.Println(s.what, s.rc)
			return 1
		}
	}
	fmt.Println("ready")
	io.Copy(io.Discard, os.Stdin)
	return 0
}

func TestCrossProcess(t *testing.T) {
	name := filepath.Join(t.TempDir(), "proc.db")
	f := openT(t, name, mainDB)

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), helperEnv+"="+name)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	defer stdin.Close()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("helper said %q, %v", line, err)
	}

	// The helper holds SHARED and RESERVED on the database, has the -shm
	// mapped and holds the WAL write lock.
	lockT(t, "SHARED", f.Lock(LockShared), OK)
	if r, rc := f.CheckReservedLock(); rc != OK || !r {
		t.Errorf("CheckReservedLock = %v, %d, want true", r, rc)
	}
	lockT(t, "RESERVED", f.Lock(LockReserved), Busy)
	lockT(t, "EXCLUSIVE", f.Lock(LockExclusive), Busy)

	addr := shmMapT(t, f, 0, false)
	if addr == 0 || region(addr)[0] != 99 {
		t.Fatal("the -shm of the helper was not kept")
	}
	lockT(t, "shm excl 0", f.ShmLock(0, 1, ShmLock|ShmExclusive), Busy)
	lockT(t, "shm shared 3", f.ShmLock(3, 1, ShmLock|ShmShared), OK)

	// When the helper exits, its locks go with it.
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper: %v", err)
	}
	lockT(t, "shm excl 0", f.ShmLock(0, 1, ShmLock|ShmExclusive), OK)
	lockT(t, "RESERVED", f.Lock(LockReserved), OK)
	lockT(t, "EXCLUSIVE", f.Lock(LockExclusive), OK)
	f.ShmUnmap(false)
}

// startHelper runs the test binary as a helper of kind env on name and
// waits for it to say "ready". The helper ends when the test does, or
// when stop is called.
func startHelper(t *testing.T, env, name string) (stop func()) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), env+"="+name)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop = func() {
		if !stopped {
			stopped = true
			stdin.Close()
			cmd.Wait()
		}
	}
	t.Cleanup(stop)
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("helper said %q, %v", line, err)
	}
	return stop
}

func TestShmReadOnlyParam(t *testing.T) {
	// readonly_shm=1: the -shm is opened for reading only, as os_unix and
	// os_win. It can be used only while another process has it set up.
	name := filepath.Join(t.TempDir(), "roshm.db")
	w := openT(t, name, mainDB)
	shmMapT(t, w, 0, true)
	if rc := w.ShmUnmap(false); rc != OK {
		t.Fatal(rc)
	}

	// The -shm is there, but no one has it open.
	f, _, rc := OpenWith(name, mainDB, OpenOptions{ReadOnlyShm: true})
	if rc != OK {
		t.Fatalf("Open = %d", rc)
	}
	t.Cleanup(func() { f.Close() })
	if _, rc := f.ShmMap(0, shmRegionSize, false); rc != ReadOnlyCantInit {
		t.Errorf("ShmMap with no one else = %d, want ReadOnlyCantInit", rc)
	}
	f.ShmUnmap(false)

	// Another process has it set up: the memory is there, read only.
	startHelper(t, helperEnv, name)
	addr, rc := f.ShmMap(0, shmRegionSize, false)
	if rc != ReadOnly || addr == 0 {
		t.Fatalf("ShmMap = %#x, %d, want an address and ReadOnly", addr, rc)
	}
	if region(addr)[0] != 99 {
		t.Errorf("read-only -shm does not show the helper's write")
	}
	lockT(t, "shared 3", f.ShmLock(3, 1, ShmLock|ShmShared), OK)
	f.ShmUnmap(false)
}
