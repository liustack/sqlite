// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package csqlite

// goasm_room, generated in engine_room_windows_amd64.s, does nothing, but
// its stack check makes gen.RoomBelow bytes of room below the stack of its
// caller, as the room check of every generated function does on
// windows/amd64: there the system writes the state of the CPU at a fault
// below the stack pointer, and Go keeps only 4 KiB for it (golang/go#81238,
// docs/architecture.md).
func goasm_room()
