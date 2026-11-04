// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build mattn

package bench

import (
	"net/url"

	_ "github.com/mattn/go-sqlite3"
)

// DriverName is the database/sql name the driver registers.
const DriverName = "sqlite3"

// DriverTag names the build tag that selects this driver.
const DriverTag = "mattn"

// DSN opens path with the same settings as every other driver, and
// synchronous (NORMAL or FULL).
func DSN(path, synchronous string) string {
	q := url.Values{}
	q.Set("_journal_mode", "WAL")
	q.Set("_synchronous", synchronous)
	q.Set("_busy_timeout", "5000")
	q.Set("_foreign_keys", "off")
	return "file:" + path + "?" + q.Encode()
}
