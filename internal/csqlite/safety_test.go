// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package csqlite

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func openRaw(t *testing.T, name string, flags int) (*Conn, error) {
	t.Helper()
	return Open(name, flags, "")
}

// Close with a statement still open is refused, keeps the stack, and the
// connection works on: finalize, then Close succeeds.
func TestCloseWithOpenStatement(t *testing.T) {
	c, err := openRaw(t, ":memory:", OpenReadWrite|OpenCreate)
	if err != nil {
		t.Fatal(err)
	}
	s, _, err := c.Prepare("SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	base := c.st.base
	err = c.Close()
	var e *Error
	if !errors.As(err, &e) || e.Primary() != 5 {
		t.Fatalf("Close with an open statement: %v", err)
	}
	if c.st.base != base || c.st.base == 0 || c.db == 0 {
		t.Fatal("a refused Close freed the stack or the handle")
	}
	if err := c.Exec("SELECT 2"); err != nil {
		t.Fatalf("connection unusable after a refused Close: %v", err)
	}
	if err := s.Finalize(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close after finalize: %v", err)
	}
	if c.st.base != 0 {
		t.Error("stack not freed by the Close that worked")
	}
}

// A name past the shadow stack is an error, not a fault.
func TestOpenLongName(t *testing.T) {
	long := strings.Repeat("a", 40<<20)
	c, err := openRaw(t, long, OpenReadWrite|OpenCreate)
	var e *Error
	if !errors.As(err, &e) || e.Primary() != 18 {
		if c != nil {
			c.Close()
		}
		t.Fatalf("long name: %v", err)
	}
	c = open(t, ":memory:")
	if _, err := openRaw(t, ":memory:", OpenReadWrite|OpenCreate); err != nil {
		t.Fatal(err)
	}
	defer func() { recover() }()
	if got := c.Filename(long); got != "" {
		t.Errorf("Filename of a long schema: %q", got)
	}
}

// A shared shadow stack cannot serve two goroutines.
func TestOpenFullMutexRejected(t *testing.T) {
	for _, f := range []int{OpenFullMutex, OpenNoMutex | OpenFullMutex} {
		c, err := openRaw(t, ":memory:", OpenReadWrite|OpenCreate|f)
		if err == nil {
			c.Close()
			t.Fatalf("flags %#x accepted", f)
		}
	}
}

// A failed allocation for a big argument is SQLITE_NOMEM.
func TestStageAllocFailure(t *testing.T) {
	c := open(t, ":memory:")
	if err := c.Exec("CREATE TABLE t(a)"); err != nil {
		t.Fatal(err)
	}
	s, _, err := c.Prepare("INSERT INTO t VALUES (?)")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Finalize()
	old := stageAlloc
	stageAlloc = func(uintptr) uintptr { return 0 }
	defer func() { stageAlloc = old }()
	big1 := strings.Repeat("x", big+1)
	check := func(what string, err error) {
		t.Helper()
		var e *Error
		if !errors.As(err, &e) || e.Primary() != 7 {
			t.Errorf("%s: %v", what, err)
		}
	}
	check("Exec", c.Exec("SELECT '"+big1+"'"))
	_, _, err = c.Prepare("SELECT '" + big1 + "'")
	check("Prepare", err)
	check("BindText", s.BindText(1, big1))
	check("BindBlob", s.BindBlob(1, []byte(big1)))
}

// A panic in a callback that has an error code to give becomes that code,
// and the connection is marked broken. Here the file of the connection is
// taken from the handle table behind SQLite's back.
func TestCallbackPanicBreaksConn(t *testing.T) {
	before := liveFiles()
	c, err := openRaw(t, filepath.Join(t.TempDir(), "p.db"), OpenReadWrite|OpenCreate)
	if err != nil {
		t.Fatal(err)
	}
	if c.Broken() {
		t.Fatal("new connection is broken")
	}
	if err := c.Exec("CREATE TABLE t(a)"); err != nil {
		t.Fatal(err)
	}
	for h := range liveFiles() {
		if before[h] {
			continue
		}
		f := files.get(h)
		files.remove(h)
		defer f.Close()
	}
	err = c.Exec("INSERT INTO t VALUES (1)")
	var e *Error
	if !errors.As(err, &e) || e.Primary() != 10 {
		t.Fatalf("Exec after the file vanished: %v", err)
	}
	if !c.Broken() {
		t.Fatal("not marked broken")
	}
	if err := c.Exec("SELECT 1"); !errors.Is(err, ErrBroken) {
		t.Errorf("Exec on a broken connection: %v", err)
	}
	if _, _, err := c.Prepare("SELECT 1"); !errors.Is(err, ErrBroken) {
		t.Errorf("Prepare on a broken connection: %v", err)
	}
	if err := c.Close(); !errors.Is(err, ErrBroken) {
		t.Errorf("Close on a broken connection: %v", err)
	}
}

// Callbacks that cannot go on safely mark the connection broken and let the
// panic go on. The ones that can fail cleanly return a code.
func TestCallbackPanicKinds(t *testing.T) {
	const bad = 0xdead
	c, err := openRaw(t, ":memory:", OpenReadWrite|OpenCreate)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close() // broken: frees the stack
	sp := c.sp
	// Mutex enter and leave: no code to give, and a half-taken lock is worse.
	func() {
		defer func() {
			if recover() == nil {
				t.Error("mutex enter did not panic")
			}
		}()
		host_mutex_enter(sp, bad)
	}()
	if !c.Broken() {
		t.Error("mutex panic left the connection unbroken")
	}
	// A fresh connection for each of the rest.
	cases := map[string]func(sp uintptr) uintptr{
		"read": func(sp uintptr) uintptr { return host_read(sp, bad, sp, 1, 0) },
		"lock": func(sp uintptr) uintptr { return host_lock(sp, bad, 1) },
		"realloc": func(sp uintptr) uintptr {
			p := alloc(32)
			headerOf(p).check = 0 // a block the heap did not give
			return realloc(sp, p, 8)
		},
		"try": func(sp uintptr) uintptr { return host_mutex_try(sp, bad) },
	}
	want := map[string]uintptr{"read": 10, "lock": 10, "realloc": 0, "try": 5}
	for name, f := range cases {
		c2, err := openRaw(t, ":memory:", OpenReadWrite|OpenCreate)
		if err != nil {
			t.Fatal(err)
		}
		if got := f(c2.sp); got != want[name] {
			t.Errorf("%s: got %d, want %d", name, got, want[name])
		}
		if !c2.Broken() {
			t.Errorf("%s: not broken", name)
		}
		c2.Close()
	}
}

func TestStackLayout(t *testing.T) {
	for _, page := range []uintptr{4 << 10, 16 << 10, 64 << 10} {
		for _, k := range []uintptr{1, 7, 1000, 12345, stackSize/page - 1, stackSize / page, stackSize/page + 3} {
			start := 0x7f0000000000 + k*page
			n := 2*stackSize + page
			base, end := stackLayout(start, page)
			if base%stackSize != 0 || base < start || base-start >= stackSize {
				t.Errorf("page %d start %#x: base %#x", page, start, base)
			}
			if (base-start)%page != 0 {
				t.Errorf("page %d: head trim %#x is not whole pages", page, base-start)
			}
			if end != base+stackSize+page || end%page != 0 {
				t.Errorf("page %d: end %#x", page, end)
			}
			if tail := start + n - end; tail%page != 0 || end > start+n {
				t.Errorf("page %d start %#x: tail trim %#x", page, start, tail)
			}
			if (base+stackSize)%page != 0 {
				t.Errorf("page %d: guard page not aligned", page)
			}
		}
		if got := roundUp(page+1, page); got != 2*page {
			t.Errorf("roundUp(%d+1, %d) = %d", page, page, got)
		}
		if got := roundUp(page, page); got != page {
			t.Errorf("roundUp(%d, %d) = %d", page, page, got)
		}
	}
}

// liveFiles is the set of handles in the file table.
func liveFiles() map[uintptr]bool {
	m := map[uintptr]bool{}
	for i := range files.pages {
		if p := files.pages[i].Load(); p != nil {
			for j, f := range p {
				if f != nil {
					m[uintptr(i<<10|j)] = true
				}
			}
		}
	}
	return m
}
