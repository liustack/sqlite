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
	"strconv"
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
		e, err := expose(l, m, "writable global")
		if err != nil {
			return "", nil, err
		}
		lines[i] = e
		names = append(names, strings.TrimPrefix(m[1], "@"))
	}
	return strings.Join(lines, "\n"), names, nil
}

// expose rewrites the definition l (m its reGlobalDef match) without the
// words that let llc reach it directly. what names it in an error.
func expose(l string, m []string, what string) (string, error) {
	var keep []string
	for _, w := range strings.Fields(m[2]) {
		switch {
		case exposeDrop[w]:
		case exposeKeep[w]:
			keep = append(keep, w)
		default:
			return "", fmt.Errorf("%s %s is %s, which the generator does not take: %q", what, m[1], w, l)
		}
	}
	prefix := m[1] + " = "
	if len(keep) > 0 {
		prefix += strings.Join(keep, " ") + " "
	}
	return prefix + m[3] + " " + l[len(m[0]):], nil
}

// reMemCopy is a call of LLVM's memcpy or memmove, and reGlobalUse a global
// a line names.
var (
	reMemCopy   = regexp.MustCompile(`\bcall void @llvm\.mem(?:cpy|move)\.`)
	reGlobalUse = regexp.MustCompile(`@(?:"[^"]*"|[-\w.$]+)`)
)

// ExposeCopiedPointerTables does for constants what ExposeWritableData does
// for writable globals, for the constants that hold pointers and that a
// memcpy or memmove copies. Such a copy is how C initializes a local array or
// struct (FTS5's table of built-in tokenizers), and llc expands it into
// direct loads of the constant (an arm64 ldr q of a label, an amd64 movups
// sym(%rip)). The lowering cannot copy the constant into the body, as its
// pointers need relocations. Reached through the GOT, it is read through a
// register that holds its address in engine_rodata. Every other constant is
// left alone. It returns the new module and the names it changed.
func ExposeCopiedPointerTables(ll string) (string, []string, error) {
	lines := strings.Split(ll, "\n")
	copied := map[string]bool{}
	for _, l := range lines {
		if !reMemCopy.MatchString(l) {
			continue
		}
		for _, g := range reGlobalUse.FindAllString(l, -1) {
			copied[g] = true
		}
	}
	var names []string
	for i, l := range lines {
		m := reGlobalDef.FindStringSubmatch(l)
		if m == nil || m[3] != "constant" || !copied[m[1]] || strings.HasPrefix(m[1], "@llvm.") {
			continue
		}
		words := strings.Fields(m[2])
		if len(words) > 0 && words[0] == "external" {
			continue
		}
		// The initializer names another global or a function: a pointer.
		if !reGlobalUse.MatchString(l[len(m[0]):]) {
			continue
		}
		e, err := expose(l, m, "copied constant")
		if err != nil {
			return "", nil, err
		}
		lines[i] = e
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

// reGOTPCREL is any use of a GOT entry in amd64 text, and reGOTUse the ones
// the lowering takes: a whole load of the address into a 64-bit register,
// and the compare, add and subtract of the address that x86 folds the load
// into.
var (
	reGOTPCREL = regexp.MustCompile(`@GOTPCREL`)
	reGOTUse   = regexp.MustCompile(`^(movq|cmpq|addq|subq)\s+([.\w$]+)@GOTPCREL\(%rip\),\s*(%r\w+)$`)
)

// amd64GOTRef reads a use of a GOT entry in the cleaned line l: the
// instruction, the global and the register. ok is false when l names no GOT
// entry, and err is set when it does in a form the lowering does not take.
func amd64GOTRef(l string) (op, sym, reg string, ok bool, err error) {
	if !reGOTPCREL.MatchString(l) {
		return "", "", "", false, nil
	}
	m := reGOTUse.FindStringSubmatch(norm(l))
	if m == nil {
		return "", "", "", true, fmt.Errorf("unreviewed GOT reference %q", l)
	}
	return m[1], m[2], m[3], true, nil
}

// lowerAMD64GOT turns the load of the GOT entry of sym into reg into a LEA
// of sym in engine_rodata, and a compare, add or subtract that x86 folded it
// into into the same instruction on the slot of sym (DataPlan.GOTSlot).
func (env *LowerEnv) lowerAMD64GOT(op, sym, reg string, rec *[]RefRecord) (string, error) {
	off, ok := env.Plan.BlobOff[sym]
	if !ok {
		return "", fmt.Errorf("GOT load of %s, which is not in engine_rodata", sym)
	}
	if op == "movq" {
		if rec != nil {
			*rec = append(*rec, RefRecord{Label: sym, Blob: true, GOT: true})
		}
		return "leaq\t__goasm_rodata+" + strconv.Itoa(off) + "(%rip), " + reg, nil
	}
	slot, ok := env.Plan.GOTSlot[sym]
	if !ok {
		return "", fmt.Errorf("%s of the GOT entry of %s, which has no slot", op, sym)
	}
	if rec != nil {
		*rec = append(*rec, RefRecord{Label: sym, Blob: true, GOT: true, Slot: true})
	}
	return op + "\t__goasm_rodata+" + strconv.Itoa(slot) + "(%rip), " + reg, nil
}
