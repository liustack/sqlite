// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build !windows

package capture

import "os"

// rename moves a staged file to its name. syncDir makes it durable.
func rename(from, to string) error { return os.Rename(from, to) }
