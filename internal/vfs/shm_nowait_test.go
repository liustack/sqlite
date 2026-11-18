// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

// A goroutine that waits for a mutex sleeps, and when every core is busy it
// waits long for a core once woken. The tests below check that what a writer
// and a reader do at every transaction does not wait for the mutex of the
// node, whoever holds it.

package vfs

import (
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// nodeHeld runs fn while the test holds the mutex of the node, and fails
// when fn has not returned in a few seconds.
func nodeHeld(t *testing.T, node *shmNode, what string, fn func()) {
	t.Helper()
	node.mu.Lock()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
		node.mu.Unlock()
	case <-time.After(2 * time.Second):
		node.mu.Unlock()
		<-done
		t.Fatalf("%s waited for the mutex of the node", what)
	}
}

// The only reader of a slot takes it and gives it back, as a connection
// alone on a read mark does at every transaction.
func TestShmLoneReaderTakesNoMutex(t *testing.T) {
	idleDelay(t, time.Hour)
	a, _, name := twoFiles(t)
	shmMapT(t, a, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false) })
	const lockShared, unShared = ShmLock | ShmShared, ShmUnlock | ShmShared
	lockT(t, "a shared 3", a.ShmLock(3, 1, lockShared), OK)
	lockT(t, "a unlock 3", a.ShmLock(3, 1, unShared), OK)

	var rc [2]int
	// Nor for the mutex of the slot.
	a.shm.node.slots[3].mu.Lock()
	nodeHeld(t, a.shm.node, "the only reader of a slot the process keeps", func() {
		rc = [2]int{a.ShmLock(3, 1, lockShared), a.ShmLock(3, 1, unShared)}
	})
	a.shm.node.slots[3].mu.Unlock()
	if rc != [2]int{OK, OK} {
		t.Fatalf("shared 3 and its unlock = %v, want OK", rc)
	}
	if got := heldBy(t, name, 3); got != unix.F_RDLCK {
		t.Errorf("slot 3 seen from outside = %d, want F_RDLCK: the process keeps it", got)
	}
}

// A slot another connection of the process holds is refused at once, as
// when a connection tries each read mark in turn to move one.
func TestShmHeldSlotRefusedTakesNoMutex(t *testing.T) {
	idleDelay(t, time.Hour)
	a, b, _ := twoFiles(t)
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false); b.ShmUnmap(false) })
	lockT(t, "a shared 3", a.ShmLock(3, 1, ShmLock|ShmShared), OK)
	lockT(t, "a exclusive 4", a.ShmLock(4, 1, ShmLock|ShmExclusive), OK)

	var rc [3]int
	nodeHeld(t, a.shm.node, "a lock on a slot another connection holds", func() {
		rc = [3]int{
			b.ShmLock(3, 1, ShmLock|ShmExclusive),
			b.ShmLock(4, 1, ShmLock|ShmExclusive),
			b.ShmLock(4, 1, ShmLock|ShmShared),
		}
	})
	if rc != [3]int{Busy, Busy, Busy} {
		t.Fatalf("exclusive 3, exclusive 4, shared 4 = %v, want SQLITE_BUSY", rc)
	}
	lockT(t, "a unlock 4", a.ShmLock(4, 1, ShmUnlock|ShmExclusive), OK)
	lockT(t, "a unlock 3", a.ShmLock(3, 1, ShmUnlock|ShmShared), OK)
}

// The write slot is taken and given back, and a read mark is moved (its
// slot exclusive, then shared), as a writer does at every transaction.
func TestShmWriterTakesNoMutex(t *testing.T) {
	idleDelay(t, time.Hour)
	a, _, name := twoFiles(t)
	shmMapT(t, a, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false) })

	var rc [6]int
	nodeHeld(t, a.shm.node, "a writer's transaction", func() {
		rc = [6]int{
			a.ShmLock(4, 1, ShmLock|ShmExclusive),
			a.ShmLock(4, 1, ShmUnlock|ShmExclusive),
			a.ShmLock(4, 1, ShmLock|ShmShared),
			a.ShmLock(0, 1, ShmLock|ShmExclusive),
			a.ShmLock(0, 1, ShmUnlock|ShmExclusive),
			a.ShmLock(4, 1, ShmUnlock|ShmShared),
		}
	})
	if rc != [6]int{} {
		t.Fatalf("the locks of a write transaction = %v, want OK", rc)
	}
	if got := heldBy(t, name, 0); got != unix.F_UNLCK {
		t.Errorf("the write slot seen from outside = %d, want F_UNLCK", got)
	}
}

// learn (wal_unix.go) holds the mutex of the node while it looks at who has
// the checkpoint slot: a connection takes that slot under the mutex.
func TestShmCheckpointSlotTakesMutex(t *testing.T) {
	a, _, _ := twoFiles(t)
	shmMapT(t, a, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false) })
	node := a.shm.node
	node.mu.Lock()
	done := make(chan int, 1)
	go func() { done <- a.ShmLock(shmCkptSlot, 1, ShmLock|ShmExclusive) }()
	select {
	case <-done:
		node.mu.Unlock()
		t.Fatal("the checkpoint slot was taken while the mutex of the node was held")
	case <-time.After(100 * time.Millisecond):
	}
	node.mu.Unlock()
	lockT(t, "a exclusive checkpoint", <-done, OK)
	lockT(t, "a unlock checkpoint", a.ShmLock(shmCkptSlot, 1, ShmUnlock|ShmExclusive), OK)
}

// A connection that moved a read mark gives its slot back: alone, the
// process keeps a read lock on it, and the connection and the readers that
// come to the mark take it with no system call. With another process, the
// slot is unlocked at once.
func TestShmMarkMoveKeepsReadSlot(t *testing.T) {
	idleDelay(t, time.Hour)
	a, b, name := twoFiles(t)
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false); b.ShmUnmap(false) })
	calls := countFcntl(t)
	lockT(t, "a exclusive 4", a.ShmLock(4, 1, ShmLock|ShmExclusive), OK)
	lockT(t, "a unlock 4", a.ShmLock(4, 1, ShmUnlock|ShmExclusive), OK)
	if n := calls.Load(); n != 2 {
		t.Errorf("exclusive and its unlock made %d fcntl calls, want 2", n)
	}
	if got := heldBy(t, name, 4); got != unix.F_RDLCK {
		t.Fatalf("slot 4 after the exclusive unlock, alone = %d, want F_RDLCK", got)
	}
	calls.Reset()
	for _, f := range []*File{a, b} {
		lockT(t, "shared 4", f.ShmLock(4, 1, ShmLock|ShmShared), OK)
	}
	for _, f := range []*File{a, b} {
		lockT(t, "unlock 4", f.ShmLock(4, 1, ShmUnlock|ShmShared), OK)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("readers of the mark made %d fcntl calls, want 0", n)
	}
	// The timer releases it as it does a slot a reader left.
	a.shm.node.releaseIdle()
	if got := heldBy(t, name, 4); got != unix.F_UNLCK {
		t.Fatalf("slot 4 after the timer = %d, want F_UNLCK", got)
	}

	otherProcess(t, name)
	a.shm.node.releaseIdle()
	lockT(t, "a exclusive 4", a.ShmLock(4, 1, ShmLock|ShmExclusive), OK)
	lockT(t, "a unlock 4", a.ShmLock(4, 1, ShmUnlock|ShmExclusive), OK)
	if got := heldBy(t, name, 4); got != unix.F_UNLCK {
		t.Fatalf("slot 4 after the exclusive unlock, with another process = %d, want F_UNLCK", got)
	}
	// Several slots, as after a recovery, are unlocked at once too.
	lockT(t, "b exclusive 3..7", b.ShmLock(3, 5, ShmLock|ShmExclusive), OK)
	lockT(t, "b unlock 3..7", b.ShmLock(3, 5, ShmUnlock|ShmExclusive), OK)
	if idle := a.shm.node.idle(); idle != 0 {
		t.Errorf("idle slots = %#x, want none", idle)
	}
}

// A reader that comes to a read slot while another connection of the
// process moves its mark looks again until the slot is given back, and is
// not refused at once: SQLite sleeps after a few refusals. A slot that is
// no read slot is refused at once, and so is a read slot once the reader
// has looked shmExclTries times.
func TestShmReaderWaitsForMarkMove(t *testing.T) {
	idleDelay(t, time.Hour)
	old := shmExclTries
	t.Cleanup(func() { shmExclTries = old })
	a, b, _ := twoFiles(t)
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false); b.ShmUnmap(false) })

	lockT(t, "a exclusive 2", a.ShmLock(2, 1, ShmLock|ShmExclusive), OK)
	lockT(t, "b shared 2", b.ShmLock(2, 1, ShmLock|ShmShared), Busy)
	lockT(t, "a unlock 2", a.ShmLock(2, 1, ShmUnlock|ShmExclusive), OK)

	lockT(t, "a exclusive 4", a.ShmLock(4, 1, ShmLock|ShmExclusive), OK)
	shmExclTries = 100
	lockT(t, "b shared 4, the slot held for long", b.ShmLock(4, 1, ShmLock|ShmShared), Busy)

	shmExclTries = 1 << 62
	done := make(chan int, 1)
	go func() { done <- b.ShmLock(4, 1, ShmLock|ShmShared) }()
	select {
	case rc := <-done:
		t.Fatalf("b shared 4 while a moves the mark = %d at once, want it to look again", rc)
	case <-time.After(50 * time.Millisecond):
	}
	lockT(t, "a unlock 4", a.ShmLock(4, 1, ShmUnlock|ShmExclusive), OK)
	select {
	case rc := <-done:
		lockT(t, "b shared 4 once a gave it back", rc, OK)
	case <-time.After(5 * time.Second):
		t.Fatal("b shared 4 did not return after a gave the slot back")
	}
	lockT(t, "b unlock 4", b.ShmLock(4, 1, ShmUnlock|ShmShared), OK)
}

// While the timer lets go of a slot in the kernel, a connection that asks
// for the slot waits for it, shared or exclusive. Nobody holds the slot
// then: SQLITE_BUSY would fail a checkpoint that has nothing to wait for,
// as SQLite does not try a read slot again.
func TestShmSlotChanging(t *testing.T) {
	idleDelay(t, time.Hour)
	a, b, _ := twoFiles(t)
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false); b.ShmUnmap(false) })

	sl := &a.shm.node.slots[5]
	for _, tc := range []struct {
		name        string
		f           *File
		offset, n   int
		flags       int
		unlockFlags int
	}{
		{"b shared 5", b, 5, 1, ShmLock | ShmShared, ShmUnlock | ShmShared},
		{"a exclusive 5", a, 5, 1, ShmLock | ShmExclusive, ShmUnlock | ShmExclusive},
		{"a exclusive 4..6", a, 4, 3, ShmLock | ShmExclusive, ShmUnlock | ShmExclusive},
		{"a exclusive 3..7", a, 3, 5, ShmLock | ShmExclusive, ShmUnlock | ShmExclusive},
	} {
		// As releaseIdle does.
		sl.pass.Add(passBegin)
		sl.mu.Lock()
		done := make(chan int, 1)
		go func() { done <- tc.f.ShmLock(tc.offset, tc.n, tc.flags) }()
		select {
		case rc := <-done:
			sl.mu.Unlock()
			sl.pass.Add(passEnd)
			t.Fatalf("%s beside the timer = %d at once, want it to wait", tc.name, rc)
		case <-time.After(50 * time.Millisecond):
		}
		sl.mu.Unlock()
		sl.pass.Add(passEnd)
		lockT(t, tc.name+" after the timer", <-done, OK)
		lockT(t, tc.name+" unlock", tc.f.ShmLock(tc.offset, tc.n, tc.unlockFlags), OK)
	}
}

// A connection that takes a slot or gives it back has the mutex of the slot
// and may wait long for a core. An exclusive lock does not sleep for it: it
// looks at the slot for some microseconds and is then refused, as with a
// slot the connection holds. A writer or a reader that moves a read mark
// asks for a slot at every transaction, and would sleep at each such wait.
func TestShmExclusiveBesideConnection(t *testing.T) {
	idleDelay(t, time.Hour)
	a, b, _ := twoFiles(t)
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false); b.ShmUnmap(false) })

	sl := &a.shm.node.slots[5]
	for _, tc := range []struct {
		name      string
		offset, n int
	}{
		{"a exclusive 5", 5, 1},
		{"a exclusive 4..6", 4, 3},
		{"a exclusive 3..7", 3, 5},
	} {
		sl.mu.Lock()
		done := make(chan int, 1)
		go func() { done <- a.ShmLock(tc.offset, tc.n, ShmLock|ShmExclusive) }()
		select {
		case rc := <-done:
			sl.mu.Unlock()
			lockT(t, tc.name+" beside a connection on slot 5", rc, Busy)
		case <-time.After(2 * time.Second):
			sl.mu.Unlock()
			if rc := <-done; rc == OK {
				a.ShmLock(tc.offset, tc.n, ShmUnlock|ShmExclusive)
			}
			t.Fatalf("%s waited for a connection on slot 5", tc.name)
		}
		lockT(t, tc.name+" once the connection left", a.ShmLock(tc.offset, tc.n, ShmLock|ShmExclusive), OK)
		lockT(t, tc.name+" unlock", a.ShmLock(tc.offset, tc.n, ShmUnlock|ShmExclusive), OK)
	}
}

// An exclusive lock of several slots that is refused one of them leaves the
// others free. A connection that asks for one of those meanwhile waits and
// gets it: it is not refused a slot nobody holds.
func TestShmExclusiveBesideRefusedRun(t *testing.T) {
	idleDelay(t, time.Hour)
	a, b, _ := twoFiles(t)
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false); b.ShmUnmap(false) })
	node := a.shm.node

	// The timer has slot 6: a waits for it with the mutexes of 3 to 5. It
	// counts itself on slot 6 once it has them, just before it waits.
	node.slots[6].pass.Add(passBegin)
	node.slots[6].mu.Lock()
	run := make(chan int, 1)
	go func() { run <- a.ShmLock(3, 5, ShmLock|ShmExclusive) }()
	for node.slots[6].pass.Load()&passCount != 2 {
		runtime.Gosched()
	}

	one := make(chan int, 1)
	go func() { one <- b.ShmLock(4, 1, ShmLock|ShmExclusive) }()
	early := false
	var rc int
	select {
	case rc = <-one:
		early = true
		t.Errorf("b exclusive 4 beside a's exclusive 3..7 = %d at once, want it to wait", rc)
	case <-time.After(50 * time.Millisecond):
	}

	// A reader comes to slot 7 before a gets there: a is refused.
	node.slots[7].state.Store(slotRead | 1)
	node.slots[6].mu.Unlock()
	node.slots[6].pass.Add(passEnd)
	lockT(t, "a exclusive 3..7 with a reader on 7", <-run, Busy)
	if !early {
		rc = <-one
	}
	if rc != OK {
		t.Errorf("b exclusive 4 once a was refused = %d, want OK", rc)
	} else {
		lockT(t, "b unlock 4", b.ShmLock(4, 1, ShmUnlock|ShmExclusive), OK)
	}
	node.slots[7].state.Store(0)
	for i := 3; i < 8; i++ {
		if p := node.slots[i].pass.Load() & passCount; p != 0 {
			t.Errorf("slot %d: %d counted in pass at the end, want 0", i, p)
		}
	}
}

// A connection that has the mutex of a slot for a moment may be leaving the
// slot: an exclusive lock looks again for as long as a system call takes. It
// gets the slot when the connection left it, and is refused when the
// connection is seen to hold it.
func TestShmExclusiveLooksAgain(t *testing.T) {
	idleDelay(t, time.Hour)
	old := shmFreeTries
	t.Cleanup(func() { shmFreeTries = old })
	shmFreeTries = 1 << 62
	a, b, _ := twoFiles(t)
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false); b.ShmUnmap(false) })

	sl := &a.shm.node.slots[5]
	for _, tc := range []struct {
		name      string
		offset, n int
		taken     bool
		want      int
	}{
		{"a exclusive 5, the connection left", 5, 1, false, OK},
		{"a exclusive 5, the connection took it", 5, 1, true, Busy},
		{"a exclusive 4..6, the connection left", 4, 3, false, OK},
		{"a exclusive 4..6, the connection took it", 4, 3, true, Busy},
	} {
		sl.mu.Lock()
		done := make(chan int, 1)
		go func() { done <- a.ShmLock(tc.offset, tc.n, ShmLock|ShmExclusive) }()
		select {
		case rc := <-done:
			sl.mu.Unlock()
			t.Fatalf("%s = %d while the connection had the mutex, want it to look again", tc.name, rc)
		case <-time.After(20 * time.Millisecond):
		}
		if tc.taken {
			sl.state.Store(slotRead | 1)
		}
		sl.mu.Unlock()
		lockT(t, tc.name, <-done, tc.want)
		if tc.taken {
			sl.state.Store(0)
		} else {
			lockT(t, tc.name+" unlock", a.ShmLock(tc.offset, tc.n, ShmUnlock|ShmExclusive), OK)
		}
	}
}

// A checkpoint asks for the read slots exclusive while the timer lets go of
// the ones the last reader left: it gets them every time. SQLITE_BUSY there
// is "database is locked" from a checkpoint with no reader and no writer.
func TestShmExclusiveBesideTimer(t *testing.T) {
	idleDelay(t, 20*time.Microsecond)
	a, b, _ := twoFiles(t)
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false); b.ShmUnmap(false) })
	for i := 0; i < 20000; i++ {
		lockT(t, "b shared 4", b.ShmLock(4, 1, ShmLock|ShmShared), OK)
		lockT(t, "b unlock 4", b.ShmLock(4, 1, ShmUnlock|ShmShared), OK)
		for spin := i % 64; spin > 0; spin-- {
			runtime.Gosched()
		}
		if rc := a.ShmLock(3, 5, ShmLock|ShmExclusive); rc != OK {
			t.Fatalf("round %d: a exclusive 3..7 with no holder = %d, want OK", i, rc)
		}
		lockT(t, "a unlock 3..7", a.ShmLock(3, 5, ShmUnlock|ShmExclusive), OK)
	}
}

// An exclusive lock takes an idle slot out of its state before it asks the
// kernel, and the timer that runs just then does not see the slot. When
// the kernel refuses, as with a reader of another process on the slot, the
// slot is idle again and the timer is set: it is not left locked for ever.
func TestShmRefusedExclusiveSetsTimer(t *testing.T) {
	for _, run := range []int{1, 3} {
		idleDelay(t, time.Hour)
		a, b, name := twoFiles(t)
		shmMapT(t, a, 0, true)
		shmMapT(t, b, 0, true)
		node := a.shm.node
		lockT(t, "a shared 4", a.ShmLock(4, 1, ShmLock|ShmShared), OK)
		lockT(t, "a unlock 4", a.ShmLock(4, 1, ShmUnlock|ShmShared), OK)
		if idle := node.idle(); idle != 1<<4 {
			t.Fatalf("idle slots = %#x, want slot 4", idle)
		}
		// A reader of another process on slot 4, or on the last of the run.
		fd, err := unix.Open(name+"-shm", unix.O_RDWR|unix.O_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		var fl unix.Flock_t
		if err := setLock(fd, &fl, unix.F_RDLCK, int64(shmBase+4+run-1), 1); err != nil {
			t.Fatal(err)
		}
		ticked := false
		stop := failFcntl(t, func(typ int16, s, l int64) error {
			if typ == unix.F_WRLCK && s == shmBase+4 && !ticked {
				ticked = true
				fcntlFault.Store(nil)
				node.releaseIdle()
			}
			return nil
		})
		rc := b.ShmLock(4, run, ShmLock|ShmExclusive)
		stop()
		if rc != Busy || !ticked {
			t.Fatalf("b exclusive 4+%d beside a reader of another process = %d, timer ran %v, want SQLITE_BUSY, true", run, rc, ticked)
		}
		if idle := node.idle(); idle != 1<<4 {
			t.Errorf("idle slots after the refused lock = %#x, want slot 4", idle)
		}
		if !node.armed.Load() {
			t.Errorf("run of %d: slot 4 is idle and the timer is not set", run)
		}
		unix.Close(fd)
		node.releaseIdle()
		if got := heldBy(t, name, 4); got != unix.F_UNLCK {
			t.Errorf("slot 4 after the timer = %d, want F_UNLCK", got)
		}
		a.ShmUnmap(false)
		b.ShmUnmap(false)
		a.Close()
		b.Close()
	}
}

// idle is the slots the process has locked in the kernel with no holder.
func (node *shmNode) idle() (mask uint16) {
	for i := range node.slots {
		if v := node.slots[i].state.Load(); v == slotRead || v == slotWrite {
			mask |= 1 << i
		}
	}
	return mask
}

// holders is how many connections of the process hold slot i shared, and -1
// when one holds it exclusive.
func (node *shmNode) holders(i int) int {
	v := node.slots[i].state.Load()
	if v&slotExcl != 0 {
		return -1
	}
	return int(v & slotCount)
}

// Connections take read slots shared, single slots and runs of slots
// exclusive, while the timer releases idle slots all the time and another
// process comes and goes. No slot is held shared and exclusive at once, nor
// exclusive twice, another open file description sees every slot locked as
// it is held, and nothing is left locked at the end.
func TestShmSlotsParallelWithTimer(t *testing.T) {
	idleDelay(t, 20*time.Microsecond)
	name := filepath.Join(t.TempDir(), "par.db")
	files := make([]*File, 6)
	for i := range files {
		files[i] = openT(t, name, mainDB)
		shmMapT(t, files[i], 0, true)
	}
	node := files[0].shm.node
	var shared, excl [ShmNLock]atomic.Int32
	var took [3]atomic.Int64
	stop := make(chan struct{})
	var comeAndGo sync.WaitGroup
	comeAndGo.Add(1)
	go func() {
		defer comeAndGo.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			gone := otherProcess(t, name)
			time.Sleep(200 * time.Microsecond)
			gone()
			time.Sleep(200 * time.Microsecond)
		}
	}()
	rounds := 20000
	if testing.Short() {
		rounds = 4000
	}
	var wg sync.WaitGroup
	for g, f := range files {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fd, err := unix.Open(name+"-shm", unix.O_RDWR|unix.O_CLOEXEC, 0)
			if err != nil {
				t.Error(err)
				return
			}
			defer unix.Close(fd)
			var fl unix.Flock_t
			seen := func(slot int, want int16) {
				if typ, err := testLock(fd, &fl, int64(shmBase+slot), 1); err != nil || typ != want {
					t.Errorf("slot %d seen from outside = %d, %v, want %d", slot, typ, err, want)
				}
			}
			rnd := uint32(g*7919 + 1)
			for range rounds {
				rnd = rnd*1664525 + 1013904223
				slot := 3 + int(rnd>>8)%5
				switch op := rnd >> 28; {
				case op < 11:
					if f.ShmLock(slot, 1, ShmLock|ShmShared) != OK {
						continue
					}
					shared[slot].Add(1)
					if excl[slot].Load() != 0 {
						t.Errorf("slot %d held shared and exclusive", slot)
					}
					if op == 0 {
						seen(slot, unix.F_RDLCK)
					}
					took[0].Add(1)
					shared[slot].Add(-1)
					if rc := f.ShmLock(slot, 1, ShmUnlock|ShmShared); rc != OK {
						t.Errorf("unlock shared %d = %d", slot, rc)
					}
				default:
					n := 1
					if op == 15 {
						n = min(1+int(rnd>>4)%3, ShmNLock-slot)
					}
					if f.ShmLock(slot, n, ShmLock|ShmExclusive) != OK {
						continue
					}
					for i := slot; i < slot+n; i++ {
						if excl[i].Add(1) != 1 || shared[i].Load() != 0 {
							t.Errorf("slot %d held exclusive with other holders", i)
						}
					}
					if rnd&1 == 0 {
						seen(slot+n-1, unix.F_WRLCK)
					}
					took[min(n, 2)].Add(1)
					for i := slot; i < slot+n; i++ {
						excl[i].Add(-1)
					}
					if rc := f.ShmLock(slot, n, ShmUnlock|ShmExclusive); rc != OK {
						t.Errorf("unlock exclusive %d+%d = %d", slot, n, rc)
					}
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	comeAndGo.Wait()
	for i := range took {
		if took[i].Load() == 0 {
			t.Errorf("locks taken shared, exclusive, exclusive on a run: %d %d %d", took[0].Load(), took[1].Load(), took[2].Load())
		}
	}
	for slot := 3; slot < ShmNLock; slot++ {
		if n := node.holders(slot); n != 0 {
			t.Errorf("slot %d has %d holders with none left", slot, n)
		}
		waitUnlocked(t, name, slot)
	}
	if idle := node.idle(); idle != 0 {
		t.Errorf("idle slots at the end = %#x, want none", idle)
	}
	for slot := range node.slots {
		if p := node.slots[slot].pass.Load() & passCount; p != 0 {
			t.Errorf("slot %d: %d counted in pass at the end, want 0", slot, p)
		}
	}
	for _, f := range files {
		f.ShmUnmap(false)
	}
}
