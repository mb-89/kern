package kerntest_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// No test waits on the wall clock (README.md "Tests"): a time limit under
// test is shortened or faked, and a test waits on an event, with a deadline
// only as the bound of a wait that fails, kern/waiting's. The check refuses,
// in every _test.go of kern, a time.Sleep, a time.After, a time.NewTimer and
// a deadline from time.Now (time.Now().Add), under any import name of
// package time; kern/waiting, no test file, is their one home.

// waitFuncs are the functions of package time that wait on the wall clock.
var waitFuncs = map[string]bool{"Sleep": true, "After": true, "NewTimer": true}

// waits answers each wall-clock wait in a _test.go of fsys, as
// <file>:<line>: <what>, sorted. It skips the folders .se, .git and testdata.
func waits(fsys fs.FS) ([]string, error) {
	var found []string
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".se", ".git", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		names := map[string]bool{}
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == "time" {
				name := "time"
				if imp.Name != nil {
					name = imp.Name.Name
				}
				names[name] = true
			}
		}
		// ofTime says whether e names the function fn of package time.
		ofTime := func(e ast.Expr, fn func(string) bool) (string, bool) {
			sel, ok := e.(*ast.SelectorExpr)
			if !ok || !fn(sel.Sel.Name) {
				return "", false
			}
			x, ok := sel.X.(*ast.Ident)
			return sel.Sel.Name, ok && names[x.Name]
		}
		at := func(n ast.Node, what string) {
			found = append(found, path+":"+strconv.Itoa(fset.Position(n.Pos()).Line)+": "+what)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if name, ok := ofTime(sel, func(s string) bool { return waitFuncs[s] }); ok {
				at(sel, "time."+name)
				return true
			}
			// A deadline: time.Now().Add(...).
			if call, ok := sel.X.(*ast.CallExpr); ok && sel.Sel.Name == "Add" {
				if _, ok := ofTime(call.Fun, func(s string) bool { return s == "Now" }); ok {
					at(sel, "a deadline from time.Now")
				}
			}
			return true
		})
		return nil
	})
	sort.Strings(found)
	return found, err
}

// No test of kern waits on the wall clock.
func TestNoTestWaitsOnTheWallClock(t *testing.T) {
	found, err := waits(os.DirFS(filepath.Join("..")))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Fatalf("a test waits on the wall clock (README.md \"Tests\"): wait on an event, with waiting.Fails or waiting.Until as its failure bound, and shorten or fake a time limit under test:\n  %s", strings.Join(found, "\n  "))
	}
}

// The check fails on a time.Sleep, a time.After, a time.NewTimer and a
// deadline from time.Now in a test, under any import name of package time,
// and passes the same in code that is no test, a method of another value
// named so, a time.Now that is no deadline, a file's text and testdata.
func TestTheWaitCheckRefusesASleepInATest(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"host/sleeps_test.go":    {Data: []byte("package host\n\nimport \"time\"\n\nfunc wait() {\n\ttime.Sleep(time.Millisecond)\n}\n")},
		"mcp/after_test.go":      {Data: []byte("package mcp\n\nimport clock \"time\"\n\nvar _ = clock.After(clock.Second)\n\nvar _ = clock.NewTimer\n")},
		"index/deadline_test.go": {Data: []byte("package index\n\nimport \"time\"\n\nvar end = time.Now().Add(time.Second)\n\nvar mark = time.Now()\n")},
		"index/sleeps.go":        {Data: []byte("package index\n\nimport \"time\"\n\nfunc wait() { time.Sleep(time.Second) }\n")},
		"bus/other_test.go":      {Data: []byte("package bus\n\ntype c struct{}\n\nfunc (c) Sleep() {}\n\nvar time c\n\nvar _ = time.Sleep\n\nconst doc = \"time.Sleep(1)\"\n")},
		"git/testdata/x_test.go": {Data: []byte("package x\n\nimport \"time\"\n\nvar _ = time.Sleep\n")},
		"template/cmd/a_test.go": {Data: []byte("package main\n\nimport \"time\"\n\nfunc f() { <-time.After(time.Second) }\n")},
	}
	found, err := waits(fsys)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"host/sleeps_test.go:6: time.Sleep",
		"index/deadline_test.go:5: a deadline from time.Now",
		"mcp/after_test.go:5: time.After",
		"mcp/after_test.go:7: time.NewTimer",
		"template/cmd/a_test.go:5: time.After",
	}
	if strings.Join(found, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the check finds\n  %s\nand wants\n  %s", strings.Join(found, "\n  "), strings.Join(want, "\n  "))
	}
}
