// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package csqlite

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The shadow stack holds what the C keeps in memory on its stack: locals
// whose address is taken, and variable arguments. The generated code finds
// the base of its region by clearing the low bits of the shadow pointer, so
// the region is aligned to its size. It is mmap memory, which the Go garbage
// collector neither scans nor moves.
const (
	stackSize = 1 << shadowSizeLog2
	pageSize  = 1 << 14 // a page on every target (16 KiB on darwin/arm64)
	// stackHeader is the start of the region the host keeps: the poll
	// counter is inside it. The C stack starts after it.
	stackHeader = 4096
	// pollInterval is how many ticks of the code run between two calls of
	// host_poll.
	pollInterval = 10000
)

func init() {
	if pollCounterOffset+8 > stackHeader {
		panic("csqlite: the poll counter is outside the host's header")
	}
}

// A stack is the shadow-stack region of one connection. One goroutine at a
// time runs C on it. A guard page follows the region.
type stack struct {
	base uintptr
}

func newStack() (*stack, error) {
	// Map twice the size, keep an aligned region and a guard page after it,
	// and give the rest back.
	n := uintptr(2*stackSize + pageSize)
	p, err := mmap(n, unix.MAP_NORESERVE)
	if err != nil {
		return nil, fmt.Errorf("csqlite: map a shadow stack: %w", err)
	}
	start := uintptr(p)
	base := (start + stackSize - 1) &^ (stackSize - 1)
	end := base + stackSize + pageSize
	if base > start {
		munmap(start, base-start)
	}
	if start+n > end {
		munmap(end, start+n-end)
	}
	if err := unix.Mprotect(bytesAt(base+stackSize, pageSize), unix.PROT_NONE); err != nil {
		munmap(base, stackSize+pageSize)
		return nil, err
	}
	s := &stack{base: base}
	*s.pollCounter() = pollInterval
	return s, nil
}

// mmap maps n bytes of zeroed memory.
func mmap(n uintptr, flags int) (unsafe.Pointer, error) {
	return unix.MmapPtr(-1, 0, nil, n, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.MAP_PRIVATE|flags)
}

// munmap gives back n bytes at p. It fails only for an address the host
// never mapped, which is a bug of the host.
func munmap(p, n uintptr) {
	if err := unix.MunmapPtr(at(p), n); err != nil {
		panic("csqlite: munmap: " + err.Error())
	}
}

// sp is the shadow pointer a call into the C starts with.
func (s *stack) sp() uintptr { return s.base + stackHeader }

func (s *stack) pollCounter() *int64 { return (*int64)(at(s.base + pollCounterOffset)) }

func (s *stack) free() error {
	munmap(s.base, stackSize+pageSize)
	s.base = 0
	return nil
}

// stackBase is the base of the region a shadow pointer is in.
func stackBase(sp uintptr) uintptr { return sp &^ (stackSize - 1) }

// host_poll runs when the code has ticked pollInterval times. Its stack
// check is where the Go runtime can stop the goroutine (for the garbage
// collector, or to run another one): a loop of the C would otherwise hold
// the whole process at a stop. It calls a function, so that it is not a
// leaf the compiler leaves without a stack check (the post-link check holds
// it to one).
func host_poll(sp uintptr) {
	resetPoll(sp)
}

//go:noinline
func resetPoll(sp uintptr) {
	*(*int64)(at(stackBase(sp) + pollCounterOffset)) = pollInterval
}

// host_trap stands for a trap instruction of the C: a check that failed, or
// the shadow stack run out.
func host_trap(sp uintptr) {
	panic("csqlite: the C trapped")
}

// at makes a pointer of an address of C memory (mmap memory, not Go's).
func at(a uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&a)) }

// bytesAt is the n bytes of C memory at a.
func bytesAt(a uintptr, n int) []byte { return unsafe.Slice((*byte)(at(a)), n) }

// cstring is the NUL-terminated string at a, copied.
func cstring(a uintptr) string {
	if a == 0 {
		return ""
	}
	n := 0
	for *(*byte)(at(a + uintptr(n))) != 0 {
		n++
	}
	return string(bytesAt(a, n))
}
