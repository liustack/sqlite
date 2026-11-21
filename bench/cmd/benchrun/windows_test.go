// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package main

import "testing"

func TestWindowsVersion(t *testing.T) {
	for _, c := range []struct{ in, arch, want string }{
		{"\r\nMicrosoft Windows [Version 10.0.26100.4652]\r\n", "arm64", "Windows 10.0.26100.4652 arm64"},
		{"Microsoft Windows [Version 10.0.20348.3932]", "amd64", "Windows 10.0.20348.3932 amd64"},
		{"", "amd64", "Windows amd64"},
	} {
		if got := windowsVersion(c.in, c.arch); got != c.want {
			t.Errorf("windowsVersion(%q, %q) = %q, want %q", c.in, c.arch, got, c.want)
		}
	}
}

func TestWindowsCPU(t *testing.T) {
	in := "\r\nHKEY_LOCAL_MACHINE\\HARDWARE\\DESCRIPTION\\System\\CentralProcessor\\0\r\n" +
		"    ProcessorNameString    REG_SZ    Snapdragon(R) X Elite - X1E80100 - Qualcomm(R) Oryon(TM) CPU\r\n\r\n"
	if got, want := windowsCPU(in), "Snapdragon(R) X Elite - X1E80100 - Qualcomm(R) Oryon(TM) CPU"; got != want {
		t.Errorf("windowsCPU = %q, want %q", got, want)
	}
	if got := windowsCPU(""); got != "" {
		t.Errorf("windowsCPU(\"\") = %q, want \"\"", got)
	}
}

func TestTestBin(t *testing.T) {
	if got := testBin("liustack", "windows"); got != "liustack.test.exe" {
		t.Errorf("testBin on windows = %q", got)
	}
	if got := testBin("liustack", "linux"); got != "liustack.test" {
		t.Errorf("testBin on linux = %q", got)
	}
}
