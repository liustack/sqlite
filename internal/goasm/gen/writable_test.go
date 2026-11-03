// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.
package gen

import (
	"fmt"
	"strings"
	"testing"
)

// TestExposeWritableData: every global the C may write becomes external and
// preemptible, so that llc reaches it through the GOT, and nothing else
// changes: constants, declarations and LLVM's own globals stay as they are.
func TestExposeWritableData(t *testing.T) {
	in := strings.Join([]string{
		`@.str = private unnamed_addr constant [2 x i8] c"x\00", align 1`,
		`@names = dso_local local_unnamed_addr global [2 x ptr] [ptr @.str, ptr @.str], align 8`,
		`@counter = internal unnamed_addr global i32 0, align 4`,
		`@cfg.1 = internal global i64 2, align 8`,
		`@"quoted name" = dso_local global i32 1, align 4`,
		`@tbl = internal unnamed_addr constant [2 x i32] [i32 1, i32 2], align 4`,
		`@ext = external global i32, align 4`,
		`@llvm.used = appending global [1 x ptr] [ptr @f], section "llvm.metadata"`,
		`define i32 @f() {`,
		`  %v = load i32, ptr @counter, align 4`,
		`  ret i32 %v`,
		`}`,
	}, "\n")
	out, names, err := ExposeWritableData(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`@.str = private unnamed_addr constant [2 x i8] c"x\00", align 1`,
		`@names = local_unnamed_addr global [2 x ptr] [ptr @.str, ptr @.str], align 8`,
		`@counter = unnamed_addr global i32 0, align 4`,
		`@cfg.1 = global i64 2, align 8`,
		`@"quoted name" = global i32 1, align 4`,
		`@tbl = internal unnamed_addr constant [2 x i32] [i32 1, i32 2], align 4`,
		`@ext = external global i32, align 4`,
		`@llvm.used = appending global [1 x ptr] [ptr @f], section "llvm.metadata"`,
		`  %v = load i32, ptr @counter, align 4`,
	} {
		if !strings.Contains(out, want+"\n") && !strings.HasSuffix(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if got := fmt.Sprint(names); got != `[names counter cfg.1 "quoted name"]` {
		t.Errorf("exposed %s", got)
	}
}

// TestExposeWritableDataRejects: a linkage the generator has not reviewed
// stops it, rather than leave a global llc might still reach directly.
func TestExposeWritableDataRejects(t *testing.T) {
	for _, l := range []string{
		`@c = common global i32 0, align 4`,
		`@w = weak global i32 0, align 4`,
		`@h = hidden global i32 0, align 4`,
		`@t = internal thread_local global i32 0, align 4`,
	} {
		if _, _, err := ExposeWritableData(l); err == nil {
			t.Errorf("%q accepted", l)
		}
	}
}

// TestPlanDataARM64Writable: a writable global that the code reaches through
// the GOT goes into the data symbol, which then becomes writable. A direct
// reference to a writable global would be a copy or a fixed address, and
// stops the plan.
func TestPlanDataARM64Writable(t *testing.T) {
	asm := ".text\n.globl f\nf:\n\tldr\tx8, :got:counter\n\tldr\tw9, [x8]\n\tadr\tx10, tbl\n\tret\n.Lfunc_end0:\n" +
		".section\t.rodata,\"a\",@progbits\n\t.p2align\t3, 0x0\ntbl:\n\t.xword\tcounter\n" +
		".type\tcounter,@object\n\t.bss\n\t.globl\tcounter\n\t.p2align\t2, 0x0\ncounter:\n\t.zero\t4\n" +
		".section\t.data.rel,\"aw\",@progbits\n\t.globl\tptrs\n\t.p2align\t3, 0x0\nptrs:\n\t.xword\ttbl\n\t.xword\tf\n"
	u, err := SplitAsm(asm, "//")
	if err != nil {
		t.Fatal(err)
	}
	p := PlanDataARM64(u, map[string]bool{"f": true})
	if len(p.Errors) != 0 {
		t.Fatalf("errors: %v", p.Errors)
	}
	if !p.Writable {
		t.Error("the data symbol holds a writable global and is not writable")
	}
	if _, ok := p.BlobOff["counter"]; !ok {
		t.Errorf("counter, reached through the GOT, is not in the data symbol: %v", p.BlobOff)
	}
	if _, ok := p.BlobOff["ptrs"]; ok {
		t.Errorf("ptrs, which nothing uses, is in the data symbol: %v", p.BlobOff)
	}
	// tbl holds the address of counter: a relocation to the data symbol.
	if len(p.Relocs) != 1 || p.Relocs[0].Func || p.Relocs[0].Add != int64(p.BlobOff["counter"]) {
		t.Errorf("relocations: %+v", p.Relocs)
	}
	for _, l := range []string{"adr\tx8, counter", "ldr\tw8, counter"} {
		asm := ".text\n.globl f\nf:\n\t" + l + "\n\tret\n.Lfunc_end0:\n" +
			"\t.bss\n\t.globl\tcounter\n\t.p2align\t2, 0x0\ncounter:\n\t.zero\t4\n"
		u, err := SplitAsm(asm, "//")
		if err != nil {
			t.Fatal(err)
		}
		p := PlanDataARM64(u, map[string]bool{"f": true})
		if !strings.Contains(fmt.Sprint(p.Errors), "writable data counter") {
			t.Errorf("%q: %v", l, p.Errors)
		}
	}
}

// TestPlanDataReadOnlyStaysReadOnly: without a writable global the data
// symbol stays read-only.
func TestPlanDataReadOnlyStaysReadOnly(t *testing.T) {
	asm := ".text\n.globl f\nf:\n\tadr\tx8, tbl\n\tret\n.Lfunc_end0:\n" +
		".section\t.rodata,\"a\",@progbits\n\t.p2align\t3, 0x0\ntbl:\n\t.xword\t3\n"
	u, err := SplitAsm(asm, "//")
	if err != nil {
		t.Fatal(err)
	}
	if p := PlanDataARM64(u, map[string]bool{"f": true}); p.Writable || len(p.Errors) != 0 {
		t.Errorf("writable %v, errors %v", p.Writable, p.Errors)
	}
}

// TestLowerARM64EngineGOT: a load of a GOT entry becomes the address of the
// global in the data symbol, as an adr of read-only data does.
func TestLowerARM64EngineGOT(t *testing.T) {
	fn, env := a64TestEnv()
	env.Plan.BlobOff["counter"] = 40
	out, _, err := LowerARM64EngineFunc([]string{
		"str x30, [sp, #-16]!",
		"ldr x8, :got:counter",
		"ldr w9, [x8]",
		"ldr x30, [sp], #16",
		"ret",
	}, fn, env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "adrp x8, __goasm_rodata+40\n\tadd x8, x8, :lo12:__goasm_rodata+40\n") {
		t.Errorf("the GOT load is not the address of counter:\n%s", out)
	}
	if _, _, err := LowerARM64EngineFunc([]string{"ldr x8, :got:nowhere", "ret"}, fn, env); err == nil {
		t.Error("a GOT load of unplaced data was taken")
	}
}

// TestEmitRodataWritable: a data symbol that holds writable globals is
// written without RODATA, so the Go linker puts it where the code can write.
func TestEmitRodataWritable(t *testing.T) {
	p := &DataPlan{Blob: []byte{1}, Writable: true}
	out := EmitRodata(p, NameMap{}, "")
	if !strings.Contains(out, "GLOBL ·engine_rodata(SB), NOPTR, $8\n") {
		t.Errorf("not writable:\n%s", out)
	}
}

// TestPlanDataAMD64Writable: on amd64 the code reaches a writable global
// through movq sym@GOTPCREL(%rip), which puts it into the data symbol. A
// direct RIP-relative reference to it, or any other use of its GOT entry,
// stops the plan.
func TestPlanDataAMD64Writable(t *testing.T) {
	plan := func(l string) *DataPlan {
		asm := ".text\n.globl f\nf:\n\t" + l + "\n\tretq\n.Lfunc_end0:\n" +
			"\t.bss\n\t.globl\tcounter\n\t.p2align\t2, 0x0\ncounter:\n\t.zero\t4\n"
		u, err := SplitAsm(asm, "#")
		if err != nil {
			t.Fatal(err)
		}
		return PlanData(u, map[string]bool{"f": true})
	}
	p := plan("movq\tcounter@GOTPCREL(%rip), %rax")
	if len(p.Errors) != 0 {
		t.Fatalf("errors: %v", p.Errors)
	}
	if _, ok := p.BlobOff["counter"]; !ok || !p.Writable {
		t.Errorf("counter: %v, writable %v", p.BlobOff, p.Writable)
	}
	if _, ok := p.GOTSlot["counter"]; ok {
		t.Errorf("a slot for counter, which only a movq loads: %v", p.GOTSlot)
	}
	// x86 folds the load of a GOT entry into a compare or an add of the
	// address: the global gets a slot of its own in the data symbol that
	// holds its address, with a pointer to it.
	for _, op := range []string{"cmpq", "addq", "subq"} {
		p := plan(op + "\tcounter@GOTPCREL(%rip), %rdi")
		if len(p.Errors) != 0 {
			t.Fatalf("%s: errors: %v", op, p.Errors)
		}
		slot, ok := p.GOTSlot["counter"]
		if !ok || slot%8 != 0 || slot+8 > len(p.Blob) {
			t.Fatalf("%s: slot %d of %d bytes", op, slot, len(p.Blob))
		}
		want := BlobReloc{Off: slot, Add: int64(p.BlobOff["counter"]), Label: "counter", GOT: true}
		if len(p.Relocs) != 1 || p.Relocs[0] != want {
			t.Errorf("%s: relocations %+v, want %+v", op, p.Relocs, want)
		}
	}
	for l, want := range map[string]string{
		"movl\tcounter(%rip), %eax":            "writable data counter",
		"leaq\tcounter(%rip), %rax":            "writable data counter",
		"andq\tcounter@GOTPCREL(%rip), %rax":   "unreviewed GOT reference",
		"movl\tcounter@GOTPCREL(%rip), %eax":   "unreviewed GOT reference",
		"movq\tcounter@GOTPCREL+8(%rip), %rax": "unreviewed GOT reference",
		"cmpq\t%rdi, counter@GOTPCREL(%rip)":   "unreviewed GOT reference",
	} {
		if p := plan(l); !strings.Contains(fmt.Sprint(p.Errors), want) {
			t.Errorf("%q: %v, want %q", l, p.Errors, want)
		}
	}
}

// TestLowerAMD64GOT: the load of a GOT entry becomes a LEA of the global in
// the data symbol, recorded as a GOT reference for the post-link check.
func TestLowerAMD64GOT(t *testing.T) {
	p := &DataPlan{BlobOff: map[string]int{"counter": 24}}
	env := &LowerEnv{Plan: p, Funcs: map[string]bool{"f": true}}
	var recs []RefRecord
	got, err := env.dataRefs("f", "movq\tcounter@GOTPCREL(%rip), %rax", &recs)
	if err != nil || got != "leaq\t__goasm_rodata+24(%rip), %rax" {
		t.Fatalf("%q %v", got, err)
	}
	if len(recs) != 1 || recs[0] != (RefRecord{Label: "counter", Blob: true, GOT: true}) {
		t.Errorf("records: %+v", recs)
	}
	if _, err := env.dataRefs("f", "movq\tnowhere@GOTPCREL(%rip), %rax", nil); err == nil {
		t.Error("a GOT load of unplaced data was taken")
	}
	// A folded load reads the slot of the global instead.
	p.GOTSlot = map[string]int{"counter": 64}
	recs = nil
	got, err = env.dataRefs("f", "cmpq\tcounter@GOTPCREL(%rip), %rdi", &recs)
	if err != nil || got != "cmpq\t__goasm_rodata+64(%rip), %rdi" {
		t.Fatalf("folded: %q %v", got, err)
	}
	if len(recs) != 1 || recs[0] != (RefRecord{Label: "counter", Blob: true, GOT: true, Slot: true}) {
		t.Errorf("folded records: %+v", recs)
	}
	delete(p.GOTSlot, "counter")
	if _, err := env.dataRefs("f", "cmpq\tcounter@GOTPCREL(%rip), %rdi", nil); err == nil {
		t.Error("a folded GOT load without a slot was taken")
	}
}
