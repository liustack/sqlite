// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build windows

package capture

import (
	"os"

	"golang.org/x/sys/windows"
)

// rename moves a staged file to its name, with MOVEFILE_WRITE_THROUGH, and
// syncDir flushes the folder after it.
func rename(from, to string) error {
	f, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	t, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(f, t, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}

// syncDirFS is syncDir: FlushFileBuffers of the folder, which NTFS takes for
// a handle opened with FILE_FLAG_BACKUP_SEMANTICS and write access. rename
// writes through already: this covers the removals too.
func syncDirFS(dir string) error {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: dir, Err: err}
	}
	err = windows.FlushFileBuffers(h)
	if cerr := windows.CloseHandle(h); err == nil {
		err = cerr
	}
	if err != nil {
		return &os.PathError{Op: "sync", Path: dir, Err: err}
	}
	return nil
}
