// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && arm64)

package csqlite

// makeRoom does nothing here. Linux and darwin write the state of the CPU at
// a fault on a stack of their own, and on windows/arm64 that state fits in
// the 4 KiB Go keeps below each stack (room_windows_amd64.go,
// docs/architecture.md).
func makeRoom() {}
