// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package vfs

import (
	"os"
	"sync"
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
	regions  []uintptr     // address of each region mapped
	maps     []mapping     // what to unmap
	fl       unix.Flock_t  // space for the fcntl argument
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

	fd, err := openFile(node.path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
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
			s.excl |= mask
		}
	}
	node.mu.Unlock()
	return rc
}

// unlock releases the slots of mask, n from offset, that s holds. A shared
// slot stays locked in the kernel while other connections of the process
// hold it. node.mu is held.
func (s *shm) unlock(offset, n int, mask uint16) int {
	node := s.node
	for i := offset; i < offset+n; i++ {
		bit := uint16(1) << i
		switch {
		case s.excl&bit != 0:
		case s.shared&bit != 0 && node.lock[i] > 1:
			node.lock[i]--
			s.shared &^= bit
			mask &^= bit
		case s.shared&bit == 0:
			mask &^= bit
		}
	}
	if mask == 0 {
		return OK
	}
	// Unlock the run of slots left in one call when it is one run, which it
	// is for everything SQLite asks.
	lo, hi := offset, offset+n
	for lo < hi && mask&(1<<lo) == 0 {
		lo++
	}
	for hi > lo && mask&(1<<(hi-1)) == 0 {
		hi--
	}
	if run := uint16(1<<hi - 1<<lo); run == mask {
		if err := setLock(node.fd, &node.fl, unix.F_UNLCK, int64(shmBase+lo), int64(hi-lo)); err != nil {
			return lockError(err, IOErrShmLock)
		}
	} else {
		for i := lo; i < hi; i++ {
			if mask&(1<<i) == 0 {
				continue
			}
			if err := setLock(node.fd, &node.fl, unix.F_UNLCK, int64(shmBase+i), 1); err != nil {
				return lockError(err, IOErrShmLock)
			}
		}
	}
	for i := lo; i < hi; i++ {
		if mask&(1<<i) != 0 {
			node.lock[i] = 0
		}
	}
	s.shared &^= mask
	s.excl &^= mask
	return OK
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
