// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package csqlite

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
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
	// stackHeader is the start of the region the host keeps: the poll
	// counter is inside it. The C stack starts after it.
	stackHeader = 4096
	// classifyOffset is the word of the header goClassify (engine/os.c)
	// writes the kind of a statement to as it is prepared.
	classifyOffset = 64
	// pendingOffset is the word of go_guard_commits (engine/os.c): set
	// while a panicked collation keeps the transaction from committing.
	pendingOffset = 128
	// pollInterval is how many ticks of the code run between two calls of
	// host_poll.
	pollInterval = 10000
)

// pageSize is the page of the operating system: 4 KiB on most machines, 16 KiB
// on darwin/arm64, and 4, 16 or 64 KiB on linux/arm64. Every mapping, trim and
// guard page is a whole number of them.
var pageSize = uintptr(os.Getpagesize())

func init() {
	if pageSize == 0 || pageSize&(pageSize-1) != 0 || stackSize%pageSize != 0 {
		panic("csqlite: the page size does not divide the shadow stack")
	}
	if pollCounterOffset+8 > stackHeader {
		panic("csqlite: the poll counter is outside the host's header")
	}
	if classifyOffset+8 > pollCounterOffset {
		panic("csqlite: the word of goClassify meets the poll counter")
	}
	if classifyOffset+8 > pendingOffset || pendingOffset+8 > pollCounterOffset {
		panic("csqlite: the pending word meets another word of the header")
	}
}

// A stack is the shadow-stack region of one connection. One goroutine at a
// time runs C on it. A guard page follows the region.
type stack struct {
	base uintptr
	// broken is set when a panic in a host callback left the C half way. Its
	// frames are gone and SQLite may hold locks, so no C runs on it again.
	broken atomic.Bool
	cause  atomic.Value // the first panic value, as a string
	// collatePanic is the panic of a Go collation during the call of C
	// running now, or "". Only the goroutine that runs the C touches it.
	collatePanic string
}

// stacks finds the stack of a shadow pointer, for a callback that must mark
// it. It is read only on the failure path.
var stacks sync.Map // base -> *stack

// poison marks the stack of sp as broken after a panic in a host callback.
func poison(sp uintptr, cause any) {
	if v, ok := stacks.Load(stackBase(sp)); ok {
		s := v.(*stack)
		s.cause.CompareAndSwap(nil, fmt.Sprint(cause))
		s.broken.Store(true)
	}
}

// stackLayout is where the shadow stack goes in a mapping of 2*stackSize+page
// bytes at start (a multiple of page): base is the aligned region, and the
// guard page follows it, so end is the end of what is kept. start, base and end
// are all multiples of page, so each trim is whole pages.
func stackLayout(start, page uintptr) (base, end uintptr) {
	base = (start + stackSize - 1) &^ (stackSize - 1)
	return base, base + stackSize + page
}

// roundUp rounds n up to a multiple of page (a power of two).
func roundUp(n, page uintptr) uintptr { return (n + page - 1) &^ (page - 1) }

func newStack() (*stack, error) {
	// Map twice the size, keep an aligned region and a guard page after it,
	// and give the rest back.
	n := uintptr(2*stackSize + pageSize)
	p, err := mmap(n, unix.MAP_NORESERVE)
	if err != nil {
		return nil, fmt.Errorf("csqlite: map a shadow stack: %w", err)
	}
	start := uintptr(p)
	base, end := stackLayout(start, pageSize)
	if base > start {
		munmap(start, base-start)
	}
	if start+n > end {
		munmap(end, start+n-end)
	}
	if err := unix.Mprotect(bytesAt(base+stackSize, int(pageSize)), unix.PROT_NONE); err != nil {
		munmap(base, stackSize+pageSize)
		return nil, err
	}
	s := &stack{base: base}
	stacks.Store(base, s)
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
	stacks.Delete(s.base)
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
	poison(sp, "the C trapped")
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
