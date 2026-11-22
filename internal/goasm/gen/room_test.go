// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package gen

import (
	"context"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRoomCheck: every unified function has the room check for a fault on
// windows after its TEXT, inside #ifdef GOOS_windows, so that the code of
// the other systems is what it was. A NOSPLIT function loads g first, and
// one without a frame says NOFRAME, so that the call of goasm_room does not
// make the assembler save BP. A register-entry leaf has none: its callers
// have it.
func TestRoomCheck(t *testing.T) {
	check := fmt.Sprintf("#ifdef GOOS_windows\n\tLEAQ -%d(SP), R12\n\tCMPQ R12, 16(R14)\n\tJHI 2(PC)\n\tCALL ·goasm_room(SB)\n\tBYTE $0x90\n#endif\n", RoomBelow)
	leafCheck := strings.Replace(check, "#ifdef GOOS_windows\n", "#ifdef GOOS_windows\n\tMOVQ (TLS), R14\n", 1)
	for _, c := range []struct {
		m    Meta
		want string
	}{
		{Meta{Kind: Unified, Frame: 48, ArgBytes: 24}, "TEXT ·f(SB), $48-24\n\tNO_LOCAL_POINTERS\n" + check},
		{Meta{Kind: Unified, NoSplit: true, ArgBytes: 24}, "TEXT ·f(SB), NOSPLIT|NOFRAME, $0-24\n\tNO_LOCAL_POINTERS\n" + leafCheck},
		{Meta{Kind: Unified, NoSplit: true, Frame: 16, ArgBytes: 24}, "TEXT ·f(SB), NOSPLIT, $16-24\n\tNO_LOCAL_POINTERS\n" + leafCheck},
		{Meta{Kind: Register, NoSplit: true, Frame: 8}, "TEXT ·f(SB), NOSPLIT, $8-0\n"},
		{Meta{Kind: Register, NoSplit: true}, "TEXT ·f(SB), NOSPLIT, $0-0\n"},
	} {
		if got := funcHeader("f", c.m); got != c.want {
			t.Errorf("funcHeader of %+v:\n%s\nwant\n%s", c.m, got, c.want)
		}
	}
	// The guard is 5024 bytes above the bottom of a stack on windows/amd64,
	// and a register-entry leaf takes less than 128 bytes and its return
	// address below the body that called it.
	if RoomBelow+5024-136 < 16<<10 {
		t.Fatalf("a register-entry leaf has %d bytes of room, less than 16 KiB", RoomBelow+5024-136)
	}
	if !strings.Contains(roomText(), fmt.Sprintf("TEXT ·goasm_room(SB), $%d-0\n", RoomFrame)) {
		t.Fatalf("goasm_room:\n%s", roomText())
	}
	if strings.Contains(roomText(), "#ifdef") || !strings.HasSuffix(RoomFile, "_windows_amd64.s") {
		t.Fatalf("goasm_room has to build on windows/amd64 by the name of its file (%s):\n%s", RoomFile, roomText())
	}
}

// TestRoomCheckAssembles: the assembler of windows/amd64 takes a function
// with the room check before the PCALIGN of a body long enough that Go's
// own stack check needs the long form of its branch. Go before 1.26.11 and
// 1.27.4 loops forever when the branch of the room check lands on the
// PCALIGN (golang/go#81792).
func TestRoomCheckAssembles(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("#include \"textflag.h\"\n#include \"funcdata.h\"\n\n")
	b.WriteString(funcHeader("f", Meta{Kind: Unified, Frame: 64, ArgBytes: 32}))
	b.WriteString("\tPCALIGN $16\n")
	for range 64 {
		b.WriteString("\tQUAD $0\n")
	}
	b.WriteString("\tRET\n\n")
	b.WriteString(roomText())
	src := filepath.Join(dir, "f.s")
	if err := os.WriteFile(src, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := exec.Command("go", "env", "GOROOT", "GOTOOLDIR").Output()
	if err != nil {
		t.Fatal(err)
	}
	dirs := strings.Fields(string(env))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// The asm binary itself, since go tool asm leaves it running when killed.
	cmd := exec.CommandContext(ctx, filepath.Join(dirs[1], "asm"), "-p", "x", "-D", "GOOS_windows", "-D", "GOARCH_amd64",
		"-I", filepath.Join(dirs[0], "pkg", "include"),
		"-o", filepath.Join(dir, "f.o"), src)
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal("the assembler of windows/amd64 did not finish in a minute")
	}
	if err != nil {
		t.Fatalf("assemble: %v\n%s", err, out)
	}
}

// TestRoomCall: on windows the engine calls a Go function of the host
// through its room wrapper, which makes the room after the function has
// returned, and calls a generated function as before. Both calls are five
// bytes, so the body is the same length on every system.
func TestRoomCall(t *testing.T) {
	names := NameMap{"sqlite3_step": "sqlite3_step"}
	host := map[string]bool{"host_read": true}
	if got, want := callText("sqlite3_step", names, host), "\tCALL ·sqlite3_step(SB)\n"; got != want {
		t.Errorf("call of a generated function:\n%s\nwant\n%s", got, want)
	}
	want := "#ifdef GOOS_windows\n\tCALL ·host_read_room(SB)\n#else\n\tCALL ·host_read(SB)\n#endif\n"
	if got := callText("host_read", NameMap{"host_read": "host_read"}, host); got != want {
		t.Errorf("call of the host:\n%s\nwant\n%s", got, want)
	}
}

// TestRoomWrappers: a wrapper calls the function with its arguments, makes
// the room and returns what the function returned, and the race detector
// adds nothing after the room is made. The file is as gofmt writes it.
func TestRoomWrappers(t *testing.T) {
	got := RoomWrappers("csqlite", "sqlite_x", map[string]Sig{
		"host_poll": {Params: []Type{I64}, Ret: Void},
		"malloc":    {Params: []Type{I64, I64}, Ret: Ptr},
		"f":         {Params: []Type{F64, JSV, Ptr}, Ret: F64},
		"j":         {Params: nil, Ret: JSV},
	})
	want := `// Code generated by internal/goasm/gen. DO NOT EDIT.

//go:build sqlite_x && amd64

package csqlite

// goasm_room is in ` + RoomFile + `.
func goasm_room()

//go:norace
func f_room(a0 float64, a1, a2 uintptr, a3 uintptr) float64 {
	r := f(a0, a1, a2, a3)
	goasm_room()
	return r
}

//go:norace
func host_poll_room(a0 uintptr) {
	host_poll(a0)
	goasm_room()
}

//go:norace
func j_room() (uintptr, uintptr) {
	r0, r1 := j()
	goasm_room()
	return r0, r1
}

//go:norace
func malloc_room(a0 uintptr, a1 uintptr) uintptr {
	r := malloc(a0, a1)
	goasm_room()
	return r
}
`
	if got != want {
		t.Errorf("RoomWrappers:\n%s\nwant\n%s", got, want)
	}
	if f, err := format.Source([]byte(got)); err != nil || string(f) != got {
		t.Errorf("not as gofmt writes it (%v):\n%s", err, f)
	}
	if !strings.HasSuffix(RoomGoFile, "_windows_amd64.go") {
		t.Errorf("%s does not build on windows/amd64 only", RoomGoFile)
	}
}

// TestRoomAddressed: the engine reaches a Go function of the host only by a
// direct call, which callText turns into a call of its room wrapper. A
// pointer to one, in a function or in engine_rodata, would be called
// without the wrapper, so the generator refuses it.
func TestRoomAddressed(t *testing.T) {
	host := map[string]bool{"host_read": true}
	ok := []Meta{{Name: "f", Rec: &FuncRecord{Refs: []RefRecord{{Label: "g", Func: true}, {Label: "host_read"}}}}}
	if err := RoomAddressed(ok, &DataPlan{Relocs: []BlobReloc{{Sym: "g", Func: true}}}, host); err != nil {
		t.Errorf("no pointer to the host: %v", err)
	}
	if err := RoomAddressed(ok, nil, host); err != nil {
		t.Errorf("no plan: %v", err)
	}
	inFunc := []Meta{{Name: "f", Rec: &FuncRecord{Refs: []RefRecord{{Label: "host_read", Func: true}}}}}
	if err := RoomAddressed(inFunc, nil, host); err == nil || !strings.Contains(err.Error(), "host_read") {
		t.Errorf("a pointer to host_read in f: %v", err)
	}
	inData := &DataPlan{Relocs: []BlobReloc{{Sym: "host_read", Func: true}}}
	if err := RoomAddressed(ok, inData, host); err == nil || !strings.Contains(err.Error(), "host_read") {
		t.Errorf("a pointer to host_read in engine_rodata: %v", err)
	}
}
