// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package csqlite

import "testing"

// SQLite's statement journal is kept in memory in chunks of a little over
// 64 KiB (the statement spill size). Each SAVEPOINT that writes takes one
// and gives it back, so such a block must come from a size class and not
// be a mapping of its own: an mmap and a munmap per statement.
func TestStatementJournalChunkIsClassed(t *testing.T) {
	for _, n := range []uintptr{64<<10 + 16, 128 << 10, 256<<10 - header} {
		p := alloc(n)
		if p == 0 {
			t.Fatalf("alloc(%d) failed", n)
		}
		if c := headerOf(p).class; c == 0 {
			t.Errorf("alloc(%d) is a mapping of its own", n)
		} else if have := uintptr(classSizes[c-1]) - header; have < n {
			t.Errorf("alloc(%d) got a class of %d bytes", n, have)
		}
		release(p)
	}
	// Past the classes, a block is still a mapping of its own.
	p := alloc(256 << 10)
	if headerOf(p).class != 0 {
		t.Error("alloc(256 KiB) came from a class")
	}
	release(p)
}

// Every size up to the largest class gets a class that holds it, and no
// class is more than about a quarter larger than what it holds.
func TestClassOf(t *testing.T) {
	for n := uintptr(1); n+header <= largeLimit; n++ {
		t16 := (n + header + 15) / 16
		size := uintptr(classSizes[classOf[t16]])
		if size < n+header {
			t.Fatalf("size %d: class of %d", n, size)
		}
		if n+header > 64 && size > (n+header)*5/4+16 {
			t.Fatalf("size %d: class of %d", n, size)
		}
	}
}
