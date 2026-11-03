// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package vfs

import (
	"encoding/binary"
	"path/filepath"
	"sync"
	"testing"
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
