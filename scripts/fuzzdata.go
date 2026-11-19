// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ignore

// Fuzzdata downloads the cases of SQLite's fuzzcheck, test/fuzzdata*.db of
// the source release of the SQLite engine/ holds, for TestFuzzData
// (internal/csqlite).
//
//	go run scripts/fuzzdata.go [dir]
//
// checks the SHA3-256 of the source zip and puts fuzzdata1.db to fuzzdata8.db
// in dir (.build/fuzzdata by default). Then:
//
//	FUZZDATA=$PWD/.build/fuzzdata go test -run TestFuzzData ./internal/csqlite
//
// The version is engine/vendor.sh's. On a new SQLite, set srcSHA3 to the one
// of the sqlite-src zip on https://sqlite.org/download.html.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha3"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

const srcSHA3 = "877c6f4996ce7adb368686bce21638de9e3d91534832ad3d9d119037d1bc22ad"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fuzzdata:", err)
		os.Exit(1)
	}
}

func run() error {
	dir := filepath.Join(".build", "fuzzdata")
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	vendor, err := os.ReadFile(filepath.Join("engine", "vendor.sh"))
	if err != nil {
		return fmt.Errorf("%v (run it from the root of the repository)", err)
	}
	version := field(vendor, "version")
	year := field(vendor, "year")
	var x, y, z int
	if _, err := fmt.Sscanf(version, "%d.%d.%d", &x, &y, &z); err != nil || year == "" {
		return fmt.Errorf("engine/vendor.sh: version %q, year %q", version, year)
	}
	// 3.54.1 is 3540100 in the file name, as in engine/vendor.sh.
	name := fmt.Sprintf("sqlite-src-%d%02d%02d00", x, y, z)
	url := "https://sqlite.org/" + year + "/" + name + ".zip"

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%s: %v", url, err)
	}
	sum := sha3.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != srcSHA3 {
		return fmt.Errorf("%s.zip has SHA3-256 %s, want %s", name, got, srcSHA3)
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	n := 0
	for _, f := range zr.File {
		base := path.Base(f.Name)
		if path.Dir(f.Name) != name+"/test" || !strings.HasPrefix(base, "fuzzdata") || path.Ext(base) != ".db" {
			continue
		}
		if err := extract(f, filepath.Join(dir, base)); err != nil {
			return err
		}
		n++
	}
	if n == 0 {
		return fmt.Errorf("%s.zip has no test/fuzzdata*.db", name)
	}
	fmt.Printf("%d files of SQLite %s in %s\n", n, version, dir)
	return nil
}

// field is the value of name=value in a shell script.
func field(sh []byte, name string) string {
	m := regexp.MustCompile(`(?m)^` + name + `=(\S+)$`).FindSubmatch(sh)
	if m == nil {
		return ""
	}
	return string(m[1])
}

func extract(f *zip.File, to string) error {
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("%s: %v", f.Name, err)
	}
	return os.WriteFile(to, b, 0o644)
}
