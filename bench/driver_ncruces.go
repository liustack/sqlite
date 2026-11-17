// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ncruces

package bench

import (
	"net/url"
	"strconv"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// DriverName is the database/sql name the driver registers.
const DriverName = "sqlite3"

// DriverTag names the build tag that selects this driver.
const DriverTag = "ncruces"

// DSN opens path with the same settings as every other driver, and
// synchronous (NORMAL or FULL).
func DSN(path, synchronous string) string {
	return DSNBusy(path, synchronous, 5000)
}

// DSNBusy is DSN with a busy timeout of busyMS milliseconds.
func DSNBusy(path, synchronous string, busyMS int) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous("+synchronous+")")
	q.Add("_pragma", "busy_timeout("+strconv.Itoa(busyMS)+")")
	q.Add("_pragma", "foreign_keys(0)")
	return "file:" + path + "?" + q.Encode()
}
