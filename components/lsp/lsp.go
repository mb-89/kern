// Package lsp is kern's lsp component: the checker (package lint) run over
// the warm model (components/files). Its findings are a name the index
// keeps in step with every managed file, and its read-only command `lsp
// lint` answers them, with the editor's open buffers in place of the files
// they hold where it is handed them. The language server (`<name> lsp`,
// package kern/lsp), the command line's `<name> lint` and the tests
// (lsptest) all ask it, so they find the same. README.md beside this file is
// its contract.
package lsp

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"kern/components/files"
	"kern/index"
	"kern/lint"
)

// Name is the component's name, which the wiring's instances name.
const Name = "lsp"

// Command is the command that answers the findings: what `<name> lint`
// and the language server call.
const Command = "lsp lint"

// The config keys' built-in values.
const (
	// Templates is the folder of the templates the checker reads.
	Templates = ".se/templates"
	// NameLength is the most characters a file's name holds.
	NameLength = 64
	// NameWords is the most words a file's name holds, the work model's name rule's.
	NameWords = 5
)

// Answer is what the command answers, as JSON: every finding, sorted, and
// how many files were checked.
type Answer struct {
	Findings []lint.Finding `json:"findings"`
	Checked  int            `json:"checked"`
}

// input is what the checker reads: the model, its folders, and the config.
type input struct {
	Model      map[string]files.File `index:"model"`
	Folders    []string              `index:"folders"`
	Templates  string                `index:"config/templates"`
	NameLength int                   `index:"config/name-length"`
	NameWords  int                   `index:"config/name-words"`
}

// Check answers the findings over the model, each file in open standing in
// place of the one the model holds at its path, and one in open the model
// lacks added where it lies in a managed folder (a new file not saved yet).
func (in input) Check(open map[string]string) Answer {
	byPath := map[string]lint.File{}
	for _, f := range in.Model {
		byPath[f.Path] = lint.File{Path: f.Path, Content: f.Content, Held: f.Held}
	}
	for p, content := range open {
		if _, held := byPath[p]; held || under(p, in.Folders) {
			byPath[p] = lint.File{Path: p, Content: content, Held: true}
		}
	}
	all := make([]lint.File, 0, len(byPath))
	for _, f := range byPath {
		all = append(all, f)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Path < all[j].Path })
	found := lint.Check(all, lint.Config{Folders: in.Folders, Templates: in.Templates, NameLength: in.NameLength, NameWords: in.NameWords})
	if found == nil {
		found = []lint.Finding{}
	}
	return Answer{Findings: found, Checked: len(all)}
}

// under says whether p lies in one of folders.
func under(p string, folders []string) bool {
	for _, f := range folders {
		if f = path.Clean(f); p == f || strings.HasPrefix(p, f+"/") {
			return true
		}
	}
	return false
}

// Open reads the command's one argument where given: the open buffers,
// a JSON object of each path, relative to the root and slash-separated, to
// its text.
func Open(args []string) (map[string]string, error) {
	switch len(args) {
	case 0:
		return nil, nil
	case 1:
		var open map[string]string
		if err := json.Unmarshal([]byte(args[0]), &open); err != nil {
			return nil, fmt.Errorf("%s: its argument is the open buffers, a JSON object of path to text: %v", Command, err)
		}
		return open, nil
	}
	return nil, fmt.Errorf("%s takes one argument at most, the open buffers, and was given %d", Command, len(args))
}

// Component is the lsp component a binary carries; importing this package
// registers it, and only a wiring's enabled instance runs it.
var Component = index.Declare(Name, "Checks the managed files over the warm model: each file's template or schema, links that reach nothing, names over their caps, and generated files marked and declared (package lint).",
	index.Config("templates", Templates, "The folder of the templates, <name>.json, relative to the checkout's root: only those the model holds are read."),
	index.Config("name-length", NameLength, "The most characters a managed file's name holds; 0 sets no cap."),
	index.Config("name-words", NameWords, "The most words a managed file's name holds before its first dot, split at dashes, underscores and spaces; 0 sets no cap."),
	index.Derived("findings", []lint.Finding{}, "Every finding over the model, sorted by path, line and column: the language server republishes on its change.", func(in input) ([]lint.Finding, error) {
		return in.Check(nil).Findings, nil
	}),
	index.Command(index.CommandDecl{
		Name:    Command,
		Usage:   Command + " [OPEN]",
		Summary: "Check the managed files.",
		Help: "Answers every finding over the warm model as JSON, {\"findings\": [{\"path\", \"line\", \"col\", \"end_col\", \"rule\", \"message\", \"severity\"}], \"checked\"}; severity \"notice\" fails nothing. " +
			"OPEN, where given, is a JSON object of path to text: the editor's open buffers, each in place of the file at its path.",
		Group:    "Checks",
		Class:    index.Interactive,
		ReadOnly: true,
		MCP:      "Use to find what is wrong with the managed files (a template or schema they break, a link that reaches nothing, a name over its cap, a generated file unmarked or undeclared) before a test or a reviewer does: the editor's language server shows the same findings.",
	}, func(in input, args []string) (index.Result, error) {
		open, err := Open(args)
		if err != nil {
			return index.Result{}, err
		}
		data, err := json.Marshal(in.Check(open))
		if err != nil {
			return index.Result{}, err
		}
		return index.Result{Text: string(data)}, nil
	}),
)

func init() { index.Register(Component) }
