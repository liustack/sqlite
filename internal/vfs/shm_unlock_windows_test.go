// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build windows && amd64

package vfs

import (
	"testing"

	"golang.org/x/sys/windows"
)

// failUnlock makes UnlockFileEx of the range from slot fail while fail
// says so.
func failUnlock(t *testing.T, slot int, fail func() bool) {
	t.Helper()
	unlockFileEx = func(h windows.Handle, reserved, low, high uint32, ov *windows.Overlapped) error {
		if ov.Offset == uint32(shmBase+slot) && fail() {
			return windows.ERROR_NOT_LOCKED
		}
		return windows.UnlockFileEx(h, reserved, low, high, ov)
	}
	t.Cleanup(func() { unlockFileEx = windows.UnlockFileEx })
}

func once() func() bool {
	done := false
	return func() bool {
		if done {
			return false
		}
		done = true
		return true
	}
}

// A File whose unlock fails lets go of the slot as SQLite takes it to, and
// its next lock of the slot unlocks the range again first.
func TestShmUnlockFailureRetried(t *testing.T) {
	a, _, name := twoFiles(t)
	shmMapT(t, a, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false) })
	lockT(t, "a excl 0", a.ShmLock(0, 1, ShmLock|ShmExclusive), OK)
	failUnlock(t, 0, once())
	lockT(t, "a unlock 0", a.ShmLock(0, 1, ShmUnlock|ShmExclusive), IOErrUnlock)
	if s, e := held(t, name+"-shm", shmBase, 1); s || e {
		t.Fatalf("slot 0 after the failed unlock: can lock shared %v, exclusive %v, want neither", s, e)
	}
	lockT(t, "a excl 0 again", a.ShmLock(0, 1, ShmLock|ShmExclusive), OK)
	lockT(t, "a unlock 0", a.ShmLock(0, 1, ShmUnlock|ShmExclusive), OK)
	if s, e := held(t, name+"-shm", shmBase, 1); !s || !e {
		t.Fatalf("slot 0 after the unlock: can lock shared %v, exclusive %v, want both", s, e)
	}
}

// On a network drive the slot locks are on the node's handle: a File that
// goes while its unlock fails leaves the range to the node, and the next
// File to lock the slot unlocks it first, or is told busy while it cannot.
func TestShmSharedUnlockFailure(t *testing.T) {
	sharedShmForTest = true
	t.Cleanup(func() { sharedShmForTest = false })
	a, b, name := twoFiles(t)
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	t.Cleanup(func() { b.ShmUnmap(false) })
	lockT(t, "a excl 0", a.ShmLock(0, 1, ShmLock|ShmExclusive), OK)
	failing := true
	failUnlock(t, 0, func() bool { return failing })
	lockT(t, "a close", a.ShmUnmap(false), OK)
	if s, e := held(t, name+"-shm", shmBase, 1); s || e {
		t.Fatalf("slot 0 while its unlock fails: can lock shared %v, exclusive %v, want neither", s, e)
	}
	lockT(t, "b excl 0 while it fails", b.ShmLock(0, 1, ShmLock|ShmExclusive), Busy)
	lockT(t, "b excl 1", b.ShmLock(1, 1, ShmLock|ShmExclusive), OK)
	lockT(t, "b unlock 1", b.ShmLock(1, 1, ShmUnlock|ShmExclusive), OK)
	failing = false
	lockT(t, "b excl 0", b.ShmLock(0, 1, ShmLock|ShmExclusive), OK)
	lockT(t, "b unlock 0", b.ShmLock(0, 1, ShmUnlock|ShmExclusive), OK)
	if s, e := held(t, name+"-shm", shmBase, 1); !s || !e {
		t.Fatalf("slot 0 at the end: can lock shared %v, exclusive %v, want both", s, e)
	}
}

// A File whose unlock failed then sits idle: another File of the process
// that wants the slot unlocks the range again for it, and is not told busy
// until the first File locks again.
func TestShmUnlockFailureOtherFile(t *testing.T) {
	a, b, name := twoFiles(t)
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false); b.ShmUnmap(false) })
	lockT(t, "a excl 0", a.ShmLock(0, 1, ShmLock|ShmExclusive), OK)
	failUnlock(t, 0, once())
	lockT(t, "a unlock 0", a.ShmLock(0, 1, ShmUnlock|ShmExclusive), IOErrUnlock)
	lockT(t, "b excl 0", b.ShmLock(0, 1, ShmLock|ShmExclusive), OK)
	lockT(t, "b unlock 0", b.ShmLock(0, 1, ShmUnlock|ShmExclusive), OK)
	if s, e := held(t, name+"-shm", shmBase, 1); !s || !e {
		t.Fatalf("slot 0 at the end: can lock shared %v, exclusive %v, want both", s, e)
	}
}

// A range of several slots is unlocked again as it was locked, before a
// lock of one of its slots.
func TestShmUnlockFailureRange(t *testing.T) {
	a, b, name := twoFiles(t)
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false); b.ShmUnmap(false) })
	lockT(t, "a excl 3-7", a.ShmLock(3, 5, ShmLock|ShmExclusive), OK)
	failUnlock(t, 3, once())
	lockT(t, "a unlock 3-7", a.ShmLock(3, 5, ShmUnlock|ShmExclusive), IOErrUnlock)
	lockT(t, "a shared 5", a.ShmLock(5, 1, ShmLock|ShmShared), OK)
	lockT(t, "b shared 5", b.ShmLock(5, 1, ShmLock|ShmShared), OK)
	lockT(t, "b excl 3", b.ShmLock(3, 1, ShmLock|ShmExclusive), OK)
	lockT(t, "b unlock 3", b.ShmLock(3, 1, ShmUnlock|ShmExclusive), OK)
	lockT(t, "a unlock 5", a.ShmLock(5, 1, ShmUnlock|ShmShared), OK)
	lockT(t, "b unlock 5", b.ShmLock(5, 1, ShmUnlock|ShmShared), OK)
	if s, e := held(t, name+"-shm", shmBase+3, 5); !s || !e {
		t.Fatalf("slots 3-7 at the end: can lock shared %v, exclusive %v, want both", s, e)
	}
}

// A shared lock whose unlock failed would stack under the next one on the
// same handle: it is unlocked first, so that one unlock frees the slot.
func TestShmSharedUnlockFailureRetried(t *testing.T) {
	a, _, name := twoFiles(t)
	shmMapT(t, a, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false) })
	lockT(t, "a shared 3", a.ShmLock(3, 1, ShmLock|ShmShared), OK)
	failUnlock(t, 3, once())
	lockT(t, "a unlock 3", a.ShmLock(3, 1, ShmUnlock|ShmShared), IOErrUnlock)
	lockT(t, "a shared 3 again", a.ShmLock(3, 1, ShmLock|ShmShared), OK)
	lockT(t, "a unlock 3", a.ShmLock(3, 1, ShmUnlock|ShmShared), OK)
	if s, e := held(t, name+"-shm", shmBase+3, 1); !s || !e {
		t.Fatalf("slot 3 at the end: can lock shared %v, exclusive %v, want both", s, e)
	}
}

// A File that closes while its unlock still fails leaves nothing behind: its
// handle's close drops the lock, and no one unlocks on that handle later.
func TestShmUnmapForgetsLeft(t *testing.T) {
	a, b, _ := twoFiles(t)
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	t.Cleanup(func() { b.ShmUnmap(false) })
	lockT(t, "a excl 0", a.ShmLock(0, 1, ShmLock|ShmExclusive), OK)
	failUnlock(t, 0, func() bool { return true })
	lockT(t, "a close", a.ShmUnmap(false), OK)
	node := b.shm.node
	node.mu.Lock()
	n := len(node.left)
	node.mu.Unlock()
	if n != 0 || node.leftN.Load() != 0 {
		t.Fatalf("ranges kept after the close: %d (count %d), want none", n, node.leftN.Load())
	}
}
