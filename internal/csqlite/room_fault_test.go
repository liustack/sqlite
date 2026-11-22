// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && (amd64 || arm64))

package csqlite

import (
	"runtime"
	"runtime/debug"
	"testing"
)

// down takes about 128 bytes of the goroutine's stack for each of n levels,
// then calls f.
//
//go:noinline
func down(n int, f func()) byte {
	var pad [96]byte
	if n == 0 {
		f()
		return pad[n%len(pad)]
	}
	pad[n%len(pad)] = byte(n)
	return down(n-1, f) + pad[(n+1)%len(pad)]
}

// TestFaultNearStackBottom: the C faults (strlen of an address in a page it
// cannot read) at each depth over 48 KiB of fresh goroutine stacks, so that
// some faults come near the bottom of a stack, and the collector runs after
// them. On windows/amd64 the system writes the state of the CPU at a fault
// below the stack pointer, up to 12.8 KiB with AMX, more than the 4 KiB Go
// keeps (golang/go#81238). Without the room the generated code makes, the
// rest lands in the heap below the stack, and a collection after it can
// find a pointer to a freed object and stop the process. Where the CPU's
// state is small the test passes either way.
func TestFaultNearStackBottom(t *testing.T) {
	st, err := newStack()
	if err != nil {
		t.Fatal(err)
	}
	defer st.free()
	bad, _ := beforeGuard(t, 8)
	bad += 8 // the first byte of the guard page
	faults := 0
	for depth := 0; depth < 384; depth++ {
		done := make(chan bool)
		go func() {
			faulted := false
			defer func() { done <- faulted }()
			down(depth, func() {
				old := debug.SetPanicOnFault(true)
				defer debug.SetPanicOnFault(old)
				defer func() { faulted = recover() != nil }()
				strlen(st.sp(), bad)
			})
		}()
		if <-done {
			faults++
		}
		if depth%32 == 31 {
			runtime.GC()
		}
	}
	runtime.GC()
	if faults != 384 {
		t.Fatalf("%d of 384 reads of the guard page faulted", faults)
	}
}
