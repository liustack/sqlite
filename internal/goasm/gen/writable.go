// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.
//
// Writable data. SQLite keeps state in writable globals (its configuration,
// the state of its allocator and page cache, the tables of built-in functions
// it links together at start), shared by the whole process as a Go global is.
// They go into the one Go data symbol with the read-only data (DataPlan), which
// is then written without RODATA.
//
// Go assembly cannot attach a relocation to the bytes of an instruction, so
// the lowering must rewrite every reference to data, and it can only rewrite
// one that takes an address. A direct load of a global (an arm64 ldr of a
// label, an amd64 movq sym(%rip)) would have to become a copy in the body,
// which is wrong for data that changes. So before llc runs,
// ExposeWritableData makes every writable global external and preemptible:
// position-independent code then reaches it only through the GOT, with one
// load of its address into a register (arm64 ldr x, :got:sym, amd64 movq
// sym@GOTPCREL(%rip), %reg), which the lowering turns into the address of the
// global in the data symbol.
package gen

import (
	"fmt"
	"regexp"
	"strings"
)

// reGlobalDef is a definition of a global in LLVM IR: its name, the words
// before global or constant (linkage, visibility, unnamed_addr and the like)
// and which of the two it is.
var reGlobalDef = regexp.MustCompile(`^(@(?:"[^"]*"|[-\w.$]+)) = ((?:[a-z_]+(?:\([^)]*\))? )*)(global|constant) `)

// exposeDrop are the words a writable global loses: the linkages and the
// marks that let llc reach it directly.
var exposeDrop = map[string]bool{"internal": true, "private": true, "dso_local": true}

// exposeKeep are the words a writable global may keep.
var exposeKeep = map[string]bool{"unnamed_addr": true, "local_unnamed_addr": true}

// ExposeWritableData makes every writable global defined in the module ll
// external and not dso_local, so that llc reaches it through the GOT. It
// returns the new module and the names of the globals it changed. A
// constant, a declaration (external) and LLVM's own globals (llvm.*) are
// left alone. Any other linkage or mark (common, weak, hidden,
// thread_local, ...) is an error: it has not been reviewed.
func ExposeWritableData(ll string) (string, []string, error) {
	lines := strings.Split(ll, "\n")
	var names []string
	for i, l := range lines {
		m := reGlobalDef.FindStringSubmatch(l)
		if m == nil || m[3] != "global" || strings.HasPrefix(m[1], "@llvm.") {
			continue
		}
		words := strings.Fields(m[2])
		if len(words) > 0 && words[0] == "external" {
			continue
		}
		var keep []string
		for _, w := range words {
			switch {
			case exposeDrop[w]:
			case exposeKeep[w]:
				keep = append(keep, w)
			default:
				return "", nil, fmt.Errorf("writable global %s is %s, which the generator does not take: %q", m[1], w, l)
			}
		}
		prefix := m[1] + " = "
		if len(keep) > 0 {
			prefix += strings.Join(keep, " ") + " "
		}
		lines[i] = prefix + "global " + l[len(m[0]):]
		names = append(names, strings.TrimPrefix(m[1], "@"))
	}
	return strings.Join(lines, "\n"), names, nil
}

// writableSection says whether llc's section of that name holds data the
// code may write. .data.rel.ro is the exception: PIC code puts constant
// tables that hold pointers there, writable only while relocations are
// applied.
func writableSection(s string) bool {
	if strings.HasPrefix(s, ".data.rel.ro") {
		return false
	}
	for _, p := range []string{".data", ".bss"} {
		if s == p || strings.HasPrefix(s, p+".") {
			return true
		}
	}
	return false
}
