package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kern/cli"
	"kern/components/clock"
	"kern/components/files"
	"kern/components/github"
	"kern/components/session"
	"kern/forbid"
	"kern/index"
)

// The clock and the GitHub component declare their forbid lists, and kern's
// own components use no entry of another's: the host's clock (host.Wall) is
// the clock's only other reader of the system time. A change that breaks it
// fails go test ./..., and so the landing's suite and CI's check.
func TestKernsComponentsUseNothingAnotherIOModuleForbids(t *testing.T) {
	t.Parallel()
	for _, c := range []*index.Component{clock.Component.Component, github.Component.Component, files.Component.Component} {
		if c.Forbids().Empty() {
			t.Errorf("the I/O component %s declares no forbid list", c.Name())
		}
	}
	var out, errs strings.Builder
	code, err := cli.Forbids(filepath.Join("..", ".."), index.Registered(), &out, &errs)
	if err != nil || code != 0 {
		t.Fatalf("kern forbids answers %d, %v:\n%s", code, err, errs.String())
	}
	if !strings.Contains(out.String(), "the lists of clock, files, github") {
		t.Fatalf("kern forbids held kern's components to no list of clock, files and github: %s", out.String())
	}
}

// A fixture component that calls time.Now is refused, naming the clock as the
// owner, and one that runs gh is refused, naming GitHub, by the lists kern's
// binary carries.
func TestAComponentThatReadsTheTimeOrRunsGhIsRefused(t *testing.T) {
	t.Parallel()
	var out, errs strings.Builder
	code, err := cli.Forbids(filepath.Join("..", "..", "forbid", "testdata", "checkout"), index.Registered(), &out, &errs)
	if err != nil || code != 1 {
		t.Fatalf("kern forbids answers %d, %v, and wants 1", code, err)
	}
	for _, want := range []string{
		"components/poller/poller.go:13: components/poller uses the call time.Now, which the I/O module clock forbids: only clock may use it, so reach it through clock's names and verbs",
		"components/runner/runner.go:15: components/runner uses the command gh, which the I/O module github forbids: only github may use it, so reach it through github's names and verbs",
		"components/pyrunner/__init__.py:15: components/pyrunner uses the command gh, which the I/O module github forbids",
		"components/pyrunner/__init__.py:13: components/pyrunner uses the call time.sleep, which the I/O module clock forbids",
		// an entry two lists hold, the clock's and the declared pyclock's, names both
		"components/pyrunner/__init__.py:20: components/pyrunner uses the call time.monotonic, which the I/O modules clock and pyclock forbid: only clock and pyclock may use it, so reach it through clock's or pyclock's names and verbs",
	} {
		if !strings.Contains(errs.String(), want) {
			t.Errorf("kern forbids says\n%s\nand names no %q", errs.String(), want)
		}
	}
}

// The files module forbids direct file writes on the folders it manages: the
// session record's writes under .se/.runtime/sessions pass, as it declares
// that folder, and are refused once a wiring manages it.
func TestTheSessionRecordWritesPastTheFilesModuleOnlyOnItsOwnFolder(t *testing.T) {
	t.Parallel()
	if files.Component.Component.Forbids().On != "folders" || session.Component.Component.Writes()[0] != session.Dir {
		t.Fatalf("files forbids on %q and the session record writes %v", files.Component.Component.Forbids().On, session.Component.Component.Writes())
	}
	root := filepath.Join("..", "..")
	w, err := index.ParseWiring([]byte("[instances.files]\ncomponent = \"files\"\nconfig = { folders = [\".se/.runtime\"] }\n"))
	if err != nil {
		t.Fatal(err)
	}
	owners, err := forbid.Load(root, index.Registered(), w)
	if err != nil {
		t.Fatal(err)
	}
	found, err := forbid.Scan(os.DirFS(root), owners)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 {
		t.Fatal("a wiring that manages .se/.runtime leaves the session record's writes passing")
	}
	for _, f := range found {
		if f.File != "components/session/session.go" || !strings.Contains(f.String(), "session writes .se/.runtime/sessions, which files manages") {
			t.Errorf("the scan finds %s, and wants only the session record's writes, named as a managed folder's", f)
		}
	}
}
