// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package gen

import (
	"reflect"
	"strings"
	"testing"
)

func TestStripCGProfile(t *testing.T) {
	in := `define void @f() {
  ret void
}
!llvm.module.flags = !{!0, !1, !7, !2}
!0 = !{i32 1, !"wchar_size", i32 4}
!1 = !{i32 8, !"PIC Level", i32 2}
!2 = !{i32 7, !"frame-pointer", i32 2}
!7 = !{i32 5, !"CG Profile", !8}
!8 = !{!9}
`
	want := `define void @f() {
  ret void
}
!llvm.module.flags = !{!0, !1, !2}
!0 = !{i32 1, !"wchar_size", i32 4}
!1 = !{i32 8, !"PIC Level", i32 2}
!2 = !{i32 7, !"frame-pointer", i32 2}
!8 = !{!9}
`
	if got := StripCGProfile(in); got != want {
		t.Fatalf("StripCGProfile:\n%s\nwant\n%s", got, want)
	}
	for _, s := range []string{want, "define void @g() {\n}\n"} {
		if got := StripCGProfile(s); got != s {
			t.Fatalf("a module without the flag changed:\n%s", got)
		}
	}
	last := strings.Replace(in, "!{!0, !1, !7, !2}", "!{!0, !1, !2, !7}", 1)
	if got := StripCGProfile(last); !strings.Contains(got, "!llvm.module.flags = !{!0, !1, !2}\n") {
		t.Fatalf("the flag last in the list stayed:\n%s", got)
	}
}

func TestInlineRemarks(t *testing.T) {
	in := `engine/sqlite.c:10:5: remark: 'f' inlined into 'g' with (cost=5, threshold=400) at callsite g:2:5; [-Rpass=inline]
   10 |     f();
      |     ^
engine/sqlite.c:12:5: remark: 'h' not inlined into 'g' because too costly to inline [-Rpass-missed=inline]
warning: something else
engine/sqlite3.c:20:3: remark: 'k' inlined into 'm' with (cost=1, threshold=45) at callsite m:1:3; [-Rpass=inline]
   20 |   k("' inlined into '");
remark: engine/libc/string/bcmp.c:7:9: 'memcmp' inlined into 'bcmp' with (cost=80, threshold=400) at callsite bcmp:2:9;
remark: engine/sqlite3.c:30:3: 'q' not inlined into 'r' because too costly to inline (cost=500, threshold=400)
`
	want := "engine/sqlite.c:10:5: remark: 'f' inlined into 'g' with (cost=5, threshold=400) at callsite g:2:5; [-Rpass=inline]\n" +
		"engine/sqlite3.c:20:3: remark: 'k' inlined into 'm' with (cost=1, threshold=45) at callsite m:1:3; [-Rpass=inline]\n" +
		"remark: engine/libc/string/bcmp.c:7:9: 'memcmp' inlined into 'bcmp' with (cost=80, threshold=400) at callsite bcmp:2:9;\n"
	if got := InlineRemarks(in); got != want {
		t.Fatalf("InlineRemarks = %q, want %q", got, want)
	}
}

func TestProfileNames(t *testing.T) {
	text := `sqlite3_open_v2
# Func Hash:
1
# Num Counters:
1
# Counter Values:
3

sqlite.c:vdbeFreeOpArray
# Func Hash:
2
# Num Counters:
1
# Counter Values:
0
`
	got := ProfileNames(text)
	want := map[string]bool{"sqlite3_open_v2": true, "sqlite.c:vdbeFreeOpArray": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ProfileNames = %v, want %v", got, want)
	}
}

func TestUnprofiledFunctions(t *testing.T) {
	ir := `define void @sqlite3_free(ptr %0) #0 !prof !62 {
define internal [2 x i64] @sqlite3VdbeExec(ptr noundef %0) #5 !PGOFuncName !2434 {
define internal i32 @vdbeNewThing(ptr %0) #5 !PGOFuncName !2655 {
define internal void @sqlite3VdbeMemRelease(ptr %0) #4 !prof !347 !PGOFuncName !348 {
define i32 @sqlite3_new_thing(ptr %0) #0 {
define i32 @"caf\C3\A9"(i32 %0) #0 {
define internal i32 @"na\C3\AFve"(i32 %0) #0 !PGOFuncName !349 {
declare void @host_trap()
!2434 = !{!"sqlite.c:sqlite3VdbeExec"}
!2655 = !{!"sqlite.c:vdbeNewThing"}
!348 = !{!"sqlite.c:sqlite3VdbeMemRelease"}
!349 = !{!"sqlite.c:na\C3\AFve"}
`
	profile := map[string]bool{"sqlite3_free": true, "sqlite.c:sqlite3VdbeExec": true,
		"sqlite.c:sqlite3VdbeMemRelease": true, "sqlite3_new_thing": true, "café": true, "sqlite.c:naïve": true}
	stale, fresh := UnprofiledFunctions(ir, profile)
	if !reflect.DeepEqual(stale, []string{"café", "sqlite.c:naïve", "sqlite.c:sqlite3VdbeExec", "sqlite3_new_thing"}) {
		t.Fatalf("stale = %v", stale)
	}
	if !reflect.DeepEqual(fresh, []string{"sqlite.c:vdbeNewThing"}) {
		t.Fatalf("fresh = %v", fresh)
	}
}

func TestStaleWarnings(t *testing.T) {
	if w := StaleWarnings([]string{"sqlite.c:vdbeA", "sqlite3_b"}, []string{"sqlite3_new"}, []string{"sqlite3VdbeExec"}, 2); len(w) != 0 {
		t.Fatalf("two stale functions of at most two and a new one: %q", w)
	}
	for _, c := range []struct{ stale, fresh []string }{
		{[]string{"sqlite.c:sqlite3VdbeExec"}, nil},
		{nil, []string{"sqlite3VdbeExec"}},
	} {
		w := StaleWarnings(c.stale, c.fresh, []string{"sqlite3VdbeExec"}, 30)
		if len(w) != 1 || !strings.Contains(w[0], "sqlite3VdbeExec has no profile") {
			t.Fatalf("stale %q new %q: %q", c.stale, c.fresh, w)
		}
	}
	w := StaleWarnings([]string{"a", "b", "c"}, nil, []string{"sqlite3VdbeExec"}, 2)
	if len(w) != 1 || !strings.Contains(w[0], "of 3 functions") {
		t.Fatalf("three stale functions of at most two: %q", w)
	}
}

func TestMisexpectLines(t *testing.T) {
	in := "engine/a.c:3:9: warning: potential performance regression from use of __builtin_expect(): annotation was correct on 2.00% (1 / 50) of profiled executions [-Wmisexpect]\n" +
		"    3 |     if (unlikely(x))\n" +
		"engine/a.c:7:1: warning: unused variable 'y' [-Wunused-variable]\n"
	got := MisexpectLines(in)
	if len(got) != 1 || !strings.HasPrefix(got[0], "engine/a.c:3:9:") {
		t.Fatalf("MisexpectLines = %q, want the one -Wmisexpect warning", got)
	}
}
