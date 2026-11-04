// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build modernc

package bench

import (
	"net/url"

	_ "modernc.org/sqlite"
)

// DriverName is the database/sql name the driver registers.
const DriverName = "sqlite"

// DriverTag names the build tag that selects this driver.
const DriverTag = "modernc"

// DSN opens path with the same settings as every other driver, and
// synchronous (NORMAL or FULL).
func DSN(path, synchronous string) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous("+synchronous+")")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(0)")
	return "file:" + path + "?" + q.Encode()
}
