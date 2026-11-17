// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build mattn

package bench

import (
	"net/url"
	"strconv"

	_ "github.com/mattn/go-sqlite3"
)

// DriverName is the database/sql name the driver registers.
const DriverName = "sqlite3"

// DriverTag names the build tag that selects this driver.
const DriverTag = "mattn"

// DSN opens path with the same settings as every other driver, and
// synchronous (NORMAL or FULL).
func DSN(path, synchronous string) string {
	return DSNBusy(path, synchronous, 5000)
}

// DSNBusy is DSN with a busy timeout of busyMS milliseconds.
func DSNBusy(path, synchronous string, busyMS int) string {
	q := url.Values{}
	q.Set("_journal_mode", "WAL")
	q.Set("_synchronous", synchronous)
	q.Set("_busy_timeout", strconv.Itoa(busyMS))
	q.Set("_foreign_keys", "off")
	return "file:" + path + "?" + q.Encode()
}
