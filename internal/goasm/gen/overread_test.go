// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package gen

import (
	"strings"
	"testing"
)

// An inlined memcpy whose source llc knows reads the constant 16 bytes at a
// time, past its end, on the paths for lengths the C never copies. The copy in
// the body gets zero padding up to the furthest read.
func TestPlanDataPadsOverreadCopy(t *testing.T) {
	plan := func(body string) *DataPlan {
		asm := ".text\n.globl f\nf:\n" + body + "\tretq\n.Lfunc_end0:\n" +
			"\t.section\t.rodata.str1.1,\"aMS\",@progbits,1\n.L.str:\n\t.asciz\t\"CREATE TABLE x\"\n"
		u, err := SplitAsm(asm, "#")
		if err != nil {
			t.Fatal(err)
		}
		p := PlanData(u, map[string]bool{"f": true})
		if len(p.Errors) != 0 {
			t.Fatalf("errors: %v", p.Errors)
		}
		return p
	}
	p := plan("\tmovups\t.L.str+32(%rip), %xmm0\n\tmovups\t.L.str(%rip), %xmm1\n")
	label := p.Rename["f"][".L.str"]
	if got := p.Pad["f"][label]; got != 48-15 {
		t.Errorf("pad %d, want %d", got, 48-15)
	}
	if d := p.BodyData("f"); !strings.HasSuffix(d, "\n.zero 33") {
		t.Errorf("body data does not end in the padding:\n%s", d)
	}
	// A read inside the object, or one whose width is not known, gets none.
	for _, body := range []string{"\tmovq\t.L.str+7(%rip), %rax\n", "\tpcmpeqb\t.L.str(%rip), %xmm0\n"} {
		p := plan(body)
		if len(p.Pad["f"]) != 0 || strings.Contains(p.BodyData("f"), ".zero") {
			t.Errorf("%q: pad %v", body, p.Pad)
		}
	}
}
