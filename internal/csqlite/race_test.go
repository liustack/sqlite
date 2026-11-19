// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build race && (linux || darwin) && (amd64 || arm64)

package csqlite

func init() { raceBuild = true }
