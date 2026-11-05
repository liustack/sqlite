// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build !windows

package capture

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes the lock of f, without waiting.
func lockFile(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return errStagedElsewhere
	}
	return err
}
