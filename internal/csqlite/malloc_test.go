// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package csqlite

import (
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

// The tests count the spans that are mapped, so no arena gives spans back
// behind them: only TestIdleArenaGivesSpansBack sets a time that comes.
func init() { idleTrim = time.Hour }

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
// goroutines, each with a stack of its own, never share bytes. Some are
// freed by another goroutine than the one that took them.
func TestAllocRandom(t *testing.T) {
	const goroutines = 4
	m0 := mappedSpans.Load()
	type block struct {
		p, n uintptr
		fill byte
	}
	passed := make(chan block, 64)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := newStack()
			if err != nil {
				t.Error(err)
				return
			}
			defer st.free()
			sp := st.sp()
			r := rand.New(rand.NewPCG(uint64(g), 7))
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
				switch op := r.IntN(11); {
				case op < 5 || len(live) == 0:
					b := block{n: size(), fill: byte(r.IntN(256))}
					if b.p = malloc(sp, b.n); b.p == 0 {
						t.Error("malloc failed")
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
					free(sp, live[i].p)
					live[i] = live[len(live)-1]
					live = live[:len(live)-1]
				case op == 10:
					// Give a block to another goroutine, and free one of
					// another's.
					i := r.IntN(len(live))
					select {
					case passed <- live[i]:
						live[i] = live[len(live)-1]
						live = live[:len(live)-1]
					default:
					}
					select {
					case b := <-passed:
						check(b)
						free(sp, b.p)
					default:
					}
				default:
					i := r.IntN(len(live))
					b := live[i]
					check(b)
					n := size()
					q := realloc(sp, b.p, n)
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
				free(sp, b.p)
			}
		}()
	}
	wg.Wait()
	close(passed)
	for b := range passed {
		release(b.p)
	}
	checkHeap(t)
	var keep int64
	for ci := range classSizes {
		keep += goroutines * int64(keepEmpty[ci]) * int64(spanBytes[ci])
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

// A new stack takes the arena the fewest stacks use, so stacks that live at
// the same time spread over the arenas, and the arena of a stack that is
// freed is taken again.
func TestStacksSpreadOverArenas(t *testing.T) {
	uses := func() [arenas]int {
		arenaUse.mu.Lock()
		defer arenaUse.mu.Unlock()
		return arenaUse.n
	}
	start := uses()
	var sts []*stack
	defer func() {
		for _, st := range sts {
			st.free()
		}
		if end := uses(); end != start {
			t.Errorf("stacks per arena after all were freed: %v, want %v", end, start)
		}
	}()
	for i := range 2*arenas + 3 {
		before := uses()
		st, err := newStack()
		if err != nil {
			t.Fatal(err)
		}
		sts = append(sts, st)
		a := st.arena
		if arenaOf(st.sp()) != &heap.arenas[a] {
			t.Fatalf("stack %d: arenaOf is not arena %d", i, a)
		}
		for j, n := range before {
			if n < before[a] {
				t.Fatalf("stack %d took arena %d with %d stacks, arena %d has %d", i, a, before[a], j, n)
			}
		}
		if after := uses(); after[a] != before[a]+1 {
			t.Fatalf("stack %d: arena %d has %d stacks, want %d", i, a, after[a], before[a]+1)
		}
	}
	// The arena of a freed stack is the least used again.
	a := sts[5].arena
	sts[5].free()
	st, err := newStack()
	if err != nil {
		t.Fatal(err)
	}
	sts[5] = st
	if got := st.arena; got != a {
		t.Errorf("the stack made after one of arena %d was freed took arena %d", a, got)
	}
}

// A block comes from the arena of the stack that asked for it, and goes
// back to that arena when another stack frees it.
func TestBlockGoesBackToItsArena(t *testing.T) {
	var sts [2]*stack
	for i := range sts {
		st, err := newStack()
		if err != nil {
			t.Fatal(err)
		}
		defer st.free()
		sts[i] = st
	}
	a, b := sts[0].arena, sts[1].arena
	if a == b {
		t.Fatalf("two stacks share arena %d", a)
	}
	for _, n := range []uintptr{24, 4336, 100 << 10} {
		ci := classOf[(n+header+15)/16]
		ca, cb := &heap.arenas[a].classes[ci], &heap.arenas[b].classes[ci]
		// Fill the spans arena a has, so that the block is the only one of
		// a span of its own.
		var fill []uintptr
		for ca.spans != 0 {
			fill = append(fill, malloc(sts[0].sp(), n))
		}
		emptyB := cb.empty
		p := malloc(sts[0].sp(), n)
		if p == 0 {
			t.Fatalf("malloc(%d) failed", n)
		}
		sb := uintptr(headerOf(p).size)
		if got := spanAt(sb).arena; got != a {
			t.Errorf("malloc(%d) on a stack of arena %d gave a block of arena %d", n, a, got)
		}
		free(sts[1].sp(), p)
		if ca.spans != sb || spanAt(sb).used != 0 || ca.empty != 1 {
			t.Errorf("malloc(%d): the block freed by another stack did not go back to its span", n)
		}
		if cb.empty != emptyB {
			t.Errorf("malloc(%d): arena %d has %d empty spans, had %d", n, b, cb.empty, emptyB)
		}
		for _, p := range fill {
			free(sts[0].sp(), p)
		}
		checkHeap(t)
	}
}

// An arena no stack has used for idleTrim gives its empty spans back to the
// system, and an arena that is taken again in that time keeps them.
func TestIdleArenaGivesSpansBack(t *testing.T) {
	// The timers the tests before set would come in an hour.
	setTrim := func(d time.Duration) {
		arenaUse.mu.Lock()
		idleTrim = d
		for a, tm := range arenaUse.timer {
			if tm != nil && tm.Stop() {
				arenaUse.timer[a] = nil
			}
		}
		arenaUse.mu.Unlock()
	}
	const trim = 50 * time.Millisecond
	setTrim(trim)
	defer setTrim(time.Hour)
	empty := func(a uint8) (n int) {
		for ci := range heap.arenas[a].classes {
			c := &heap.arenas[a].classes[ci]
			c.mu.Lock()
			n += c.empty
			c.mu.Unlock()
		}
		return n
	}
	st, err := newStack()
	if err != nil {
		t.Fatal(err)
	}
	a := st.arena
	sizes := []uintptr{24, 4336, 100 << 10}
	for _, n := range sizes {
		p := malloc(st.sp(), n)
		if p == 0 {
			t.Fatalf("malloc(%d) failed", n)
		}
		free(st.sp(), p)
	}
	kept := empty(a)
	if kept < len(sizes) {
		t.Fatalf("arena %d keeps %d empty spans after a block of %d classes, want at least %d", a, kept, len(sizes), len(sizes))
	}
	m0 := mappedSpans.Load()

	// Left and taken again at once: it is in use when the time is over.
	st.free()
	if st, err = newStack(); err != nil {
		t.Fatal(err)
	}
	if st.arena != a {
		t.Fatalf("the next stack took arena %d, want %d", st.arena, a)
	}
	time.Sleep(4 * trim)
	if n := empty(a); n != kept {
		t.Errorf("arena %d has %d empty spans while a stack uses it, had %d", a, n, kept)
	}

	// Left for good.
	st.free()
	for end := time.Now().Add(10 * time.Second); empty(a) != 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("arena %d still has %d empty spans long after its last stack was freed", a, empty(a))
		}
	}
	want := int64(0)
	for _, n := range sizes {
		want += int64(spanBytes[classOf[(n+header+15)/16]])
	}
	if gone := m0 - mappedSpans.Load(); gone < want {
		t.Errorf("%d bytes of spans went back, want at least the %d of a span of each of %d classes", gone, want, len(sizes))
	}
	checkHeap(t)

	// The arena serves the next stack as before.
	if st, err = newStack(); err != nil {
		t.Fatal(err)
	}
	defer st.free()
	for _, n := range sizes {
		p := malloc(st.sp(), n)
		if p == 0 {
			t.Fatalf("malloc(%d) after the trim failed", n)
		}
		*(*byte)(at(p)) = 1
		free(st.sp(), p)
	}
	checkHeap(t)
}

// An arena that is trimmed while goroutines take and give back its blocks
// loses only spans with no block in use: no block changes under its owner.
// The race detector does not see this memory, so the blocks are checked.
func TestTrimBesideAllocs(t *testing.T) {
	st, err := newStack()
	if err != nil {
		t.Fatal(err)
	}
	defer st.free()
	sp, a := st.sp(), &heap.arenas[st.arena]
	sizes := []uintptr{24, 200, 4336, 20 << 10, 100 << 10, 200 << 10}
	var stop atomic.Bool
	var trims sync.WaitGroup
	trims.Add(1)
	go func() {
		defer trims.Done()
		for !stop.Load() {
			a.trim()
		}
	}()
	var wg sync.WaitGroup
	var bad atomic.Int64
	for g := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(g), 7))
			type block struct {
				p, n uintptr
				fill byte
			}
			var held []block
			drop := func(b block) {
				for _, c := range bytesAt(b.p, int(b.n)) {
					if c != b.fill {
						bad.Add(1)
						break
					}
				}
				free(sp, b.p)
			}
			for i := range 4000 {
				n := sizes[rng.IntN(len(sizes))]
				p := malloc(sp, n)
				if p == 0 {
					bad.Add(1)
					return
				}
				b := block{p, n, byte(g<<4 | i&15)}
				for j := range bytesAt(p, int(n)) {
					bytesAt(p, int(n))[j] = b.fill
				}
				held = append(held, b)
				// Mostly a block or two, so that spans become empty.
				for len(held) > rng.IntN(3) {
					k := rng.IntN(len(held))
					drop(held[k])
					held[k] = held[len(held)-1]
					held = held[:len(held)-1]
				}
			}
			for _, b := range held {
				drop(b)
			}
		}()
	}
	wg.Wait()
	stop.Store(true)
	trims.Wait()
	if n := bad.Load(); n != 0 {
		t.Errorf("%d blocks failed or changed while their arena was trimmed", n)
	}
	checkHeap(t)
	a.trim()
	checkHeap(t)
	for ci := range a.classes {
		if n := a.classes[ci].empty; n != 0 {
			t.Errorf("class %d keeps %d empty spans after a trim", classSizes[ci], n)
		}
	}
}

// BenchmarkMallocFree takes and gives back a block the size of a page of
// the page cache, from goroutines that each have a stack: what readers do
// after each commit of another connection.
func BenchmarkMallocFree(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		st, err := newStack()
		if err != nil {
			b.Error(err)
			return
		}
		defer st.free()
		sp := st.sp()
		var ps [32]uintptr
		for pb.Next() {
			for i := range ps {
				ps[i] = malloc(sp, 4336)
			}
			for _, p := range ps {
				free(sp, p)
			}
		}
	})
}

// checkHeap checks the lists of spans of every class of every arena: the
// links go both ways, each span on a list has a free block and is of the
// arena, and the count of empty spans is right and within keepEmpty. An
// empty span is always on its list, so the walk sees them all.
func checkHeap(t *testing.T) {
	t.Helper()
	for ai := range heap.arenas {
		for ci := range classSizes {
			c := &heap.arenas[ai].classes[ci]
			size := uintptr(classSizes[ci])
			c.mu.Lock()
			empty, prev := 0, uintptr(0)
			for sb := c.spans; sb != 0; sb = spanAt(sb).next {
				s := spanAt(sb)
				if !s.listed || s.prev != prev || !s.hasFree(size) || int(s.arena) != ai {
					t.Errorf("arena %d, class %d: span %#x: listed %v, prev %#x want %#x, has free %v, arena %d", ai, size, sb, s.listed, s.prev, prev, s.hasFree(size), s.arena)
				}
				if s.used == 0 {
					empty++
				}
				prev = sb
			}
			if empty != c.empty || empty > keepEmpty[ci] {
				t.Errorf("arena %d, class %d: %d empty spans, counted %d, keeps %d", ai, size, empty, c.empty, keepEmpty[ci])
			}
			c.mu.Unlock()
		}
	}
}
