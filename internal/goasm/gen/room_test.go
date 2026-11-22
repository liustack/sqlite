// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package gen

import (
	"context"
	"fmt"
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
