package files_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kern/components/files"
	"kern/filled"
	"kern/index"
)

// gate is a fake of an application's template component: its verb accept
// claims one path for the template note, takes there only a value that its
// fill made (marked by its seal), and answers every other path free, as SE's
// template component's verb accept does (SE#1683).
type gate struct {
	mu    sync.Mutex
	asked []filled.Request
}

const claimed = "docs/claimed.md"

func (g *gate) accept(_ context.Context, r filled.Request) (filled.Accepted, error) {
	g.mu.Lock()
	g.asked = append(g.asked, r)
	g.mu.Unlock()
	if r.Path != claimed {
		return filled.Accepted{Free: true}, nil
	}
	v, _ := r.Filled.(map[string]any)
	if v == nil || v["seal"] != "made by fill" || v["template"] != "note" {
		return filled.Accepted{}, errors.New(r.Target + " of " + r.Path + " takes a filled template of note, not free text: fill it with the template component's fill (verb fill of the template component with template note)")
	}
	return filled.Accepted{Template: "note", Version: "1.0.0", Document: v["document"].(string)}, nil
}

func (g *gate) requests() []filled.Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]filled.Request{}, g.asked...)
}

// gated starts the files component on a real index beside the fake template
// component, its port template-accept wired to the fake's verb accept.
func gated(t *testing.T, root string) (*gate, *index.Index) {
	t.Helper()
	g := &gate{}
	tmpl := index.Declare("template", "A fake template component: its verb accept claims docs/claimed.md for the template note.", index.IO(),
		index.JSONVerb("accept", "Accepts a filled template of the template that claims the path; any other path is free.", filled.RequestShape, filled.AnswerShape, g.accept))
	f := files.New(files.Options{Root: root, Clock: clock{make(chan time.Time)}, Watch: func() (files.Watcher, error) { return newFed(), nil }})
	w := index.Wiring{
		Instances: []index.Instance{
			{Name: "files", Component: files.Name, Enabled: true, Line: 1, Config: map[string]index.ConfigValue{"folders": {Value: anyOf([]string{"docs"}), Line: 1}}},
			{Name: "template", Component: "template", Enabled: true, Line: 2, Config: map[string]index.ConfigValue{}},
		},
		Wires: []index.Wire{{Port: "files." + filled.Port, To: "template.accept", Line: 3}},
	}
	ix, err := index.Start(w, f.Component, tmpl)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Stop)
	return g, ix
}

func docsFolder(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// A free-text write to a path a template claims is refused, naming the
// template, before anything is written; a filled template is written, its
// document as the gate answered it; and the gate is asked with the path the
// write names, so the template's claimed paths reach the module.
func TestAFreeTextWriteToAClaimedPathIsRefusedAndAFilledTemplateIsWritten(t *testing.T) {
	t.Parallel()
	root := docsFolder(t)
	g, ix := gated(t, root)
	abs := filepath.Join(root, filepath.FromSlash(claimed))

	_, err := ix.Call("files write", "--path", claimed, "--version", "", "--content", "# Note\nfree text\n")
	if err == nil || !strings.Contains(err.Error(), "filled template of note") || !strings.Contains(err.Error(), "fill") {
		t.Fatalf("a free-text write to %s answers %v, and wants it refused naming the template note and its fill", claimed, err)
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Fatalf("after the refused write %s stands: %v", claimed, err)
	}
	asked := g.requests()
	if len(asked) != 1 || asked[0].Target != "files write" || asked[0].Path != claimed || asked[0].Filled != "# Note\nfree text\n" {
		t.Fatalf("the gate was asked %+v, and wants one request of files write for %s with the text as it stands", asked, claimed)
	}

	forged, _ := json.Marshal(map[string]any{"template": "note", "document": "# Note\nforged\n", "seal": "written by hand"})
	if _, err := ix.Call("files write", "--path", claimed, "--version", "", "--filled", string(forged)); err == nil || !strings.Contains(err.Error(), "note") {
		t.Fatalf("a hand-made value of a filled template's shape answers %v, and wants it refused naming note", err)
	}
	if _, err := ix.Call("files write", "--path", claimed, "--version", "", "--filled", "not json"); err == nil || !strings.Contains(err.Error(), "--filled takes the JSON") {
		t.Fatalf("--filled that is no JSON answers %v", err)
	}

	made, _ := json.Marshal(map[string]any{"template": "note", "version": "1.0.0", "document": "# Note\nfilled\n", "seal": "made by fill"})
	r, err := ix.Call("files write", "--path", claimed, "--version", "", "--filled", string(made))
	if err != nil || !strings.Contains(r.Text, "wrote "+claimed) {
		t.Fatalf("a filled template's write answers %q, %v", r.Text, err)
	}
	if data, _ := os.ReadFile(abs); string(data) != "# Note\nfilled\n" {
		t.Fatalf("after the filled template's write the disk holds %q, and wants its document", data)
	}

	r, _ = ix.Call("files read", "--path", claimed)
	version := strings.Fields(r.Text)[3]
	if _, err := ix.Call("files edit", "--path", claimed, "--version", version, "--old", "filled", "--new", "edited"); err == nil || !strings.Contains(err.Error(), "filled template of note") {
		t.Fatalf("a free edit of %s answers %v, and wants it refused naming note", claimed, err)
	}
	if data, _ := os.ReadFile(abs); string(data) != "# Note\nfilled\n" {
		t.Fatalf("after the refused edit the disk holds %q", data)
	}
	if _, err := ix.Call("files write", "--path", claimed, "--version", version, "--content", "x", "--filled", string(made)); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("--content with --filled answers %v", err)
	}
}
