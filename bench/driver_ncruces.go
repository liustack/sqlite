// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ncruces

package bench

import (
	"net/url"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// DriverName is the database/sql name the driver registers.
const DriverName = "sqlite3"

// DriverTag names the build tag that selects this driver.
const DriverTag = "ncruces"

// DSN opens path with the same settings as every other driver.
func DSN(path string) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(0)")
	return "file:" + path + "?" + q.Encode()
}
