// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package powerloss

import (
	"fmt"
	"path/filepath"

	"github.com/liustack/sqlite/internal/vfs"
)

// Apply does c, a change the VFS told of (vfs.SetTrace), to the file of
// that name. seq is the place of c in the log. The VFS tells of no
// directory sync where the system has none (NoDirSync).
func (d *Disk) Apply(seq int, c vfs.Change) (err error) {
	name := filepath.Base(c.Path)
	switch c.Kind {
	case vfs.ChangeCreate:
		d.Create(name)
	case vfs.ChangeDirSync:
		d.SyncDir()
	case vfs.ChangeWrite:
		err = d.Write(seq, name, c.Off, c.Data)
	case vfs.ChangeTruncate:
		err = d.Truncate(seq, name, c.Off)
	case vfs.ChangeSync:
		err = d.Sync(name)
	case vfs.ChangeDelete:
		err = d.Delete(name)
	}
	if err != nil {
		return fmt.Errorf("entry %d: change %d: %w", seq, c.Kind, err)
	}
	return nil
}
