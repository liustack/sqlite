// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package csqlite

import (
	"math/rand/v2"
	"sync"
	"testing"
)

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

// Blocks freed after a peak give their memory back: a class keeps no more
// empty spans than keepEmpty says.
func TestEmptySpansGoBack(t *testing.T) {
	for _, n := range []uintptr{24, 1000, 60 << 10, 200 << 10} {
		m0 := mappedSpans.Load()
		var ps []uintptr
		for range (16 << 20) / n {
			p := alloc(n)
			if p == 0 {
				t.Fatalf("alloc(%d) failed", n)
			}
			ps = append(ps, p)
		}
		if grew := mappedSpans.Load() - m0; grew < 16<<20 {
			t.Fatalf("alloc(%d): spans grew by %d bytes for 16 MiB", n, grew)
		}
		for _, p := range ps {
			release(p)
		}
		ci := classOf[(n+header+15)/16]
		if left, keep := mappedSpans.Load()-m0, int64(keepEmpty[ci])*int64(spanBytes[ci]); left > keep {
			t.Errorf("alloc(%d): %d bytes of spans left after all were freed, want at most %d", n, left, keep)
		}
		checkHeap(t)
	}
}

// Blocks of every size, taken and given back in any order from several
// goroutines, never share bytes.
func TestAllocRandom(t *testing.T) {
	m0 := mappedSpans.Load()
	var wg sync.WaitGroup
	for g := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(g), 7))
			type block struct {
				p, n uintptr
				fill byte
			}
			var live []block
			check := func(b block) {
				for i, x := range bytesAt(b.p, int(b.n)) {
					if x != b.fill {
						t.Errorf("block %#x of %d bytes: byte %d is %d, want %d", b.p, b.n, i, x, b.fill)
						return
					}
				}
			}
			size := func() uintptr {
				if r.IntN(10) == 0 {
					return uintptr(1 + r.IntN(300<<10))
				}
				return uintptr(1 + r.IntN(2000))
			}
			for range 20000 {
				switch op := r.IntN(10); {
				case op < 5 || len(live) == 0:
					b := block{n: size(), fill: byte(r.IntN(256))}
					if b.p = alloc(b.n); b.p == 0 {
						t.Error("alloc failed")
						return
					}
					clear(bytesAt(b.p, int(b.n)))
					for i := range bytesAt(b.p, int(b.n)) {
						bytesAt(b.p, int(b.n))[i] = b.fill
					}
					live = append(live, b)
				case op < 8:
					i := r.IntN(len(live))
					check(live[i])
					release(live[i].p)
					live[i] = live[len(live)-1]
					live = live[:len(live)-1]
				default:
					i := r.IntN(len(live))
					b := live[i]
					check(b)
					n := size()
					q := realloc(0, b.p, n)
					if q == 0 {
						t.Error("realloc failed")
						return
					}
					b.p = q
					b.n = min(b.n, n)
					check(b)
					b.n = n
					for i := range bytesAt(b.p, int(b.n)) {
						bytesAt(b.p, int(b.n))[i] = b.fill
					}
					live[i] = b
				}
			}
			for _, b := range live {
				check(b)
				release(b.p)
			}
		}()
	}
	wg.Wait()
	checkHeap(t)
	var keep int64
	for ci := range classSizes {
		keep += int64(keepEmpty[ci]) * int64(spanBytes[ci])
	}
	if left := mappedSpans.Load() - m0; left > keep {
		t.Errorf("%d bytes of spans left after all were freed, want at most %d", left, keep)
	}
}

// A few connections that each take a large block and give it back, in
// turn, do not map a span each time: a class keeps the empty spans of at
// least 4 blocks.
func TestNoSpanThrash(t *testing.T) {
	for _, n := range []uintptr{70 << 10, 100 << 10, 128 << 10, 200 << 10} {
		var ps [4]uintptr
		round := func() {
			for i := range ps {
				if ps[i] = alloc(n); ps[i] == 0 {
					t.Fatalf("alloc(%d) failed", n)
				}
			}
			for _, p := range ps {
				release(p)
			}
		}
		round()
		m0 := spanMaps.Load()
		for range 100 {
			round()
		}
		if maps := spanMaps.Load() - m0; maps != 0 {
			t.Errorf("alloc(%d): %d spans mapped in 100 rounds of 4 blocks", n, maps)
		}
		checkHeap(t)
	}
}

// checkHeap checks the lists of spans of every class: the links go both
// ways, each span on a list has a free block, and the count of empty spans
// is right and within keepEmpty. An empty span is always on its list, so
// the walk sees them all.
func checkHeap(t *testing.T) {
	t.Helper()
	for ci := range classSizes {
		c := &heap.classes[ci]
		size := uintptr(classSizes[ci])
		c.mu.Lock()
		empty, prev := 0, uintptr(0)
		for sb := c.spans; sb != 0; sb = spanAt(sb).next {
			s := spanAt(sb)
			if !s.listed || s.prev != prev || !s.hasFree(size) {
				t.Errorf("class %d: span %#x: listed %v, prev %#x want %#x, has free %v", size, sb, s.listed, s.prev, prev, s.hasFree(size))
			}
			if s.used == 0 {
				empty++
			}
			prev = sb
		}
		if empty != c.empty || empty > keepEmpty[ci] {
			t.Errorf("class %d: %d empty spans, counted %d, keeps %d", size, empty, c.empty, keepEmpty[ci])
		}
		c.mu.Unlock()
	}
}
