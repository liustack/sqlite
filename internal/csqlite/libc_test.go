// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package csqlite

import (
	"bytes"
	"fmt"
	"runtime/debug"
	"testing"
)

// The functions of engine/libc that are changed from musl's (its patches/):
// each must give what the plain loop gives, for every length, every place
// in a word and every place of the first difference, and must read nothing
// past what it is given.

// cmpSign is the sign of what a C compare returned, an int in a register.
func cmpSign(r uintptr) int {
	switch v := int32(r); {
	case v < 0:
		return -1
	case v > 0:
		return 1
	}
	return 0
}

// beforeGuard gives the last n bytes before a page that cannot be read: the
// guard page that follows a shadow stack, which nothing here runs C on. A
// read past the bytes is a fault.
func beforeGuard(t *testing.T, n int) (addr uintptr, mem []byte) {
	st, err := newStack()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.free() })
	addr = st.base + stackSize - uintptr(n)
	return addr, bytesAt(addr, n)
}

// faultFails makes a fault in the C a failure of the test that says what
// ran, instead of the end of the process.
func faultFails(t *testing.T, what func() string) func() {
	old := debug.SetPanicOnFault(true)
	return func() {
		debug.SetPanicOnFault(old)
		if r := recover(); r != nil {
			t.Fatalf("%s: %v", what(), r)
		}
	}
}

// TestMemcmp: memcmp compares eight bytes at a time. Every length up to 40,
// each buffer ending from 0 to 7 bytes before a page that cannot be read
// (so each starts at each place of a word, and with 0 a read past the end
// faults), and the first difference at each byte, with pairs of bytes whose
// order differs as signed and as unsigned. The bytes before what is
// compared differ, and so do the ones after: they must not count.
func TestMemcmp(t *testing.T) {
	st, err := newStack()
	if err != nil {
		t.Fatal(err)
	}
	defer st.free()
	const room = 64
	pa, a := beforeGuard(t, room)
	pb, b := beforeGuard(t, room)
	pairs := [][2]byte{{1, 2}, {2, 1}, {0x7f, 0x80}, {0x80, 0x7f}, {0, 0xff}, {0xff, 0}, {0x10, 0x11}}
	var n, ea, eb, at int
	defer faultFails(t, func() string {
		return fmt.Sprintf("memcmp of %d bytes that end %d and %d bytes before a page it cannot read", n, ea, eb)
	})()
	for n = 0; n <= 40; n++ {
		for ea = 0; ea < 8; ea++ {
			for eb = 0; eb < 8; eb++ {
				oa, ob := room-ea-n, room-eb-n
				// at is the first byte that differs: n for none.
				for at = 0; at <= n; at++ {
					for _, p := range pairs {
						for i := range a {
							a[i] = byte(i*7 + 3)
							b[i] = ^a[i]
						}
						for i := 0; i < n; i++ {
							b[ob+i] = a[oa+i]
						}
						if at < n {
							a[oa+at], b[ob+at] = p[0], p[1]
							// What follows the first difference says the
							// opposite, and must not count.
							for i := at + 1; i < n; i++ {
								a[oa+i], b[ob+i] = p[1], p[0]
							}
						}
						want := bytes.Compare(a[oa:oa+n], b[ob:ob+n])
						got := cmpSign(memcmp(st.sp(), pa+uintptr(oa), pb+uintptr(ob), uintptr(n)))
						if got != want {
							t.Fatalf("memcmp of %d bytes that end %d and %d bytes before the page, first difference at %d (%#x, %#x): sign %d, want %d",
								n, ea, eb, at, p[0], p[1], got, want)
						}
					}
				}
			}
		}
	}
}

// TestStrcspn: strcspn has a path of its own for a set of two bytes, which
// SQLite's LIKE calls at each place a match can start. Sets of no byte to
// four, with a byte twice and with bytes of 0x80 and more, in strings that
// have such bytes too, and the first byte of the set at each place of the
// string or nowhere. The terminator of the set is the last byte before a
// page that cannot be read, and so is the one of the string, or it is up to
// 7 bytes before it.
func TestStrcspn(t *testing.T) {
	st, err := newStack()
	if err != nil {
		t.Fatal(err)
	}
	defer st.free()
	const room = 64
	ps, s := beforeGuard(t, room)
	pc, c := beforeGuard(t, 8)
	// The bytes of a string outside the set: in no set below, some of them
	// negative as a signed char.
	fill := []byte{'m', 0x90, 'n', 0xf0, 'o', 0xc3, 'p'}
	sets := []string{"", "a", "ab", "aa", "aA", "\x80a", "a\xff", "\xfe\xff", "abc", "abca", "\x80\x81\x82"}
	var set string
	var n, end int
	defer faultFails(t, func() string {
		return fmt.Sprintf("strcspn of a string of %d bytes whose terminator is %d bytes before a page it cannot read, set %q", n, end, set)
	})()
	for _, set = range sets {
		// The set, its terminator the last byte that can be read.
		set0 := pc + uintptr(len(c)-len(set)-1)
		copy(c[len(c)-len(set)-1:], set)
		c[len(c)-1] = 0
		for n = 0; n <= 24; n++ {
			for end = 0; end < 8; end++ {
				off := room - 1 - end - n
				// at is where the first byte of the set is: n for nowhere.
				for at := 0; at <= n; at++ {
					for k := 0; k < max(len(set), 1); k++ {
						for i := range s {
							s[i] = 'z' // not of the string: must not be read as part of it
						}
						for i := 0; i < n; i++ {
							s[off+i] = fill[i%len(fill)]
						}
						s[off+n] = 0
						want := n
						if at < n && len(set) > 0 {
							s[off+at] = set[k]
							want = at
							// A later byte of the set must not count.
							if at+1 < n {
								s[off+at+1] = set[len(set)-1-k]
							}
						}
						if got := int(strcspn(st.sp(), ps+uintptr(off), set0)); got != want {
							t.Fatalf("strcspn(%q, %q), terminator %d bytes before the page: %d, want %d", s[off:off+n], set, end, got, want)
						}
					}
				}
			}
		}
	}
}
