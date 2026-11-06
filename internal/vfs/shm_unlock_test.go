// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

// A slot whose unlock fails stays locked in the kernel. The tests below
// check that the node keeps it, retries it and hands it over, so that no
// connection is left with a slot SQLite thinks it gave back.

package vfs

import (
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func failUnlockOnce(t *testing.T, start int64) (count *int, stop func()) {
	n := 0
	stop = failFcntl(t, func(typ int16, s, l int64) error {
		if typ == unix.F_UNLCK && s == start && n == 0 {
			n++
			return unix.ENOLCK
		}
		return nil
	})
	return &n, stop
}

// The owner of the write slot closes and its unlock fails: the kernel lock
// stays, the node keeps it, and the timer's next run releases it.
func TestShmUnmapUnlockRetried(t *testing.T) {
	idleDelay(t, time.Hour)
	a, b, name := twoFiles(t)
	t.Cleanup(func() { b.ShmUnmap(false) })
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	lockT(t, "a writer", a.ShmLock(0, 1, ShmLock|ShmExclusive), OK)
	n, stop := failUnlockOnce(t, shmBase)
	lockT(t, "a close", a.Close(), OK)
	stop()
	if *n != 1 {
		t.Fatalf("injected %d failures", *n)
	}
	if got := heldBy(t, name, 0); got != unix.F_WRLCK {
		t.Fatalf("slot 0 after the failed unlock = %d, want F_WRLCK (the kernel still holds it)", got)
	}
	node := b.shm.node
	node.mu.Lock()
	idle, armed, l0 := node.idle, node.armed, node.lock[0]
	node.mu.Unlock()
	if idle != 1 || !armed || l0 != 0 {
		t.Fatalf("node after the failed unlock: idle %#x armed %v lock[0] %d, want 0x1 true 0", idle, armed, l0)
	}
	node.releaseIdle()
	if got := heldBy(t, name, 0); got != unix.F_UNLCK {
		t.Fatalf("slot 0 after the retry = %d, want F_UNLCK", got)
	}
	lockT(t, "b writer", b.ShmLock(0, 1, ShmLock|ShmExclusive), OK)
	lockT(t, "b unlock", b.ShmLock(0, 1, ShmUnlock|ShmExclusive), OK)
}

// Another connection of the process takes the slot over before the retry.
func TestShmUnmapUnlockTakenOver(t *testing.T) {
	idleDelay(t, time.Hour)
	a, b, name := twoFiles(t)
	t.Cleanup(func() { b.ShmUnmap(false) })
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	lockT(t, "a writer", a.ShmLock(0, 1, ShmLock|ShmExclusive), OK)
	_, stop := failUnlockOnce(t, shmBase)
	lockT(t, "a close", a.Close(), OK)
	stop()
	lockT(t, "b writer", b.ShmLock(0, 1, ShmLock|ShmExclusive), OK)
	if b.shm.node.idle != 0 {
		t.Errorf("idle after takeover = %#x, want 0", b.shm.node.idle)
	}
	lockT(t, "b unlock", b.ShmLock(0, 1, ShmUnlock|ShmExclusive), OK)
	if got := heldBy(t, name, 0); got != unix.F_UNLCK {
		t.Fatalf("slot 0 after b let go = %d, want F_UNLCK", got)
	}
}

// The failure in ShmLock itself, not in ShmUnmap: before, the connection
// kept the slot that SQLite thinks it gave back, and its next write lock was
// SQLITE_BUSY until it closed.
func TestShmUnlockFailureLetsGo(t *testing.T) {
	idleDelay(t, time.Hour)
	a, b, name := twoFiles(t)
	t.Cleanup(func() { a.ShmUnmap(false); b.ShmUnmap(false) })
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	lockT(t, "a writer", a.ShmLock(0, 1, ShmLock|ShmExclusive), OK)
	_, stop := failUnlockOnce(t, shmBase)
	rc := a.ShmLock(0, 1, ShmUnlock|ShmExclusive)
	stop()
	t.Logf("failed unlock reports %d", rc)
	if got := heldBy(t, name, 0); got != unix.F_WRLCK {
		t.Fatalf("slot 0 = %d, want F_WRLCK", got)
	}
	lockT(t, "a writer again", a.ShmLock(0, 1, ShmLock|ShmExclusive), OK)
	lockT(t, "a unlock", a.ShmLock(0, 1, ShmUnlock|ShmExclusive), OK)
	lockT(t, "b writer", b.ShmLock(0, 1, ShmLock|ShmExclusive), OK)
	lockT(t, "b unlock", b.ShmLock(0, 1, ShmUnlock|ShmExclusive), OK)
}

// A failure that lasts: the timer keeps trying, and the slot is released
// once the unlock works again.
func TestShmUnmapUnlockLastingFailure(t *testing.T) {
	idleDelay(t, time.Millisecond)
	a, b, name := twoFiles(t)
	t.Cleanup(func() { b.ShmUnmap(false) })
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	lockT(t, "a writer", a.ShmLock(0, 1, ShmLock|ShmExclusive), OK)
	stop := failFcntl(t, func(typ int16, s, l int64) error {
		if typ == unix.F_UNLCK && s == shmBase {
			return unix.ENOLCK
		}
		return nil
	})
	c := countFcntl(t)
	lockT(t, "a close", a.Close(), OK)
	time.Sleep(20 * time.Millisecond)
	if got := heldBy(t, name, 0); got != unix.F_WRLCK {
		t.Fatalf("slot 0 while unlock fails = %d, want F_WRLCK", got)
	}
	t.Logf("fcntl calls in 20 ms of failing retries: %d", c.Load())
	stop()
	waitUnlocked(t, name, 0)
	lockT(t, "b writer", b.ShmLock(0, 1, ShmLock|ShmExclusive), OK)
	lockT(t, "b unlock", b.ShmLock(0, 1, ShmUnlock|ShmExclusive), OK)
}

// A reader closes while its read slot sits between two of another reader:
// the unlock that splits the kernel's lock fails, and once the other reader
// lets go, the checkpoint gets the slot.
func TestShmUnmapReadLockSplit(t *testing.T) {
	a, b, name := twoFiles(t)
	otherProcess(t, name)
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	for _, slot := range []int{3, 5} {
		lockT(t, "b shared", b.ShmLock(slot, 1, ShmLock|ShmShared), OK)
	}
	lockT(t, "a shared", a.ShmLock(4, 1, ShmLock|ShmShared), OK)
	stop := failFcntl(t, func(typ int16, start, n int64) error {
		if typ == unix.F_UNLCK && start == shmBase+4 && n == 1 {
			return unix.ENOLCK
		}
		return nil
	})
	lockT(t, "a close", a.Close(), OK)
	stop()
	for _, slot := range []int{3, 5} {
		lockT(t, "b release", b.ShmLock(slot, 1, ShmUnlock|ShmShared), OK)
	}
	b.shm.node.releaseIdle()
	lockT(t, "b checkpoint slot", b.ShmLock(4, 1, ShmLock|ShmExclusive), OK)
	lockT(t, "b unlock", b.ShmLock(4, 1, ShmUnlock|ShmExclusive), OK)
}
