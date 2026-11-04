// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package csqlite

import (
	"path/filepath"
	"strings"
	"testing"
)

// A large value is read from its overflow pages straight into the buffer
// that holds it, as with os_unix and os_win, and not page by page through
// the page cache: the file says it may be read in parts of a page
// (SQLITE_IOCAP_SUBPAGE_READ).
func TestOverflowReadSkipsPageCache(t *testing.T) {
	name := filepath.Join(t.TempDir(), "overflow.db")
	text := strings.Repeat("x", 200_000) // about 50 overflow pages of 4 KiB
	w := open(t, name)
	if err := w.Exec(`CREATE TABLE t(v TEXT); INSERT INTO t VALUES('` + text + `')`); err != nil {
		t.Fatal(err)
	}

	c := open(t, name)
	s, _, err := c.Prepare(`SELECT v FROM t`)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Finalize()
	if row, err := s.Step(); err != nil || !row {
		t.Fatalf("Step = %v, %v", row, err)
	}
	if got := s.ColumnText(0); got != text {
		t.Fatalf("read %d bytes, want %d", len(got), len(text))
	}
	a := c.args()
	cur, hi := a.word(), a.word()
	sqlite3_db_status(c.sp, c.db, 8, cur, hi, 0) // SQLITE_DBSTATUS_CACHE_MISS
	// The schema, the root of t and the first overflow page come through the
	// cache. The rest must not.
	if miss := *(*int32)(at(cur)); miss > 5 {
		t.Errorf("%d pages read through the page cache, want at most 5", miss)
	}
}
