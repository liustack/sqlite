// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite_test

import (
	"database/sql"
	"errors"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/liustack/sqlite"
)

// A program reads the result code of a failed statement from sqlite.Error.
func TestErrorCodes(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t(x UNIQUE); INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO t VALUES (1)`)
	var e *sqlite.Error
	if !errors.As(err, &e) {
		t.Fatalf("%v (%T) is not a *sqlite.Error", err, err)
	}
	if e.Code() != sqlite.CONSTRAINT || e.ExtendedCode() != sqlite.CONSTRAINT_UNIQUE {
		t.Errorf("code %d, extended %d: want %d, %d", e.Code(), e.ExtendedCode(), sqlite.CONSTRAINT, sqlite.CONSTRAINT_UNIQUE)
	}
	if !strings.Contains(e.Error(), "UNIQUE constraint failed") {
		t.Errorf("message %q", e.Error())
	}
}

// codes.go names every error code of engine/sqlite3.h with its value, so a
// new SQLite that adds one fails here until it is added.
func TestErrorCodesMatchHeader(t *testing.T) {
	h, err := os.ReadFile("engine/sqlite3.h")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{}
	prim := regexp.MustCompile(`(?m)^#define SQLITE_([A-Z]+) +(\d+) +/\*`)
	inErrors := false
	for _, line := range strings.Split(string(h), "\n") {
		switch {
		case strings.Contains(line, "beginning-of-error-codes"):
			inErrors = true
		case strings.Contains(line, "end-of-error-codes"):
			inErrors = false
		case inErrors:
			if m := prim.FindStringSubmatch(line); m != nil && m[1] != "ROW" && m[1] != "DONE" {
				want[m[1]], _ = strconv.Atoi(m[2])
			}
		}
	}
	ext := regexp.MustCompile(`(?m)^#define SQLITE_([A-Z]+)_([A-Z0-9_]+) +\(SQLITE_([A-Z]+) *\| *\((\d+)<<8\)\)`)
	for _, m := range ext.FindAllStringSubmatch(string(h), -1) {
		if m[1] != m[3] || m[1] == "OK" {
			continue
		}
		n, _ := strconv.Atoi(m[4])
		want[m[1]+"_"+m[2]] = want[m[3]] | n<<8
	}
	if len(want) < 100 {
		t.Fatalf("read only %d codes from the header", len(want))
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "codes.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := (&types.Config{Importer: importer.Default()}).Check("sqlite", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, name := range pkg.Scope().Names() {
		if c, ok := pkg.Scope().Lookup(name).(*types.Const); ok && c.Exported() {
			v, _ := strconv.Atoi(c.Val().ExactString())
			got[name] = v
		}
	}
	for name, v := range want {
		if g, ok := got[name]; !ok {
			t.Errorf("codes.go lacks %s = %d", name, v)
		} else if g != v {
			t.Errorf("%s = %d in codes.go, %d in sqlite3.h", name, g, v)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("codes.go has %s, which sqlite3.h does not", name)
		}
	}
}
