// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite_test

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// A program that imports the driver does not build on a Go newer than the
// one the engine was checked with, and the only error it gets names that
// Go. With sqlite_anygo it builds.
func TestNewerGoStopsTheDriver(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command")
	}
	src, err := os.ReadFile("internal/goversion/newer.go")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^//go:build (go1\.\d+) && !sqlite_anygo$`).FindSubmatch(src)
	if m == nil {
		t.Fatalf("internal/goversion/newer.go has no build line of the form the test reads:\n%s", src)
	}
	newer := string(m[1])
	build := func(tags string) ([]byte, error) {
		cmd := exec.Command(gobin, "build", "-tags", tags, "-o", os.DevNull, ".")
		cmd.Env = append(os.Environ(), "GOFLAGS=", "CGO_ENABLED=0")
		return cmd.CombinedOutput()
	}
	out, err := build(newer)
	if err == nil {
		t.Fatalf("the build with %s succeeded", newer)
	}
	named := false
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(l, "#") {
			continue
		}
		if !strings.HasPrefix(l, "internal/goversion/newer.go:") && !strings.HasPrefix(l, `internal\goversion\newer.go:`) {
			t.Fatalf("the build with %s said\n%s\nwant only the errors of internal/goversion", newer, out)
		}
		named = named || strings.Contains(l, "liustack_sqlite_is_checked_up_to_go1_")
	}
	if !named {
		t.Fatalf("the build with %s said\n%s\nwant the error that names the Go the engine was checked with", newer, out)
	}
	if out, err := build(newer + ",sqlite_anygo"); err != nil {
		t.Fatalf("the build with %s and sqlite_anygo failed:\n%s", newer, out)
	}
}
