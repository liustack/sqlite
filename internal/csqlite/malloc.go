// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package csqlite

import (
	"sync"
	"sync/atomic"
	"unsafe"
)

// malloc, realloc and free of the C. SQLite's memory is mmap memory, which
// the Go garbage collector does not see: the C keeps pointers to it in it.
//
// A block of up to 256 KiB is taken from a size class. Each class has spans:
// a span is a mapping of its own, of about 256 KiB, cut into blocks of the
// class, with its own list of free blocks threaded through them and a count
// of the blocks in use. A span whose blocks are all free again is unmapped,
// but for a few per class (keepEmpty), kept for the next blocks: memory
// taken at a peak goes back to the system when the peak is over, and a
// block taken and given back over and over does not map and unmap a span
// each time. A use of a class block after it is freed, a bug in the C, can
// fault the process when its span is gone, as it can for a larger block. A
// larger block is a mapping of its own, given back when freed. The classes
// go past 64 KiB because SQLite keeps a statement journal in memory in
// chunks of 64 KiB and a little more, one for each SAVEPOINT that writes:
// as mappings, that was an mmap and a munmap per statement. glibc also
// serves blocks of up to 128 KiB or more from its heap. Each block starts
// with a header of 16 bytes (the C asks for 8-byte alignment, and gets 16).
const (
	header     = 16
	largeLimit = 256 << 10 // the largest class, header included
	spanTarget = 256 << 10 // about the size of a span
	spanHeader = 64        // the span struct at the start of a span
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

// spanBytes[c] is the size of a span of class c: as many blocks as fit in
// about spanTarget, at least one, rounded up to whole pages.
var spanBytes [len(classSizes)]uintptr

// keepEmpty[c] is how many spans of class c are kept with no block in use:
// enough for 4 blocks, so that a few connections taking a large block in
// turn do not map and unmap a span each time. Only the classes past 64 KiB
// keep more than one.
var keepEmpty [len(classSizes)]int

func init() {
	c := 0
	for i := range classOf {
		for uint32(16*i) > classSizes[c] {
			c++
		}
		classOf[i] = uint8(c)
	}
	for c, size := range classSizes {
		k := max((spanTarget-spanHeader)/uintptr(size), 1)
		spanBytes[c] = roundUp(spanHeader+k*uintptr(size), pageSize)
		keepEmpty[c] = int((4 + k - 1) / k)
	}
}

// A span is at the start of its mapping. Its blocks follow the struct: the
// free ones are on free, and the ones never taken start at fresh.
type span struct {
	free       uintptr // the first free block (its header), 0 for none
	fresh, end uintptr // the blocks never taken: fresh up to end
	used       uintptr // the blocks in use
	prev, next uintptr // in the class's list of spans with a free block
	listed     bool
}

// The span struct fits in spanHeader, which keeps the blocks 16-byte aligned.
var _ [spanHeader - unsafe.Sizeof(span{})]byte

func spanAt(s uintptr) *span { return (*span)(at(s)) }

// hasFree says whether the span can give a block.
func (s *span) hasFree(size uintptr) bool { return s.free != 0 || s.fresh+size <= s.end }

type sizeClass struct {
	mu    sync.Mutex
	spans uintptr // the first span with a free block
	empty int     // spans of the class with no block in use, up to keepEmpty
	_     [40]byte
}

var heap struct {
	classes [len(classSizes)]sizeClass
}

// mappedSpans is the bytes of the spans mapped now, and spanMaps the spans
// mapped so far.
var (
	mappedSpans atomic.Int64
	spanMaps    atomic.Int64
)

// blockHeader is at the start of each block: its class plus one (0 for a
// mapping of its own), a check word, and the length of a mapping or the
// span of a class block.
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
	size := uintptr(classSizes[ci])
	c := &heap.classes[ci]
	c.mu.Lock()
	sb := c.spans
	if sb == 0 {
		if sb = newSpan(ci); sb == 0 {
			c.mu.Unlock()
			return 0
		}
		c.push(sb)
		c.empty++
	}
	s := spanAt(sb)
	b := s.free
	if b != 0 {
		s.free = *(*uintptr)(at(b + header))
	} else {
		b = s.fresh
		s.fresh += size
	}
	if s.used == 0 {
		c.empty--
	}
	s.used++
	if !s.hasFree(size) {
		c.unlink(sb)
	}
	c.mu.Unlock()
	h := (*blockHeader)(at(b))
	h.class = uint32(ci) + 1
	h.check = magic
	h.size = uint64(sb)
	return b + header
}

// newSpan maps a span of class ci, with all its blocks fresh.
func newSpan(ci uint8) uintptr {
	m, err := mmap(spanBytes[ci])
	if err != nil {
		return 0
	}
	mappedSpans.Add(int64(spanBytes[ci]))
	spanMaps.Add(1)
	sb := uintptr(m)
	*spanAt(sb) = span{fresh: sb + spanHeader, end: sb + spanBytes[ci]}
	return sb
}

// push puts the span sb first in the class's list, with c.mu held.
func (c *sizeClass) push(sb uintptr) {
	s := spanAt(sb)
	s.prev, s.next, s.listed = 0, c.spans, true
	if c.spans != 0 {
		spanAt(c.spans).prev = sb
	}
	c.spans = sb
}

// unlink takes the span sb out of the class's list, with c.mu held.
func (c *sizeClass) unlink(sb uintptr) {
	s := spanAt(sb)
	if s.prev != 0 {
		spanAt(s.prev).next = s.next
	} else {
		c.spans = s.next
	}
	if s.next != 0 {
		spanAt(s.next).prev = s.prev
	}
	s.prev, s.next, s.listed = 0, 0, false
}

func allocLarge(t uintptr) uintptr {
	t = roundUp(t, pageSize)
	m, err := mmap(t)
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
	ci := h.class - 1
	c := &heap.classes[ci]
	sb := uintptr(h.size)
	h.check = 0
	c.mu.Lock()
	s := spanAt(sb)
	*(*uintptr)(at(p)) = s.free
	s.free = b
	s.used--
	if !s.listed {
		c.push(sb)
	}
	if s.used > 0 {
		c.mu.Unlock()
		return
	}
	if c.empty < keepEmpty[ci] {
		c.empty++ // kept for the next blocks of the class
		c.mu.Unlock()
		return
	}
	c.unlink(sb)
	c.mu.Unlock()
	munmap(sb, spanBytes[ci])
	mappedSpans.Add(-int64(spanBytes[ci]))
}
