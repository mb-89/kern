// Package lsptest is the tests' road to the checker: Findings runs a
// checkout's files and lsp instances, as its wiring places them, on an index
// of their own, and calls `lsp lint`, so a test finds what the language
// server shows and `<name> lint` prints. An application's test holds its
// managed files to the checker with it (README.md "Language server").
package lsptest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"kern/components/files"
	"kern/components/lsp"
	"kern/index"
	"kern/lint"
)

// Findings answers every finding `lsp lint` answers in the checkout at
// root: its wiring.toml's instances of the files and the lsp components,
// with their config and wires, run with the model read from the disk. It
// writes nothing it leaves: each managed folder's start cookie is heard and
// removed before it returns.
func Findings(root string) ([]lint.Finding, error) {
	all, err := index.ReadWiring(filepath.Join(root, index.WiringFile))
	if err != nil {
		return nil, err
	}
	w := index.Wiring{}
	kept := map[string]bool{}
	var folders []string
	for _, in := range all.Instances {
		if (in.Component == files.Name || in.Component == lsp.Name) && in.Enabled {
			w.Instances = append(w.Instances, in)
			kept[in.Name] = true
			if v, ok := in.Config["folders"]; ok && in.Component == files.Name {
				list, _ := v.Value.([]any)
				for _, f := range list {
					folders = append(folders, fmt.Sprint(f))
				}
			}
		}
	}
	for _, wr := range all.Wires {
		if inst, _, ok := strings.Cut(wr.Port, "."); ok && kept[inst] {
			w.Wires = append(w.Wires, wr)
		}
	}
	quiet := &heard{events: make(chan string), errors: make(chan error)}
	fc := files.New(files.Options{Root: root, Watch: func() (files.Watcher, error) { return quiet, nil }})
	ix, err := index.Start(w, fc.Component, lsp.Component)
	if err != nil {
		return nil, fmt.Errorf("lsptest: the wiring of %s: %w", root, err)
	}
	defer fc.Stop()
	for _, folder := range folders {
		c := filepath.Join(root, filepath.FromSlash(folder), files.Own+"cookie")
		if _, err := os.Stat(c); err == nil {
			quiet.events <- c // heard, so the module removes it
		}
	}
	quiet.events <- filepath.Join(root, files.Own+"settled") // taken once every cookie was handled
	r, err := ix.Call(lsp.Command)
	if err != nil {
		return nil, fmt.Errorf("lsptest: %s: %w", lsp.Command, err)
	}
	var a lsp.Answer
	if err := json.Unmarshal([]byte(r.Text), &a); err != nil {
		return nil, fmt.Errorf("lsptest: %s answers no findings: %w", lsp.Command, err)
	}
	return a.Findings, nil
}

// heard is a watcher that hears only what Findings hands it.
type heard struct {
	events chan string
	errors chan error
}

func (h *heard) Add(string) error      { return nil }
func (h *heard) Events() <-chan string { return h.events }
func (h *heard) Errors() <-chan error  { return h.errors }
func (h *heard) Close() error          { return nil }
