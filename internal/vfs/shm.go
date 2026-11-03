// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package vfs

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// shm is the wal-index of a File in WAL mode, as os_unix's unixShmNode: the
// "-shm" file next to the database, mapped MAP_SHARED in regions.
//
// os_unix shares one node between the connections of a process and counts
// their locks in memory. Here every File opens and maps the -shm itself and
// locks it with OFD locks, so connections of one process see each other
// through the kernel, as connections of different processes do. Two
// mappings of one file share their pages, so they see each other's writes.
type shm struct {
	fd       int
	path     string
	readOnly bool      // the -shm could only be opened for reading
	unlocked bool      // read-only and no one had it open: the DMS lock is not held
	regions  []uintptr // address of each region mapped
	maps     []mapping // what to unmap
	shared   uint16    // slots locked shared
	excl     uint16    // slots locked exclusive
}

type mapping struct {
	addr unsafe.Pointer
	size uintptr
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
	s := f.shm
	if s.unlocked {
		if rc := s.lockDMS(&f.fl); rc != OK {
			return 0, rc
		}
		s.unlocked = false
	}

	// With pages larger than a region, map whole pages.
	perMap := os.Getpagesize() / size
	if perMap < 1 {
		perMap = 1
	}
	want := (n + perMap) / perMap * perMap

	rc := OK
	if len(s.regions) < want {
		rc = s.mapRegions(f, want, size, perMap, extend)
	}
	var addr uintptr
	if n < len(s.regions) {
		addr = s.regions[n]
	}
	if s.readOnly && rc == OK {
		rc = ReadOnly
	}
	return addr, rc
}

// mapRegions maps regions until there are want of them, making the file
// larger first when extend is set.
func (s *shm) mapRegions(f *File, want, size, perMap int, extend bool) int {
	if err := unix.Fstat(s.fd, &f.st); err != nil {
		return IOErrShmSize
	}
	need := int64(want) * int64(size)
	if f.st.Size < need {
		if !extend {
			return OK
		}
		// Write a byte at the end of every new page, as os_unix, so that
		// the space is there now and not found missing (SIGBUS) when the
		// memory is used.
		const pgsz = 4096
		zero := []byte{0}
		for pg := f.st.Size / pgsz; pg < need/pgsz; pg++ {
			var n int
			err := retry(func() (err error) {
				n, err = unix.Pwrite(s.fd, zero, pg*pgsz+pgsz-1)
				return err
			})
			if err != nil || n != 1 {
				return IOErrShmSize
			}
		}
	}

	prot := unix.PROT_READ | unix.PROT_WRITE
	if s.readOnly {
		prot = unix.PROT_READ
	}
	for len(s.regions) < want {
		m := mapping{size: uintptr(size * perMap)}
		p, err := unix.MmapPtr(s.fd, int64(size)*int64(len(s.regions)), nil, m.size, prot, unix.MAP_SHARED)
		if err != nil {
			return IOErrShmMap
		}
		m.addr = p
		s.maps = append(s.maps, m)
		for i := range perMap {
			s.regions = append(s.regions, uintptr(p)+uintptr(i*size))
		}
	}
	return OK
}

// openShm opens the -shm file, as os_unix's unixOpenSharedMemory, and takes
// the DMS lock.
func (f *File) openShm() int {
	if err := unix.Fstat(f.fd, &f.st); err != nil {
		return IOErrFstat
	}
	mode := uint32(f.st.Mode) & 0777
	s := &shm{fd: -1, path: f.path + "-shm"}

	fd, err := openFile(s.path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	if err != nil {
		fd, err = openFile(s.path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
		if err != nil {
			return CantOpen
		}
		s.readOnly = true
	}
	s.fd = fd
	// A -shm made by root is given to the owner of the database.
	if os.Geteuid() == 0 {
		unix.Fchown(fd, int(f.st.Uid), int(f.st.Gid))
	}

	rc := s.lockDMS(&f.fl)
	if rc != OK && rc != ReadOnlyCantInit {
		unix.Close(fd)
		return rc
	}
	s.unlocked = rc == ReadOnlyCantInit
	f.shm = s
	return rc
}

// lockDMS takes a read lock on the DMS byte, as os_unix's
// unixLockSharedMemory. The DMS byte is read locked by every connection that
// has the -shm open. When no one holds it, this connection is the first, and
// the -shm may hold anything a crash left: it takes a write lock, truncates
// the file, and turns the lock into a read lock. A write lock held by
// another means that one is truncating: SQLITE_BUSY, and SQLite tries again.
func (s *shm) lockDMS(fl *unix.Flock_t) int {
	typ, err := testLock(s.fd, fl, shmDMS, 1)
	if err != nil {
		return IOErrLock
	}
	switch typ {
	case unix.F_WRLCK:
		return Busy
	case unix.F_UNLCK:
		if s.readOnly {
			return ReadOnlyCantInit
		}
		if err := setLock(s.fd, fl, unix.F_WRLCK, shmDMS, 1); err != nil {
			return Busy
		}
		// To 3 bytes and not 0, as os_unix, so that a legitimate
		// truncation can be told from the work of a stray process.
		if err := retry(func() error { return unix.Ftruncate(s.fd, 3) }); err != nil {
			return IOErrShmOpen
		}
	}
	if err := setLock(s.fd, fl, unix.F_RDLCK, shmDMS, 1); err != nil {
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
	start, size := int64(shmBase+offset), int64(n)

	switch {
	case flags&ShmUnlock != 0:
		if (s.shared|s.excl)&mask == 0 {
			return OK
		}
		if err := setLock(s.fd, &f.fl, unix.F_UNLCK, start, size); err != nil {
			return lockError(err, IOErrShmLock)
		}
		s.shared &^= mask
		s.excl &^= mask

	case flags&ShmShared != 0:
		if s.shared&mask != 0 {
			return OK
		}
		if err := setLock(s.fd, &f.fl, unix.F_RDLCK, start, size); err != nil {
			return lockError(err, IOErrShmLock)
		}
		s.shared |= mask

	default:
		if err := setLock(s.fd, &f.fl, unix.F_WRLCK, start, size); err != nil {
			return lockError(err, IOErrShmLock)
		}
		s.excl |= mask
	}
	return OK
}

// ShmUnmap unmaps the wal-index and closes the -shm file, which drops its
// locks. delete deletes the file: SQLite asks for that only while it holds
// EXCLUSIVE on the database and no other connection has the -shm open.
// Without a wal-index it does nothing.
func (f *File) ShmUnmap(delete bool) int {
	s := f.shm
	if s == nil {
		return OK
	}
	for _, m := range s.maps {
		unix.MunmapPtr(m.addr, m.size)
	}
	if delete {
		unix.Unlink(s.path)
	}
	unix.Close(s.fd)
	f.shm = nil
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
	typ, err := testLock(f.shm.fd, &f.fl, shmDMS, 1)
	return err == nil && typ != unix.F_UNLCK
}
