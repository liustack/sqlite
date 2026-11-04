// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package vfs

import (
	"math/bits"
	"os"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// shmNode is the wal-index of one database in this process, as os_unix's
// unixShmNode: the "-shm" file next to the database, opened once and mapped
// MAP_SHARED in regions, shared by all the Files of that database.
//
// The node's descriptor holds the process's locks on the -shm, and the node
// counts which connection of the process holds which slot. A connection
// conflicting with another of the process is refused here, and the kernel
// is asked only when the process's lock on a slot changes. With one lock
// owner per process, the kernel's lock list on the file stays short, and
// most read marks are taken with no system call at all.
type shmNode struct {
	id   fileID // of the database file
	path string
	fd   int
	nRef int // Files using the node, under shmNodes

	mu       sync.Mutex
	readOnly bool          // the -shm could only be opened for reading
	unlocked bool          // read-only and no one had it open: the DMS lock is not held
	lock     [ShmNLock]int // per slot: shared holders in the process, or -1 for exclusive
	idle     uint16        // read slots the process keeps read locked with no holder
	others   bool          // another process had the -shm open when last looked: no idle slots
	regions  []uintptr     // address of each region mapped
	maps     []mapping     // what to unmap
	fl       unix.Flock_t  // space for the fcntl argument
	timer    *time.Timer   // releases the idle slots
	armed    bool          // timer is set to run
	closed   bool          // the descriptor is closed: the timer does nothing
}

type fileID struct{ dev, ino uint64 }

type mapping struct {
	addr unsafe.Pointer
	size uintptr
}

// shmNodes are the open nodes, by database file. The mutex is held to find,
// make, count and close nodes, as os_unix's unixBigLock.
var shmNodes struct {
	sync.Mutex
	m map[fileID]*shmNode
}

// shm is a File's use of the node, as os_unix's unixShm: the slots this
// connection holds.
type shm struct {
	node   *shmNode
	shared uint16 // slots locked shared
	excl   uint16 // slots locked exclusive
}

// ShmMap returns the address of region n of the wal-index, each region size
// bytes. When the region is not there yet, extend makes it, and without
// extend the address is 0 and the result OK. The memory stays mapped until
// ShmUnmap. With a -shm that can only be read, the result is SQLITE_READONLY
// with the address, or SQLITE_READONLY_CANTINIT when no other connection has
// it open.
func (f *File) ShmMap(n, size int, extend bool) (uintptr, int) {
	if f.shm == nil {
		if rc := f.openShm(); rc != OK {
			return 0, rc
		}
	}
	node := f.shm.node
	node.mu.Lock()
	defer node.mu.Unlock()
	if node.unlocked {
		if rc := node.lockDMS(); rc != OK {
			return 0, rc
		}
		node.unlocked = false
	}

	// With pages larger than a region, map whole pages.
	perMap := os.Getpagesize() / size
	if perMap < 1 {
		perMap = 1
	}
	want := (n + perMap) / perMap * perMap

	rc := OK
	if len(node.regions) < want {
		rc = node.mapRegions(want, size, perMap, extend)
	}
	var addr uintptr
	if n < len(node.regions) {
		addr = node.regions[n]
	}
	if node.readOnly && rc == OK {
		rc = ReadOnly
	}
	return addr, rc
}

// mapRegions maps regions until there are want of them, making the file
// larger first when extend is set. node.mu is held.
func (node *shmNode) mapRegions(want, size, perMap int, extend bool) int {
	var st unix.Stat_t
	if err := unix.Fstat(node.fd, &st); err != nil {
		return IOErrShmSize
	}
	need := int64(want) * int64(size)
	if st.Size < need {
		if !extend {
			return OK
		}
		// Write a byte at the end of every new page, as os_unix, so that
		// the space is there now and not found missing (SIGBUS) when the
		// memory is used.
		const pgsz = 4096
		zero := []byte{0}
		for pg := st.Size / pgsz; pg < need/pgsz; pg++ {
			var n int
			err := retry(func() (err error) {
				n, err = unix.Pwrite(node.fd, zero, pg*pgsz+pgsz-1)
				return err
			})
			if err != nil || n != 1 {
				return IOErrShmSize
			}
		}
	}

	prot := unix.PROT_READ | unix.PROT_WRITE
	if node.readOnly {
		prot = unix.PROT_READ
	}
	for len(node.regions) < want {
		m := mapping{size: uintptr(size * perMap)}
		p, err := unix.MmapPtr(node.fd, int64(size)*int64(len(node.regions)), nil, m.size, prot, unix.MAP_SHARED)
		if err != nil {
			return IOErrShmMap
		}
		m.addr = p
		node.maps = append(node.maps, m)
		for i := range perMap {
			node.regions = append(node.regions, uintptr(p)+uintptr(i*size))
		}
	}
	return OK
}

// openShm attaches the File to the node of its database, as os_unix's
// unixOpenSharedMemory. The first File of the process opens the -shm and
// takes the DMS lock. The others find the node.
func (f *File) openShm() int {
	id := fileID{f.dev, f.ino}
	shmNodes.Lock()
	defer shmNodes.Unlock()

	rc := OK
	node := shmNodes.m[id]
	if node == nil {
		if node, rc = newShmNode(f, id); rc != OK && rc != ReadOnlyCantInit {
			return rc
		}
		if shmNodes.m == nil {
			shmNodes.m = make(map[fileID]*shmNode)
		}
		shmNodes.m[id] = node
	}
	node.nRef++
	f.shm = &shm{node: node}
	return rc
}

// newShmNode opens the -shm of f and takes the DMS lock.
func newShmNode(f *File, id fileID) (*shmNode, int) {
	if err := unix.Fstat(f.fd, &f.st); err != nil {
		return nil, IOErrFstat
	}
	mode := uint32(f.st.Mode) & 0777
	node := &shmNode{id: id, path: f.path + "-shm", fd: -1}

	// As os_unix, readonly_shm=1 opens it for reading only at once, and
	// one that cannot be written is opened for reading only.
	fd, err := -1, error(unix.EACCES)
	if !f.roShm {
		fd, err = openFile(node.path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	}
	if err != nil {
		fd, err = openFile(node.path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
		if err != nil {
			return nil, CantOpen
		}
		node.readOnly = true
	}
	node.fd = fd
	// A -shm made by root is given to the owner of the database.
	if os.Geteuid() == 0 {
		unix.Fchown(fd, int(f.st.Uid), int(f.st.Gid))
	}

	rc := node.lockDMS()
	if rc != OK && rc != ReadOnlyCantInit {
		unix.Close(fd)
		return nil, rc
	}
	node.unlocked = rc == ReadOnlyCantInit
	return node, rc
}

// lockDMS takes a read lock on the DMS byte, as os_unix's
// unixLockSharedMemory. The DMS byte is read locked by every process that
// has the -shm open. When no one holds it, this process is the first, and
// the -shm may hold anything a crash left: it takes a write lock, truncates
// the file, and turns the lock into a read lock. A write lock held by
// another means that one is truncating: SQLITE_BUSY, and SQLite tries again.
func (node *shmNode) lockDMS() int {
	typ, err := testLock(node.fd, &node.fl, shmDMS, 1)
	if err != nil {
		return IOErrLock
	}
	node.others = typ != unix.F_UNLCK
	switch typ {
	case unix.F_WRLCK:
		return Busy
	case unix.F_UNLCK:
		if node.readOnly {
			return ReadOnlyCantInit
		}
		if err := setLock(node.fd, &node.fl, unix.F_WRLCK, shmDMS, 1); err != nil {
			return Busy
		}
		// To 3 bytes and not 0, as os_unix, so that a legitimate
		// truncation can be told from the work of a stray process.
		if err := retry(func() error { return unix.Ftruncate(node.fd, 3) }); err != nil {
			return IOErrShmOpen
		}
	}
	if err := setLock(node.fd, &node.fl, unix.F_RDLCK, shmDMS, 1); err != nil {
		return Busy
	}
	return OK
}

// ShmLock locks or unlocks n slots of the wal-index from offset, as
// os_unix's unixShmLock. flags is SQLITE_SHM_LOCK or SQLITE_SHM_UNLOCK with
// SQLITE_SHM_SHARED or SQLITE_SHM_EXCLUSIVE. A shared lock is on one slot.
// SQLite never asks for a lock it holds, nor moves a slot between shared
// and exclusive without unlocking it first.
func (f *File) ShmLock(offset, n, flags int) int {
	s := f.shm
	if s == nil {
		return IOErrShmLock
	}
	mask := uint16(1<<(offset+n) - 1<<offset)
	node := s.node
	node.mu.Lock()
	rc := OK
	switch {
	case flags&ShmUnlock != 0:
		if (s.shared|s.excl)&mask != 0 {
			rc = s.unlock(offset, n, mask)
		}

	case flags&ShmShared != 0:
		if s.shared&mask != 0 {
			break
		}
		switch {
		case node.lock[offset] < 0:
			// Another connection of the process holds it exclusive.
			rc = Busy
		case node.lock[offset] == 0 && node.idle&mask != 0:
			// The process still holds it from its last reader.
			node.idle &^= mask
		case node.lock[offset] == 0:
			if err := setLock(node.fd, &node.fl, unix.F_RDLCK, int64(shmBase+offset), 1); err != nil {
				rc = lockError(err, IOErrShmLock)
			}
		}
		if rc == OK {
			node.lock[offset]++
			s.shared |= mask
		}

	default:
		for _, l := range node.lock[offset : offset+n] {
			if l != 0 {
				// Another connection of the process holds one of them.
				rc = Busy
				break
			}
		}
		if rc == OK {
			if err := setLock(node.fd, &node.fl, unix.F_WRLCK, int64(shmBase+offset), int64(n)); err != nil {
				rc = lockError(err, IOErrShmLock)
			}
		}
		if rc == OK {
			for i := offset; i < offset+n; i++ {
				node.lock[i] = -1
			}
			// An idle read lock of the process became this write lock.
			node.idle &^= mask
			s.excl |= mask
		}
	}
	node.mu.Unlock()
	return rc
}

// unlock releases the slots of mask, n from offset, that s holds. A shared
// slot stays locked in the kernel while other connections of the process
// hold it, and a read slot stays locked a little longer while no other
// process has the -shm open (see shmReadSlots). node.mu is held.
func (s *shm) unlock(offset, n int, mask uint16) int {
	node := s.node
	arm := false
	for i := offset; i < offset+n; i++ {
		bit := uint16(1) << i
		switch {
		case s.excl&bit != 0:
		case s.shared&bit != 0 && node.lock[i] > 1:
			node.lock[i]--
			s.shared &^= bit
			mask &^= bit
		case s.shared&bit != 0 && bit&shmReadSlots != 0:
			// The last reader of the process leaves a read slot. Alone,
			// the process keeps it. Either way the timer looks again for
			// other processes.
			arm = true
			if !node.others {
				node.lock[i] = 0
				node.idle |= bit
				s.shared &^= bit
				mask &^= bit
			}
		case s.shared&bit == 0:
			mask &^= bit
		}
	}
	if arm {
		node.armIdle()
	}
	if mask == 0 {
		return OK
	}
	// A slot whose unlock failed is still locked in the kernel, and stays
	// held by s.
	done, err := node.unlockSlots(mask)
	for i := offset; i < offset+n; i++ {
		if done&(1<<i) != 0 {
			node.lock[i] = 0
		}
	}
	s.shared &^= done
	s.excl &^= done
	if err != nil {
		return lockError(err, IOErrShmLock)
	}
	return OK
}

// unlockSlots unlocks the slots of mask in the kernel, one call for each run
// of slots next to each other. Everything SQLite asks is one run. done is
// the slots unlocked: a call that fails leaves its slots locked, and err is
// the first failure. node.mu is held.
func (node *shmNode) unlockSlots(mask uint16) (done uint16, err error) {
	for rest := mask; rest != 0; {
		lo := bits.TrailingZeros16(rest)
		hi := lo + bits.TrailingZeros16(^(rest >> lo))
		run := uint16(1<<hi - 1<<lo)
		rest &^= run
		if e := setLock(node.fd, &node.fl, unix.F_UNLCK, int64(shmBase+lo), int64(hi-lo)); e != nil {
			if err == nil {
				err = e
			}
			continue
		}
		done |= run
	}
	return done, err
}

// The read slots, WAL_READ_LOCK(0) to WAL_READ_LOCK(4), are released late
// while no other process has the -shm open.
//
// A read transaction takes one of them shared and gives it back. When the
// process's last reader gives it back, the kernel lock is kept and the slot
// marked idle, so that the next reader of the process takes it again with
// no system call. A timer releases the idle slots shmIdleDelay later, and
// runs at most once per delay, however many transactions there are.
//
// Keeping the lock only stops aReadMark of the slot from changing, the
// guarantee a live reader holds, so the check walTryBeginRead makes after
// taking the lock stays valid, and no one reads wrong data. Connections of
// this process are not slowed at all: an exclusive lock of the process on
// an idle slot turns the kept read lock into a write lock.
//
// Another process is slowed. With readers here less than shmIdleDelay
// apart, a slot would be locked nearly all the time: the checkpoints of
// the other process would rarely be complete and its WAL would grow
// without bound. So the slots are kept only while no other process has the
// -shm open. Every process that has it open, this package or os_unix,
// holds a read lock on the DMS byte, and a test of that byte on the node's
// descriptor sees the locks of every other owner, OFD or POSIX, but not
// its own. The test is made when the -shm is opened and at every tick of
// the timer, which also runs after the last reader of a slot leaves while
// other processes are there. With another process, read slots are released
// at once, as os_unix does.
//
// A process that opens the -shm just after a test sees an idle slot of
// this process locked until the next tick at the latest: shmIdleDelay
// after the slot went idle, and later only by how late the Go runtime runs
// the timer. From that tick on, this process releases read slots at once.
//
// WRITE, CKPT and RECOVER (slots 0 to 2) are always released at once.
const shmReadSlots = uint16(1<<ShmNLock - 1<<3)

// shmIdleDelay is how long idle read slots stay locked. Tests change it.
var shmIdleDelay = time.Millisecond

// armIdle sets the timer to release the idle slots, if it is not set.
// node.mu is held.
func (node *shmNode) armIdle() {
	if node.armed {
		return
	}
	node.armed = true
	if node.timer == nil {
		node.timer = time.AfterFunc(shmIdleDelay, node.releaseIdle)
	} else {
		node.timer.Reset(shmIdleDelay)
	}
}

// releaseIdle looks whether other processes have the -shm open, and unlocks
// in the kernel the read slots no connection of the process holds. The
// timer runs it.
func (node *shmNode) releaseIdle() {
	node.mu.Lock()
	defer node.mu.Unlock()
	node.armed = false
	if node.closed {
		return
	}
	// When the test fails, act as if another process were there.
	typ, err := testLock(node.fd, &node.fl, shmDMS, 1)
	node.others = err != nil || typ != unix.F_UNLCK
	if node.idle == 0 {
		return
	}
	// An idle slot has no holder: taking it shared or exclusive clears
	// its idle mark. A slot whose unlock failed keeps its kernel lock and
	// its mark, and the next tick tries again.
	done, _ := node.unlockSlots(node.idle)
	node.idle &^= done
	if node.idle != 0 {
		node.armIdle()
	}
}

// ShmUnmap detaches the File from the wal-index and releases the slots it
// still holds. The last File of the process unmaps the memory and closes
// the -shm, which drops the DMS lock. remove then deletes the file: SQLite
// asks for that only while it holds EXCLUSIVE on the database and no other
// connection has the -shm open. Without a wal-index it does nothing.
func (f *File) ShmUnmap(remove bool) int {
	s := f.shm
	if s == nil {
		return OK
	}
	f.shm = nil
	node := s.node
	if held := s.shared | s.excl; held != 0 {
		node.mu.Lock()
		s.unlock(0, ShmNLock, held)
		node.mu.Unlock()
	}

	shmNodes.Lock()
	defer shmNodes.Unlock()
	node.nRef--
	if node.nRef > 0 {
		return OK
	}
	delete(shmNodes.m, node.id)
	// Closing the descriptor drops the idle read locks with the rest.
	node.mu.Lock()
	node.closed = true
	if node.timer != nil {
		node.timer.Stop()
	}
	node.mu.Unlock()
	for _, m := range node.maps {
		unix.MunmapPtr(m.addr, m.size)
	}
	if remove {
		unix.Unlink(node.path)
	}
	unix.Close(node.fd)
	return OK
}

// shmShared is os_unix's unixIsSharingShmNode: whether another connection,
// in this process or another, has the -shm of this File open. Then the
// database must not be locked RESERVED or EXCLUSIVE in WAL mode, or a
// connection closing would delete the WAL and -shm under the other.
func (f *File) shmShared() bool {
	if f.shm == nil {
		return false
	}
	node := f.shm.node
	shmNodes.Lock()
	others := node.nRef > 1
	shmNodes.Unlock()
	if others {
		return true
	}
	node.mu.Lock()
	typ, err := testLock(node.fd, &node.fl, shmDMS, 1)
	node.mu.Unlock()
	return err == nil && typ != unix.F_UNLCK
}
