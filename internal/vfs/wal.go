// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package vfs

import "sync/atomic"

// stoppedWALs counts the WALs of the process that a failed sync stopped and
// that a File still has open.
var stoppedWALs atomic.Int64

// WALsStopped says a failed sync stopped a WAL that a File of the process
// still has open. It opens again only once all of them are closed: see
// wal_unix.go.
func WALsStopped() bool { return stoppedWALs.Load() > 0 }
