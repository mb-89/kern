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

// Time is an I/O module (components/clock/README.md): no component keeps a
// timer of its own. The check refuses a time.Tick, a time.NewTicker or a
// time.AfterFunc in any Go file of kern, its tests among them, outside the
// clock component and the host's clock: a component that acts on time reads
// the clock's tick or second (index.Reacts) and keeps its interval with a
// clock.Relay, and a wait for an answer waits on host.Clock.

// timerFuncs are the functions of package time that start a timer of their own.
var timerFuncs = map[string]bool{"Tick": true, "NewTicker": true, "AfterFunc": true}

// timerHomes are the places that may start one: the clock component's
// folder, and the host's clock.
var timerHomes = []string{"components/clock/", "host/clock.go"}

// timers answers each call of a timer function of package time in a Go file
// of fsys outside timerHomes, as <file>:<line>: time.<func>, sorted. It reads
// package time under any import name, and skips the folders .se, .git and
// testdata.
func timers(fsys fs.FS) ([]string, error) {
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
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		for _, home := range timerHomes {
			if path == home || (strings.HasSuffix(home, "/") && strings.HasPrefix(path, home)) {
				return nil
			}
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
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || !timerFuncs[sel.Sel.Name] {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && names[x.Name] {
				found = append(found, path+":"+strconv.Itoa(fset.Position(sel.Pos()).Line)+": time."+sel.Sel.Name)
			}
			return true
		})
		return nil
	})
	sort.Strings(found)
	return found, err
}

// kern's own tree starts no timer outside the clock component and the host's clock.
func TestNoTimerOutsideTheClock(t *testing.T) {
	found, err := timers(os.DirFS(filepath.Join("..")))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Fatalf("a timer of its own outside the clock (components/clock/README.md): read the clock's tick or second with index.Reacts and a clock.Relay, or wait on host.Clock:\n  %s", strings.Join(found, "\n  "))
	}
}

// The check fails on a new ticker in a component, under any import name of
// package time and in a test, and passes the clock component's own and the
// host's clock, a method of another package named so, and a file's text.
func TestTheTimerCheckFailsOnANewTickerInAComponent(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"components/poller/poller.go":      {Data: []byte("package poller\n\nimport \"time\"\n\nfunc run(fn func()) {\n\tgo func() {\n\t\tfor range time.NewTicker(time.Second).C {\n\t\t\tfn()\n\t\t}\n\t}()\n}\n")},
		"components/poller/poller_test.go": {Data: []byte("package poller\n\nimport clock \"time\"\n\nvar _ = clock.AfterFunc\n")},
		"mcp/tick.go":                      {Data: []byte("package mcp\n\nimport \"time\"\n\nvar _ = time.Tick\n")},
		"components/clock/loop.go":         {Data: []byte("package clock\n\nimport \"time\"\n\nvar _ = time.NewTicker\n")},
		"host/clock.go":                    {Data: []byte("package host\n\nimport \"time\"\n\nvar _ = time.AfterFunc\n")},
		"index/other.go":                   {Data: []byte("package index\n\ntype t struct{}\n\nfunc (t) Tick() {}\n\nvar time t\n\nvar _ = time.Tick\n\nconst doc = \"time.NewTicker\"\n")},
		"components/clock/testdata/x.go":   {Data: []byte("package x\n")},
	}
	found, err := timers(fsys)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"components/poller/poller.go:7: time.NewTicker",
		"components/poller/poller_test.go:5: time.AfterFunc",
		"mcp/tick.go:5: time.Tick",
	}
	if strings.Join(found, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the check finds\n  %s\nand wants\n  %s", strings.Join(found, "\n  "), strings.Join(want, "\n  "))
	}
}
