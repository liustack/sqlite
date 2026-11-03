// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package vfs

import "golang.org/x/sys/unix"

// The lock levels are made of byte locks, as os_unix's unixLock:
//
//	SHARED     read lock on the shared range, taken while holding a read
//	           lock on the pending byte
//	RESERVED   SHARED, and a write lock on the reserved byte
//	PENDING    RESERVED, and a write lock on the pending byte: no new SHARED
//	EXCLUSIVE  PENDING (or SHARED), and a write lock on the shared range
//
// The locks are on the open file description, so every File is its own lock
// owner, in this process or another. Nothing ever waits: a conflict is
// SQLITE_BUSY.

// Lock raises the lock to level: SHARED, RESERVED or EXCLUSIVE. SQLite
// never asks for PENDING. When EXCLUSIVE fails from RESERVED, the File keeps
// PENDING, so that the next try does not race new readers.
func (f *File) Lock(level int) int {
	if f.lock >= level || f.noLock {
		return OK
	}

	// SHARED, and EXCLUSIVE from RESERVED, first take the pending byte.
	if level == LockShared || level == LockExclusive && f.lock == LockReserved {
		typ := int16(unix.F_WRLCK)
		if level == LockShared {
			typ = unix.F_RDLCK
		}
		if err := setLock(f.fd, &f.fl, typ, pendingByte, 1); err != nil {
			return lockError(err, IOErrLock)
		}
		if level == LockExclusive {
			f.lock = LockPending
		}
	}

	switch {
	case level == LockShared:
		rc := OK
		if err := setLock(f.fd, &f.fl, unix.F_RDLCK, sharedFirst, sharedSize); err != nil {
			rc = lockError(err, IOErrLock)
		}
		// The read lock on the pending byte was only needed to get in.
		if err := setLock(f.fd, &f.fl, unix.F_UNLCK, pendingByte, 1); err != nil && rc == OK {
			rc = IOErrUnlock
		}
		if rc != OK {
			return rc
		}

	case f.shmShared():
		// In WAL mode, another connection still has the -shm open.
		return Busy

	default:
		start, n := int64(reservedByte), int64(1)
		if level == LockExclusive {
			start, n = sharedFirst, sharedSize
		}
		if err := setLock(f.fd, &f.fl, unix.F_WRLCK, start, n); err != nil {
			return lockError(err, IOErrLock)
		}
	}
	f.lock = level
	return OK
}

// Unlock lowers the lock to level: SHARED or NONE.
func (f *File) Unlock(level int) int {
	if f.lock <= level || f.noLock {
		return OK
	}
	if f.lock > LockShared {
		if level == LockShared {
			// Turn the write lock on the shared range back into a read
			// lock. This cannot conflict: another owner that does is not
			// keeping to the protocol.
			if err := setLock(f.fd, &f.fl, unix.F_RDLCK, sharedFirst, sharedSize); err != nil {
				return IOErrRdlock
			}
		}
		// The pending and reserved bytes.
		if err := setLock(f.fd, &f.fl, unix.F_UNLCK, pendingByte, 2); err != nil {
			return IOErrUnlock
		}
		f.lock = LockShared
	}
	if level == LockNone {
		if err := setLock(f.fd, &f.fl, unix.F_UNLCK, 0, 0); err != nil {
			return IOErrUnlock
		}
		f.lock = LockNone
	}
	return OK
}

// CheckReservedLock tells whether some File, this one or another, holds
// RESERVED or more.
func (f *File) CheckReservedLock() (bool, int) {
	if f.noLock {
		return false, OK
	}
	if f.lock > LockShared {
		return true, OK
	}
	typ, err := testLock(f.fd, &f.fl, reservedByte, 1)
	if err != nil {
		return false, IOErrCheckReservedLock
	}
	return typ != unix.F_UNLCK, OK
}

// LockState is the lock level held, for SQLITE_FCNTL_LOCKSTATE.
func (f *File) LockState() int {
	return f.lock
}

// setLock sets a lock of typ (F_RDLCK, F_WRLCK or F_UNLCK) on n bytes from
// start, without waiting. n 0 is to the end of the file. fl is space for the
// argument.
func setLock(fd int, fl *unix.Flock_t, typ int16, start, n int64) error {
	*fl = unix.Flock_t{Type: typ, Whence: 0, Start: start, Len: n}
	for {
		err := unix.FcntlFlock(uintptr(fd), fOFDSetlk, fl)
		if err != unix.EINTR {
			return err
		}
	}
}

// testLock tells which lock another owner holds that would stop a write
// lock on n bytes from start: F_UNLCK, F_RDLCK or F_WRLCK. The locks of
// this open file description do not count.
func testLock(fd int, fl *unix.Flock_t, start, n int64) (int16, error) {
	*fl = unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: start, Len: n}
	for {
		err := unix.FcntlFlock(uintptr(fd), fOFDGetlk, fl)
		if err == nil {
			return fl.Type, nil
		}
		if err != unix.EINTR {
			return 0, err
		}
	}
}

// lockError is os_unix's sqliteErrorFromPosixError: SQLITE_BUSY for a lock
// held by another owner, def for the rest.
func lockError(err error, def int) int {
	switch err {
	case unix.EACCES, unix.EAGAIN, unix.ETIMEDOUT, unix.EBUSY, unix.EINTR, unix.ENOLCK:
		return Busy
	case unix.EPERM:
		return Perm
	}
	return def
}
