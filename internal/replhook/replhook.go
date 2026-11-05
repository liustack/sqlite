// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

// Package replhook gives the replicator (replica/internal/capture) what it
// needs of the driver and the driver does not offer its users. Package
// sqlite fills it in as it is initialized.
package replhook

import "context"

// Attach makes every connection the driver opens to the database file path
// from then on ready for replication: no automatic checkpoints, and the
// PRAGMAs that would checkpoint refused (csqlite.Conn.SetGuard). path is
// the name SQLite gives the file (csqlite.Conn.Filename). It fails when the
// driver has a connection to path open, or path is attached already. detach
// ends it: connections opened after it are as before.
var Attach func(path string) (detach func(), err error)

// Lock takes the driver's write lock of an attached path, as a writer does,
// waiting until ctx is done: until unlock, no connection of this process
// writes to the file.
var Lock func(ctx context.Context, path string) (unlock func(), err error)
