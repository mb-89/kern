// Package forbid is the scan that holds every I/O module to its forbid list
// (index.Forbids): what an I/O component replaces, the calls, imports,
// commands and hosts only its own code may use. The scan reads the code of the
// checkout's components, the Go and Python files under components/ but their
// tests, and refuses each use of an entry of another I/O module's list, naming
// the file, the line, the component's folder and the module that owns it. It
// runs before code is committed, in the landing's suite and in CI, never at
// the core's start (README.md "Forbid lists").
package forbid

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"kern/index"
)

// Dir is the folder of the checkout the scan reads: where an application
// keeps its components, Go packages and declared ones alike.
const Dir = "components"

// Owner is one I/O module's forbid list and the folder its own code lies in,
// which may use the list's entries.
type Owner struct {
	// Module is the I/O component's name.
	Module string
	// Home is its folder relative to the checkout, slash-separated; empty
	// where its code lies outside the checkout, such as a kern component an
	// application's binary carries.
	Home string
	// List is its forbid list.
	List index.Forbidden
}

// owns says whether the file at p, slash-separated under the checkout, is the
// owner's own code.
func (o Owner) owns(p string) bool {
	return o.Home != "" && strings.HasPrefix(p, o.Home+"/")
}

// Finding is one use of a forbidden entry by a component that does not own it.
type Finding struct {
	// File is slash-separated, relative to the checkout; Line counts from 1.
	File string
	Line int
	// Folder is the component's folder that holds the file.
	Folder string
	// Kind is call, import, command or host, and Entry the entry used.
	Kind, Entry string
	// Owners are the I/O modules whose lists hold the entry.
	Owners []string
}

func (f Finding) String() string {
	owners := strings.Join(f.Owners, " and ")
	module, forbids := "module "+owners, "forbids"
	if len(f.Owners) > 1 {
		module, forbids = "modules "+owners, "forbid"
	}
	return fmt.Sprintf("%s:%d: %s uses the %s %s, which the I/O %s %s: only %s may use it, so reach it through %s",
		f.File, f.Line, f.Folder, f.Kind, f.Entry, module, forbids, owners, reach(f.Owners))
}

// reach names the owners' names and verbs: "clock's", "clock's or pyclock's".
func reach(owners []string) string {
	var each []string
	for _, o := range owners {
		each = append(each, o+"'s")
	}
	return strings.Join(each, " or ") + " names and verbs"
}

// Load answers the owners of the forbid lists the checkout at root loads: each
// I/O component of compiled, the components a binary carries, with its home
// where its package lies in the checkout's Go module (go.mod at root), and
// each I/O component declared by a component.toml under components/, its
// folder its home. A component.toml the core would refuse is refused, naming
// its file.
func Load(root string, compiled []*index.Component) ([]Owner, error) {
	module, err := modulePath(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, err
	}
	owners := Compiled(module, compiled)
	declared, err := Declared(os.DirFS(root))
	if err != nil {
		return nil, err
	}
	return append(owners, declared...), nil
}

// Compiled answers the owners among compiled, each I/O component with a
// forbid list: its home is the folder of its package (index.Component.Package)
// relative to module, the checkout's Go module path, or none where its package
// lies outside it.
func Compiled(module string, compiled []*index.Component) []Owner {
	var out []Owner
	for _, c := range compiled {
		if !c.IO() || c.Forbids().Empty() {
			continue
		}
		home := ""
		// An external test package, kern/forbid_test, lies in its package's folder.
		if p := strings.TrimSuffix(c.Package(), "_test"); module != "" && strings.HasPrefix(p, module+"/") {
			home = strings.TrimPrefix(p, module+"/")
		}
		out = append(out, Owner{Module: c.Name(), Home: home, List: c.Forbids()})
	}
	return out
}

// Declared answers the owners declared under components/ of fsys: each
// component.toml of an I/O component with a forbid list, its folder its home.
func Declared(fsys fs.FS) ([]Owner, error) {
	var out []Owner
	err := fs.WalkDir(fsys, Dir, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && p == Dir {
			return fs.SkipAll
		}
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != Dir && skippedDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != index.DeclarationFile {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		c, err := index.ParseDeclaration(data)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if c.IO() && !c.Forbids().Empty() {
			out = append(out, Owner{Module: c.Name(), Home: path.Dir(p), List: c.Forbids()})
		}
		return nil
	})
	return out, err
}

// modulePath reads the module line of a go.mod, or none where there is no go.mod.
func modulePath(file string) (string, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`), nil
		}
	}
	return "", nil
}

// skippedDir says whether the scan leaves a folder out: test data, tests,
// caches, and hidden folders such as .git and .se.
func skippedDir(name string) bool {
	switch name {
	case "testdata", "tests", "test", "__pycache__", "node_modules":
		return true
	}
	return strings.HasPrefix(name, ".")
}

// testFile says whether a file is a test, which the scan leaves out: a
// component's tests run on fakes, and the README's "Tests" holds them to no
// wall clock.
func testFile(name string) bool {
	return strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_test.py") ||
		(strings.HasPrefix(name, "test_") && strings.HasSuffix(name, ".py")) || name == "conftest.py"
}

// use is one place a file uses something a forbid list may name.
type use struct {
	line        int
	kind, entry string
}

// Scan reads the Go and Python files under components/ of fsys, but tests and
// test data, and answers each use of an entry of an owner's list in a file no
// owner of that entry owns, sorted by file and line. A Go file that does not
// parse is refused, naming it.
func Scan(fsys fs.FS, owners []Owner) ([]Finding, error) {
	var found []Finding
	err := fs.WalkDir(fsys, Dir, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && p == Dir {
			return fs.SkipAll
		}
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != Dir && skippedDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if testFile(d.Name()) {
			return nil
		}
		var scan func(string, []byte, []Owner) ([]use, error)
		switch path.Ext(p) {
		case ".go":
			scan = goUses
		case ".py":
			scan = pyUses
		default:
			return nil
		}
		var others []Owner
		for _, o := range owners {
			if !o.owns(p) {
				others = append(others, o)
			}
		}
		if len(others) == 0 {
			return nil
		}
		src, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		uses, err := scan(p, src, others)
		if err != nil {
			return err
		}
		folder := componentFolder(fsys, p)
		for _, u := range uses {
			if names := forbidders(owners, p, u); len(names) > 0 {
				found = append(found, Finding{File: p, Line: u.line, Folder: folder, Kind: u.kind, Entry: u.entry, Owners: names})
			}
		}
		return nil
	})
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].File != found[j].File {
			return found[i].File < found[j].File
		}
		return found[i].Line < found[j].Line
	})
	return found, err
}

// forbidders answers the owners whose lists hold the use's entry, sorted, or
// none where an owner of it owns the file.
func forbidders(owners []Owner, p string, u use) []string {
	var names []string
	for _, o := range owners {
		for _, k := range o.List.Kinds() {
			if strings.TrimSuffix(k.Key, "s") != u.kind {
				continue
			}
			for _, e := range k.Entries {
				if e != u.entry {
					continue
				}
				if o.owns(p) {
					return nil
				}
				names = append(names, o.Module)
			}
		}
	}
	sort.Strings(names)
	return names
}

// componentFolder answers the folder of the component a file belongs to: the
// nearest folder up from it that holds a component.toml, else its own folder.
func componentFolder(fsys fs.FS, p string) string {
	dir := path.Dir(p)
	for d := dir; d != "." && d != Dir; d = path.Dir(d) {
		if _, err := fs.Stat(fsys, path.Join(d, index.DeclarationFile)); err == nil {
			return d
		}
	}
	return dir
}

// entries are the owners' entries of one kind, by entry.
func entries(owners []Owner, key string) []string {
	seen := map[string]bool{}
	var out []string
	for _, o := range owners {
		for _, k := range o.List.Kinds() {
			if k.Key != key {
				continue
			}
			for _, e := range k.Entries {
				if !seen[e] {
					seen[e] = true
					out = append(out, e)
				}
			}
		}
	}
	return out
}

// stringUses answers the commands and hosts a string literal names: a command
// where the string is the command's name or a command line that starts with
// it, a host where the host stands in it between characters no host name holds.
func stringUses(line int, s string, owners []Owner) []use {
	var out []use
	for _, c := range entries(owners, "commands") {
		if s == c || strings.HasPrefix(s, c+" ") {
			out = append(out, use{line, "command", c})
		}
	}
	for _, h := range entries(owners, "hosts") {
		if namesHost(s, h) {
			out = append(out, use{line, "host", h})
		}
	}
	return out
}

func namesHost(s, host string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], host)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(host)
		if (start == 0 || !hostByte(s[start-1])) && (end == len(s) || !hostByte(s[end])) {
			return true
		}
		i = start + 1
	}
}

func hostByte(b byte) bool {
	return b == '.' || b == '-' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// importUses answers the use of an import entry by a package or module
// imported: the entry itself, or one below it, sep the separator of its
// path's parts ("/" in Go, "." in Python).
func importUses(line int, imported, sep string, owners []Owner) []use {
	var out []use
	for _, e := range entries(owners, "imports") {
		if imported == e || strings.HasPrefix(imported, e+sep) {
			out = append(out, use{line, "import", e})
		}
	}
	return out
}
