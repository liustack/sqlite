// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build windows && amd64

// Package vfs on windows: files through os.File, with no locks and no shared
// memory. It runs SQLite in one process with the rollback journal, no more.
// The locks and the WAL index of os_win.c come next.
package vfs

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

type File struct {
	f             *os.File
	path          string
	deleteOnClose bool
	lock          int
}

// Open opens name with SQLITE_OPEN_* flags. An empty name is a temporary
// file, deleted when closed.
func Open(name string, flags int) (*File, int, int) {
	if name == "" {
		t, err := os.CreateTemp("", "etilqs_*")
		if err != nil {
			return nil, 0, CantOpen
		}
		return &File{f: t, path: t.Name(), deleteOnClose: true}, flags, OK
	}
	of := os.O_RDONLY
	if flags&OpenReadWrite != 0 {
		of = os.O_RDWR
	}
	if flags&OpenCreate != 0 {
		of |= os.O_CREATE
	}
	if flags&OpenExclusive != 0 {
		of |= os.O_EXCL
	}
	f, err := os.OpenFile(name, of, 0o644)
	if err != nil && flags&OpenReadWrite != 0 && errors.Is(err, fs.ErrPermission) {
		f, err = os.OpenFile(name, os.O_RDONLY, 0)
		flags = flags&^(OpenReadWrite|OpenCreate) | OpenReadOnly
	}
	if err != nil {
		return nil, 0, CantOpen
	}
	return &File{f: f, path: name, deleteOnClose: flags&OpenDeleteOnClose != 0}, flags, OK
}

func (f *File) Close() int {
	err := f.f.Close()
	if f.deleteOnClose {
		os.Remove(f.path)
	}
	if err != nil {
		return IOErrClose
	}
	return OK
}

func (f *File) ReadAt(p []byte, off int64) int {
	n, err := f.f.ReadAt(p, off)
	if n == len(p) {
		return OK
	}
	if err == io.EOF || err == nil {
		clear(p[n:])
		return IOErrShortRead
	}
	return IOErrRead
}

func (f *File) WriteAt(p []byte, off int64) int {
	if _, err := f.f.WriteAt(p, off); err != nil {
		return IOErrWrite
	}
	return OK
}

func (f *File) Truncate(size int64) int {
	if err := f.f.Truncate(size); err != nil {
		return IOErrTruncate
	}
	return OK
}

func (f *File) Sync(flags int) int {
	if err := f.f.Sync(); err != nil {
		return IOErrFsync
	}
	return OK
}

func (f *File) Size() (int64, int) {
	st, err := f.f.Stat()
	if err != nil {
		return 0, IOErrFstat
	}
	return st.Size(), OK
}

func (f *File) SetChunkSize(n int)             {}
func (f *File) SizeHint(size int64) int        { return OK }
func (f *File) HasMoved() bool                 { return false }
func (f *File) SectorSize() int                { return 4096 }
func (f *File) DeviceCharacteristics() int     { return 0 }
func (f *File) Lock(level int) int             { f.lock = max(f.lock, level); return OK }
func (f *File) Unlock(level int) int           { f.lock = min(f.lock, level); return OK }
func (f *File) CheckReservedLock() (bool, int) { return false, OK }
func (f *File) LockState() int                 { return f.lock }

func (f *File) ShmMap(n, size int, extend bool) (uintptr, int) { return 0, IOErrShmOpen }
func (f *File) ShmLock(offset, n, flags int) int               { return IOErrShmLock }
func (f *File) ShmUnmap(remove bool) int                       { return OK }

func Delete(name string, syncDir bool) int {
	if err := os.Remove(name); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return IOErrDeleteNoent
		}
		return IOErrDelete
	}
	return OK
}

func Access(name string, flags int) (bool, int) {
	st, err := os.Stat(name)
	if err != nil {
		return false, OK
	}
	if flags == AccessReadWrite {
		return st.Mode().Perm()&0o200 != 0, OK
	}
	return true, OK
}

func FullPathname(name string) (string, int) {
	p, err := filepath.Abs(name)
	if err != nil {
		return "", CantOpen
	}
	return p, OK
}
