// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build turso

package bench

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	_ "turso.tech/database/tursogo"
)

// DriverName is the database/sql name of the driver below, which opens
// connections with tursogo's driver.
const DriverName = "turso-bench"

// DriverTag names the build tag that selects this driver.
const DriverTag = "turso"

// DSN opens path with the same settings as every other driver, and
// synchronous (NORMAL or FULL).
func DSN(path, synchronous string) string {
	return DSNBusy(path, synchronous, 5000)
}

// DSNBusy is DSN with a busy timeout of busyMS milliseconds.
func DSNBusy(path, synchronous string, busyMS int) string {
	q := url.Values{}
	q.Set("_busy_timeout", strconv.Itoa(busyMS))
	q.Add("_pragma", "journal_mode = WAL")
	q.Add("_pragma", "synchronous = "+synchronous)
	q.Add("_pragma", "foreign_keys = 0")
	return path + "?" + q.Encode()
}

// tursogo's DSN takes a busy timeout but no PRAGMA, and ignores what it
// does not know. So this driver runs each _pragma of the DSN on every
// connection it opens, as the DSNs of the other drivers do.
type pragmaDriver struct{ d driver.Driver }

func init() {
	db, err := sql.Open("turso", "")
	if err != nil {
		panic(err)
	}
	sql.Register(DriverName, pragmaDriver{db.Driver()})
	db.Close()
}

func (p pragmaDriver) Open(dsn string) (driver.Conn, error) {
	var pragmas []string
	if _, q, ok := strings.Cut(dsn, "?"); ok {
		v, err := url.ParseQuery(q)
		if err != nil {
			return nil, err
		}
		pragmas = v["_pragma"]
	}
	c, err := p.d.Open(dsn)
	if err != nil {
		return nil, err
	}
	for _, s := range pragmas {
		if _, err := c.(driver.ExecerContext).ExecContext(context.Background(), "PRAGMA "+s, nil); err != nil {
			c.Close()
			return nil, fmt.Errorf("PRAGMA %s: %w", s, err)
		}
	}
	return c, nil
}
