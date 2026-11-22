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

// roomHandlers are the functions a callback defers to catch a panic. Each
// makes the room last, so a callback that defers one of them as its first
// statement, and nothing else, makes the room after all it does.
var roomHandlers = map[string]bool{"failWith": true, "failQuiet": true, "failAndGoOn": true}

// TestCallbacksMakeRoom: every Go function the generated code calls makes
// the room for a fault on windows (makeRoom) as the last thing it does
// before it returns into the engine. On windows the stack may shrink while
// a callback runs, and the generated code checks the room only where a
// function starts. A callback either defers one of roomHandlers as its
// first statement and defers nothing else, or defers nothing and calls
// makeRoom right before its only return, which computes nothing. A callback
// that never returns ends with a panic.
func TestCallbacksMakeRoom(t *testing.T) {
	called, texts := map[string]bool{}, map[string]bool{}
	files, err := filepath.Glob("*_amd64.s")
	if err != nil {
		t.Fatal(err)
	}
	call := regexp.MustCompile(`CALL ·([A-Za-z0-9_]+)\(SB\)`)
	text := regexp.MustCompile(`(?m)^TEXT ·([A-Za-z0-9_]+)\(SB\)`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range call.FindAllSubmatch(b, -1) {
			called[string(m[1])] = true
		}
		for _, m := range text.FindAllSubmatch(b, -1) {
			texts[string(m[1])] = true
		}
	}
	for name := range texts {
		delete(called, name)
	}
	if !called["host_poll"] || !called["malloc"] || !called["host_write"] {
		t.Fatalf("the generated code calls %v: no host_poll, malloc or host_write", called)
	}
	gofiles, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	defined := map[string]*ast.FuncDecl{}
	fset := token.NewFileSet()
	for _, gf := range gofiles {
		if strings.HasSuffix(gf, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, gf, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Body != nil {
				defined[fd.Name.Name] = fd
			}
		}
	}
	for name := range roomHandlers {
		fd := defined[name]
		if fd == nil {
			t.Fatalf("no %s", name)
		}
		if returns(fd.Body) > 0 || !isMakeRoom(fd.Body.List[len(fd.Body.List)-1]) {
			t.Errorf("%s does not end with makeRoom(), or returns before it", name)
		}
	}
	for name := range called {
		fd := defined[name]
		if fd == nil {
			t.Errorf("the generated code calls %s, which the package does not define in Go", name)
			continue
		}
		if why := makesRoomLast(fd); why != "" {
			t.Errorf("%s: %s", name, why)
		}
	}
}

// makesRoomLast says what is wrong with the callback fd, or "".
func makesRoomLast(fd *ast.FuncDecl) string {
	list := fd.Body.List
	if len(list) == 0 {
		return "it does not make the room"
	}
	defers := 0
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.DeferStmt:
			defers++
		}
		return true
	})
	if d, ok := list[0].(*ast.DeferStmt); ok {
		id, ok := d.Call.Fun.(*ast.Ident)
		if !ok || !roomHandlers[id.Name] {
			return "its first statement defers a function that does not make the room"
		}
		if defers != 1 {
			return "it defers more than its handler, which would run after the handler made the room"
		}
		return ""
	}
	if defers != 0 {
		return "it defers something, and not one of the handlers as its first statement"
	}
	if e, ok := list[len(list)-1].(*ast.ExprStmt); ok {
		if c, ok := e.X.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "panic" && returns(fd.Body) == 0 {
				return ""
			}
		}
	}
	n := returns(fd.Body)
	if r, ok := list[len(list)-1].(*ast.ReturnStmt); ok {
		for _, e := range r.Results {
			if _, ok := e.(*ast.Ident); !ok {
				return "its return computes a value after makeRoom"
			}
		}
		list = list[:len(list)-1]
		n--
	}
	if n != 0 {
		return "it returns before it makes the room"
	}
	if len(list) == 0 || !isMakeRoom(list[len(list)-1]) {
		return "its last statement before the return is not makeRoom()"
	}
	return ""
}

// returns counts the return statements of body, outside function literals.
func returns(body *ast.BlockStmt) int {
	n := 0
	ast.Inspect(body, func(x ast.Node) bool {
		switch x.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			n++
		}
		return true
	})
	return n
}

// isMakeRoom says s is the statement makeRoom().
func isMakeRoom(s ast.Stmt) bool {
	e, ok := s.(*ast.ExprStmt)
	if !ok {
		return false
	}
	c, ok := e.X.(*ast.CallExpr)
	if !ok || len(c.Args) != 0 {
		return false
	}
	id, ok := c.Fun.(*ast.Ident)
	return ok && id.Name == "makeRoom"
}
