// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && (amd64 || arm64))

package sqlite_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The driver does not build for a target it does not run on, and the only
// error names the targets it runs on. Without it the package built there
// with no driver, and a program failed only when it opened a database.
func TestUnsupportedTargetStops(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command")
	}
	for _, target := range [][2]string{{"freebsd", "amd64"}, {"linux", "riscv64"}, {"windows", "386"}} {
		cmd := exec.Command(gobin, "build", "-o", os.DevNull, ".")
		cmd.Env = append(os.Environ(), "GOFLAGS=", "CGO_ENABLED=0", "GOOS="+target[0], "GOARCH="+target[1])
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Errorf("the build for %s/%s succeeded", target[0], target[1])
			continue
		}
		named := false
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if strings.HasPrefix(l, "#") {
				continue
			}
			if !strings.HasPrefix(l, "./unsupported.go:") && !strings.HasPrefix(l, `.\unsupported.go:`) {
				t.Fatalf("the build for %s/%s said\n%s\nwant only the error of unsupported.go", target[0], target[1], out)
			}
			named = named || strings.Contains(l, "liustack_sqlite_runs_on_linux_darwin_windows_amd64_arm64_only")
		}
		if !named {
			t.Errorf("the build for %s/%s said\n%s\nwant the error that names the targets", target[0], target[1], out)
		}
	}
}
