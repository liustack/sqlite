// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package csqlite

import (
	"sync"
)

// malloc, realloc and free of the C. SQLite's memory is mmap memory, which
// the Go garbage collector does not see: the C keeps pointers to it in it.
//
// A block of up to 256 KiB is taken from a size class: each class keeps a
// list of its free blocks, threaded through them, and cuts new ones from
// 4 MiB arenas. A larger block is a mapping of its own, given back when
// freed. The classes go past 64 KiB because SQLite keeps a statement journal
// in memory in chunks of 64 KiB and a little more, one for each SAVEPOINT that
// writes: as mappings, that was an mmap and a munmap per statement. glibc
// also serves blocks of up to 128 KiB or more from its heap. Each
// block starts with a header of 16 bytes (the C asks for 8-byte alignment,
// and gets 16).
const (
	header     = 16
	arenaSize  = 4 << 20
	largeLimit = 256 << 10 // the largest class, header included
	refill     = 64 << 10  // the bytes a class cuts from an arena at once
	magic      = 0x51_4c_69_74
)

var classSizes = [...]uint32{
	32, 48, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 448, 512, 640, 768, 896, 1024,
	1280, 1536, 1792, 2048, 2560, 3072, 3584, 4096, 4352, 5120, 6144, 7168, 8192, 8448,
	10240, 12288, 14336, 16384, 20480, 24576, 28672, 32768, 40960, 49152, 57344, 65536,
	69632, 81920, 98304, 114688, 131072, 163840, 196608, 229376, 262144,
}

// classOf[i] is the class of a block of up to 16*i bytes, header included.
var classOf [largeLimit/16 + 1]uint8

func init() {
	c := 0
	for i := range classOf {
		for uint32(16*i) > classSizes[c] {
			c++
		}
		classOf[i] = uint8(c)
	}
}

type sizeClass struct {
	mu   sync.Mutex
	free uintptr // the first free block (its header)
	_    [40]byte
}

var heap struct {
	classes [len(classSizes)]sizeClass
	mu      sync.Mutex // the arena
	next    uintptr
	end     uintptr
}

// blockHeader is at the start of each block: its class plus one (0 for a
// mapping of its own), a check word, and the length of a mapping.
type blockHeader struct {
	class uint32
	check uint32
	size  uint64
}

func headerOf(p uintptr) *blockHeader { return (*blockHeader)(at(p - header)) }

// A bad block (heap damage) fails the call: free leaks it, realloc says out of
// memory, and the connection is marked broken. The heap is shared, but the
// block that failed is the only one lost.
func malloc(sp, n uintptr) uintptr { return alloc(n) }

func free(sp, p uintptr) {
	defer failQuiet(sp)
	release(p)
}

func realloc(sp, p, n uintptr) (ret uintptr) {
	defer failWith(sp, &ret, 0)
	if p == 0 {
		return alloc(n)
	}
	h := headerOf(p)
	if h.check != magic {
		panic("csqlite: realloc of a block malloc did not give")
	}
	var have uintptr
	if h.class == 0 {
		have = uintptr(h.size) - header
	} else {
		have = uintptr(classSizes[h.class-1]) - header
	}
	if n <= have && (h.class == 0 || n+header > have/2) {
		return p
	}
	q := alloc(n)
	if q == 0 {
		return 0
	}
	copy(bytesAt(q, int(min(n, have))), bytesAt(p, int(min(n, have))))
	release(p)
	return q
}

func alloc(n uintptr) uintptr {
	if n > 1<<40 {
		return 0
	}
	t := n + header
	if t > largeLimit {
		return allocLarge(t)
	}
	ci := classOf[(t+15)/16]
	c := &heap.classes[ci]
	c.mu.Lock()
	b := c.free
	if b == 0 {
		b = c.cut(int(ci))
		if b == 0 {
			c.mu.Unlock()
			return 0
		}
	}
	c.free = *(*uintptr)(at(b + header))
	c.mu.Unlock()
	h := (*blockHeader)(at(b))
	h.class = uint32(ci) + 1
	h.check = magic
	return b + header
}

// cut threads a run of new blocks of the class onto its free list, with c.mu
// held, and returns the first.
func (c *sizeClass) cut(ci int) uintptr {
	size := uintptr(classSizes[ci])
	run := max(refill/size, 1) * size
	heap.mu.Lock()
	if heap.end-heap.next < run {
		m, err := mmap(arenaSize, 0)
		if err != nil {
			heap.mu.Unlock()
			return 0
		}
		heap.next = uintptr(m)
		heap.end = heap.next + arenaSize
	}
	start := heap.next
	heap.next += run
	heap.mu.Unlock()
	for b := start; b < start+run; b += size {
		next := b + size
		if next >= start+run {
			next = 0
		}
		*(*uintptr)(at(b + header)) = next
	}
	c.free = start
	return start
}

func allocLarge(t uintptr) uintptr {
	t = roundUp(t, pageSize)
	m, err := mmap(t, 0)
	if err != nil {
		return 0
	}
	b := uintptr(m)
	h := (*blockHeader)(at(b))
	h.class = 0
	h.check = magic
	h.size = uint64(t)
	return b + header
}

func release(p uintptr) {
	if p == 0 {
		return
	}
	h := headerOf(p)
	if h.check != magic {
		panic("csqlite: free of a block malloc did not give")
	}
	b := p - header
	if h.class == 0 {
		munmap(b, uintptr(h.size))
		return
	}
	c := &heap.classes[h.class-1]
	h.check = 0
	c.mu.Lock()
	*(*uintptr)(at(p)) = c.free
	c.free = b
	c.mu.Unlock()
}
