// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package main

import (
	"strings"
	"testing"
)

// The docs show the largest WAL of each driver in each case, once a run
// recorded it, and the README tables do not.
func TestRenderContentionWAL(t *testing.T) {
	s := &contentionSet{Name: "x", Drivers: []string{"liustack", "mattn"}}
	s.Meta.Set("time", "30s")
	for _, c := range contentionCases {
		s.Results = append(s.Results,
			contentionResult{Case: c.key, Driver: "liustack", Seconds: 30, Done: 300, MaxWAL: 4 << 20},
			contentionResult{Case: c.key, Driver: "mattn", Seconds: 30, Done: 300, MaxWAL: 1933 << 20})
	}
	docs := renderContention(s, en, false)
	if !strings.Contains(docs, "**The largest WAL**") || !strings.Contains(docs, "| 4.00 MiB | 1.89 GiB |") {
		t.Errorf("no WAL table in the docs:\n%s", docs)
	}
	if strings.Contains(renderContention(s, en, true), "largest WAL") {
		t.Error("a WAL table in the README")
	}
	for i := range s.Results {
		s.Results[i].MaxWAL = 0
	}
	if strings.Contains(renderContention(s, en, false), "largest WAL") {
		t.Error("a WAL table for a run that did not record the WAL")
	}
}
