package forbid_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"kern/forbid"
	"kern/index"
)

// The owners of testdata/checkout: a clock and a GitHub component at their
// folders, as Load answers a binary's components, and the declared pyclock.
var testOwners = []forbid.Owner{
	{Module: "clock", Home: "components/clock", List: index.Forbidden{
		Calls: []string{"time.Now", "time.Sleep", "time.sleep", "datetime.now"}}},
	{Module: "github", Home: "components/github", List: index.Forbidden{
		Commands: []string{"gh"}, Hosts: []string{"api.github.com"}, Imports: []string{"github.com/google/go-github"}}},
	{Module: "elsewhere", List: index.Forbidden{Calls: []string{"time.Since"}}},
}

func lines(found []forbid.Finding) []string {
	var out []string
	for _, f := range found {
		out = append(out, f.String())
	}
	return out
}

// The scan refuses each use of another I/O module's entry under components/,
// in Go and in Python, naming the file, the line, the component's folder and
// the owner; it passes an owner's own code, tests, test data, comments,
// docstrings and a string that only holds an entry's name.
func TestTheScanRefusesWhatAnotherIOModuleForbids(t *testing.T) {
	t.Parallel()
	fsys := os.DirFS(filepath.Join("testdata", "checkout"))
	declared, err := forbid.Declared(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(declared) != 1 || declared[0].Module != "pyclock" || declared[0].Home != "components/pyclock" {
		t.Fatalf("the declared owners are %+v, and want pyclock at components/pyclock", declared)
	}
	found, err := forbid.Scan(fsys, append(testOwners, declared...))
	if err != nil {
		t.Fatal(err)
	}
	const tail = " may use it, so reach it through "
	want := []string{
		"components/github/github.go:11: components/github uses the call time.Now, which the I/O module clock forbids: only clock" + tail + "clock's names and verbs",
		"components/hosts/hosts.go:6: components/hosts uses the call time.Since, which the I/O module elsewhere forbids: only elsewhere" + tail + "elsewhere's names and verbs",
		"components/poller/poller.go:13: components/poller uses the call time.Now, which the I/O module clock forbids: only clock" + tail + "clock's names and verbs",
		"components/pyrunner/__init__.py:13: components/pyrunner uses the call time.sleep, which the I/O module clock forbids: only clock" + tail + "clock's names and verbs",
		"components/pyrunner/__init__.py:14: components/pyrunner uses the call datetime.now, which the I/O module clock forbids: only clock" + tail + "clock's names and verbs",
		"components/pyrunner/__init__.py:15: components/pyrunner uses the command gh, which the I/O module github forbids: only github" + tail + "github's names and verbs",
		"components/pyrunner/__init__.py:20: components/pyrunner uses the call time.monotonic, which the I/O module pyclock forbids: only pyclock" + tail + "pyclock's names and verbs",
		"components/runner/runner.go:7: components/runner uses the import github.com/google/go-github, which the I/O module github forbids: only github" + tail + "github's names and verbs",
		"components/runner/runner.go:12: components/runner uses the host api.github.com, which the I/O module github forbids: only github" + tail + "github's names and verbs",
		"components/runner/runner.go:15: components/runner uses the command gh, which the I/O module github forbids: only github" + tail + "github's names and verbs",
		"components/runner/runner.go:18: components/runner uses the command gh, which the I/O module github forbids: only github" + tail + "github's names and verbs",
	}
	if got := lines(found); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the scan finds\n  %s\nand wants\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// A component.toml that declares a forbid list without the I/O flag is
// refused by the scan's load, naming its file; a Go file that does not parse
// is refused, naming it; a checkout without components/ holds nothing to scan.
func TestTheScanRefusesWhatItCannotRead(t *testing.T) {
	t.Parallel()
	_, err := forbid.Declared(fstest.MapFS{"components/x/component.toml": {Data: []byte("name = \"x\"\ncontract = 1\nhelp = \"h\"\n[forbids]\ncommands = [\"gh\"]\n")}})
	if err == nil || !strings.Contains(err.Error(), "components/x/component.toml") || !strings.Contains(err.Error(), "carries no I/O flag") {
		t.Fatalf("the load answers %v, and wants the file and the missing I/O flag", err)
	}
	_, err = forbid.Scan(fstest.MapFS{"components/x/x.go": {Data: []byte("package x\nfunc {")}}, testOwners)
	if err == nil || !strings.Contains(err.Error(), "components/x/x.go does not parse") {
		t.Fatalf("the scan answers %v, and wants the file that does not parse", err)
	}
	found, err := forbid.Scan(fstest.MapFS{"index/x.go": {Data: []byte("package x\nimport \"time\"\nvar _ = time.Now\n")}}, testOwners)
	if err != nil || len(found) > 0 {
		t.Fatalf("a checkout without components/ answers %v, %v", found, err)
	}
}

// A binary's I/O component owns the folder of the package that declared it
// where that package lies in the checkout's module, and no folder elsewhere;
// one without a forbid list, or without the I/O flag, owns nothing.
func TestACompiledOwnerLivesInItsPackagesFolder(t *testing.T) {
	t.Parallel()
	list := index.Forbidden{Commands: []string{"gh"}}
	door := index.Declare("door", "Reaches the outside.", index.IO(), index.Forbids(list))
	plain := index.Declare("plain", "Reaches the outside without a list.", index.IO())
	got := forbid.Compiled("kern", []*index.Component{door, plain})
	if len(got) != 1 || got[0].Module != "door" || got[0].Home != "forbid" {
		t.Fatalf("in kern the owners are %+v, and want door at forbid", got)
	}
	if got := forbid.Compiled("app", []*index.Component{door}); len(got) != 1 || got[0].Home != "" {
		t.Fatalf("in another module the owners are %+v, and want door with no home", got)
	}
}

// A list forbidden only on the folders an owner's config names passes a use
// in a module that declares the folders it writes, none of them managed, and
// refuses one that declares none or writes a managed folder, naming why; the
// managed folders are what the wiring sets for the key.
func TestAListOnManagedFoldersPassesAWriterOfItsOwnFolder(t *testing.T) {
	t.Parallel()
	w, err := index.ParseWiring([]byte("[instances.files]\ncomponent = \"files\"\nconfig = { folders = [\"docs\", \"notes/\"] }\n"))
	if err != nil {
		t.Fatal(err)
	}
	owners := forbid.Manage([]forbid.Owner{
		{Module: "files", Home: "components/files", List: index.Forbidden{Calls: []string{"os.WriteFile"}, On: "folders"}},
		{Module: "record", Home: "components/record", Writes: []string{".se/record"}},
		{Module: "notes", Home: "components/notes", Writes: []string{"notes/inbox"}},
	}, w)
	if got := owners[0].Managed; strings.Join(got, ",") != "docs,notes" {
		t.Fatalf("files manages %v, and wants docs and notes", got)
	}
	write := []byte("package x\n\nimport \"os\"\n\nvar _ = os.WriteFile\n")
	fsys := fstest.MapFS{
		"components/files/files.go":   {Data: write},
		"components/record/record.go": {Data: write},
		"components/notes/notes.go":   {Data: write},
		"components/loose/loose.go":   {Data: write},
	}
	found, err := forbid.Scan(fsys, owners)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"components/loose/loose.go:5: components/loose uses the call os.WriteFile, which the I/O module files forbids on the folders it manages: components/loose declares no folders it writes, so it may write one files manages (its config folders: docs, notes); declare them (index.Writes, or writes in component.toml), or write through files's verbs",
		"components/notes/notes.go:5: components/notes uses the call os.WriteFile, which the I/O module files forbids on the folders it manages: notes writes notes/inbox, which files manages (its config folders: docs, notes); write through files's verbs",
	}
	if got := lines(found); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the scan finds\n  %s\nand wants\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}
