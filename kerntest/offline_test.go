package kerntest_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"net"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"kern/offline"
)

// kern's suite runs with the network cut (README.md "Tests"): every folder
// of kern's module with a _test.go imports package offline in one of them,
// so its test binary cuts itself off the network at init.

// uncut answers each folder of fsys that holds a _test.go and none that
// imports kern/offline, sorted. It skips the folders .se, .git and testdata,
// and each folder with a go.mod of its own, another module.
func uncut(fsys fs.FS) ([]string, error) {
	tests, cut := map[string]bool{}, map[string]bool{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".se", ".git", "testdata":
				return fs.SkipDir
			}
			if _, err := fs.Stat(fsys, path.Join(p, "go.mod")); err == nil && p != "." {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		dir := path.Dir(p)
		tests[dir] = true
		src, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, src, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			if v, _ := strconv.Unquote(imp.Path.Value); v == "kern/offline" {
				cut[dir] = true
			}
		}
		return nil
	})
	var found []string
	for dir := range tests {
		if !cut[dir] {
			found = append(found, dir)
		}
	}
	sort.Strings(found)
	return found, err
}

// Every test binary of kern runs with the network cut.
func TestEveryTestBinaryRunsWithTheNetworkCut(t *testing.T) {
	found, err := uncut(os.DirFS(filepath.Join("..")))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Fatalf("a test package that does not cut the network (README.md \"Tests\"): add a test file offline_test.go with import _ \"kern/offline\" to\n  %s", strings.Join(found, "\n  "))
	}
}

// The check names a folder whose tests import no kern/offline, and passes one
// where one test file does, another module and testdata.
func TestTheNetworkCheckNamesAFolderWhoseTestsDoNotCut(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"go.mod":                       {Data: []byte("module kern\n")},
		"bus/bus_test.go":              {Data: []byte("package bus\n\nimport \"testing\"\n")},
		"host/host_test.go":            {Data: []byte("package host_test\n\nimport \"testing\"\n")},
		"host/offline_test.go":         {Data: []byte("package host_test\n\nimport _ \"kern/offline\"\n")},
		"template/go.mod":              {Data: []byte("module app\n")},
		"template/app_test.go":         {Data: []byte("package app\n")},
		"forbid/testdata/x/x_test.go":  {Data: []byte("package x\n")},
		"components/files/files.go":    {Data: []byte("package files\n")},
		"components/clock/one_test.go": {Data: []byte("package clock\n\nimport offline \"kern/offline\"\n\nvar _ = offline.Closed\n")},
	}
	found, err := uncut(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(found, ",") != "bus" {
		t.Fatalf("the check names %v, and wants bus alone", found)
	}
}

// In a cut test binary a host name outside the hosts file resolves to
// nothing, every proxy variable names the closed port, and no token is found.
// Loopback is dialled by its number, which needs no lookup.
func TestTheTestBinaryIsCutOffTheNetwork(t *testing.T) {
	if os.Getenv(offline.Hot) != "" {
		t.Skip("a hot test's run leaves the network whole")
	}
	if _, err := net.LookupHost("api.github.com"); err == nil || !strings.Contains(err.Error(), offline.Refusal.Error()) {
		t.Fatalf("api.github.com resolves: %v, and wants the cut's refusal", err)
	}
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY"} {
		if v := os.Getenv(k); v != offline.Closed {
			t.Fatalf("%s is %q, and wants %s", k, v, offline.Closed)
		}
	}
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN", "CLAUDE_CODE_REMOTE"} {
		if _, set := os.LookupEnv(k); set {
			t.Fatalf("%s is set in a cut binary", k)
		}
	}
}
