// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && (amd64 || arm64))

package csqlite

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestCallsThroughRoom: on windows/amd64 the generated code calls every Go
// function of the package through its wrapper in
// engine_room_windows_amd64.go, which calls the function, then goasm_room,
// and returns. The stack may shrink while the function runs, and the
// generated code checks the room for a fault only where one of its own
// functions starts.
func TestCallsThroughRoom(t *testing.T) {
	files, err := filepath.Glob("*_amd64.s")
	if err != nil {
		t.Fatal(err)
	}
	call := regexp.MustCompile(`CALL ·([A-Za-z0-9_]+)\(SB\)`)
	text := regexp.MustCompile(`(?m)^TEXT ·([A-Za-z0-9_]+)\(SB\)`)
	pair := regexp.MustCompile(`#ifdef GOOS_windows\n\tCALL ·([A-Za-z0-9_]+)_room\(SB\)\n#else\n\tCALL ·([A-Za-z0-9_]+)\(SB\)\n#endif\n`)
	texts := map[string]bool{}
	calls := map[string]int{}  // calls of each function outside the generated code
	paired := map[string]int{} // of those, the ones in the pair
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range text.FindAllSubmatch(b, -1) {
			texts[string(m[1])] = true
		}
		for _, m := range call.FindAllSubmatch(b, -1) {
			calls[string(m[1])]++
		}
		for _, m := range pair.FindAllSubmatch(b, -1) {
			if string(m[1]) != string(m[2]) {
				t.Errorf("windows calls %s_room where the others call %s", m[1], m[2])
			}
			paired[string(m[2])]++
		}
	}
	for name := range texts {
		delete(calls, name)
	}
	if calls["host_poll"] == 0 || calls["malloc"] == 0 || calls["host_write"] == 0 {
		t.Fatalf("the generated code calls %v: no host_poll, malloc or host_write", calls)
	}
	wrappers := roomWrappers(t)
	for name, n := range calls {
		if strings.HasSuffix(name, "_room") {
			if !wrappers[name] {
				t.Errorf("the generated code calls %s, which %s does not define", name, roomGoFile)
			}
			continue
		}
		if paired[name] != n {
			t.Errorf("%d of the %d calls of %s are not through %s_room on windows", n-paired[name], n, name, name)
		}
	}
}

const roomGoFile = "engine_room_windows_amd64.go"

// roomWrappers reads roomGoFile and checks each wrapper: go:norace, and a
// body that calls the function it wraps with its own arguments, then
// goasm_room, then returns what the function returned.
func roomWrappers(t *testing.T) map[string]bool {
	f, err := parser.ParseFile(token.NewFileSet(), roomGoFile, nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		name := fd.Name.Name
		got[name] = true
		if fd.Doc == nil || !hasDirective(fd.Doc, "//go:norace") {
			t.Errorf("%s does not say go:norace", name)
		}
		if why := wrapsRoom(fd, strings.TrimSuffix(name, "_room")); why != "" {
			t.Errorf("%s: %s", name, why)
		}
	}
	return got
}

func hasDirective(g *ast.CommentGroup, d string) bool {
	for _, c := range g.List {
		if c.Text == d {
			return true
		}
	}
	return false
}

// wrapsRoom says what is wrong with the wrapper fd of the function inner,
// or "".
func wrapsRoom(fd *ast.FuncDecl, inner string) string {
	list := fd.Body.List
	var params []string
	for _, f := range fd.Type.Params.List {
		for _, n := range f.Names {
			params = append(params, n.Name)
		}
	}
	isCall := func(e ast.Expr, fn string, args []string) bool {
		c, ok := e.(*ast.CallExpr)
		if !ok || len(c.Args) != len(args) {
			return false
		}
		if id, ok := c.Fun.(*ast.Ident); !ok || id.Name != fn {
			return false
		}
		for i, a := range c.Args {
			if id, ok := a.(*ast.Ident); !ok || id.Name != args[i] {
				return false
			}
		}
		return true
	}
	isStmt := func(s ast.Stmt, fn string, args []string) bool {
		e, ok := s.(*ast.ExprStmt)
		return ok && isCall(e.X, fn, args)
	}
	if fd.Type.Results == nil {
		if len(list) != 2 || !isStmt(list[0], inner, params) || !isStmt(list[1], "goasm_room", nil) {
			return "its body is not the call, then goasm_room()"
		}
		return ""
	}
	if len(list) != 3 {
		return "its body is not three statements"
	}
	a, ok := list[0].(*ast.AssignStmt)
	if !ok || a.Tok != token.DEFINE || len(a.Rhs) != 1 || !isCall(a.Rhs[0], inner, params) {
		return "it does not first keep what the call returns"
	}
	if !isStmt(list[1], "goasm_room", nil) {
		return "its second statement is not goasm_room()"
	}
	r, ok := list[2].(*ast.ReturnStmt)
	if !ok || len(r.Results) != len(a.Lhs) {
		return "it does not return what the call returned"
	}
	for i, e := range r.Results {
		id, ok := e.(*ast.Ident)
		lhs, _ := a.Lhs[i].(*ast.Ident)
		if !ok || lhs == nil || id.Name != lhs.Name {
			return "it does not return what the call returned"
		}
	}
	return ""
}
