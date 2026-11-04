// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package sqlite_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestURIModeOf(t *testing.T) {
	// modeof= in a file: URI makes a new database with the mode of that
	// file, as os_unix. Windows has no file modes, and os_win ignores it.
	if runtime.GOOS == "windows" {
		t.Skip("no file modes")
	}
	dir := t.TempDir()
	ref := filepath.Join(dir, "ref")
	if err := os.WriteFile(ref, nil, 0600); err != nil {
		t.Fatal(err)
	}
	os.Chmod(ref, 0600)
	name := filepath.Join(dir, "new.db")
	db, err := sql.Open("sqlite", "file:"+name+"?modeof="+ref)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t(x)`); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0600 {
		t.Errorf("mode = %v, want 0600", got)
	}
}
