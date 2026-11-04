// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package sqlite

import (
	"testing"
	"time"
	_ "time/tzdata" // the zone below, on any machine
)

// TestLocaltime: the localtime and utc modifiers follow Go's time.Local,
// summer time included.
func TestLocaltime(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	saved := time.Local
	time.Local = ny
	defer func() { time.Local = saved }()

	db := mOpen(t, ":memory:")
	for _, c := range []struct{ q, want string }{
		{`SELECT datetime('2026-01-15 12:00:00', 'localtime')`, "2026-01-15 07:00:00"},
		{`SELECT datetime('2026-07-15 12:00:00', 'localtime')`, "2026-07-15 08:00:00"},
		{`SELECT datetime('2026-07-15 08:00:00', 'utc')`, "2026-07-15 12:00:00"},
		{`SELECT datetime(0, 'unixepoch', 'localtime')`, "1969-12-31 19:00:00"},
		// Outside 1970 to 2037 SQLite maps the year into that range.
		{`SELECT datetime('2100-07-15 12:00:00', 'localtime')`, "2100-07-15 08:00:00"},
	} {
		var got string
		if err := db.QueryRow(c.q).Scan(&got); err != nil {
			t.Fatalf("%s: %v", c.q, err)
		}
		if got != c.want {
			t.Errorf("%s = %s, want %s", c.q, got, c.want)
		}
	}
}
