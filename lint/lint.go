// Package lint is kern's one checker of managed files: the language server
// (`<name> lsp`, package lsp), the command line's `<name> lint` and the tests
// (components/lsp/lsptest) all ask Check, so a rule is written once and
// reaches all three, and what the editor shows is what the tests find. Its
// rules are each managed file's template or schema, links that reach
// nothing, and names over their caps (README.md "Language server").
//
// Check is pure: it reads the files it is handed and nothing else, so its
// callers hand it the warm model (components/files), with the editor's open
// buffers in place of the files they hold.
package lint

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf16"
)

// The rules, as each finding names its own.
const (
	// RuleTemplate is a Markdown file rendered from a template that no
	// longer conforms to it, or a template that cannot be read.
	RuleTemplate = "template"
	// RuleSchema is a JSON file that is no JSON, or breaks the schema its
	// "$schema" names.
	RuleSchema = "schema"
	// RuleLink is a Markdown link into a managed folder that reaches nothing.
	RuleLink = "link"
	// RuleName is a file whose name is over a cap.
	RuleName = "name"
)

// Rules are the rules Check runs, in the order a finding's rule sorts.
var Rules = []string{RuleTemplate, RuleSchema, RuleLink, RuleName}

// File is one file the checker reads: its path relative to the checkout's
// root, slash-separated, and its content where Held. A file held by its hash
// alone (too large, or binary) is known to stand and is checked by its name
// alone.
type File struct {
	Path    string
	Content string
	Held    bool
}

// Config is what the checker is told beside the files.
type Config struct {
	// Folders are the managed folders, relative to the root and
	// slash-separated: a link is judged where its target lies under one,
	// since only there does the checker know every file that stands.
	Folders []string
	// Templates is the folder whose <name>.json files are the templates,
	// relative to the root and slash-separated; "" is none. Only the
	// templates the files handed hold are read.
	Templates string
	// NameLength is the most characters a file's name may hold; 0 sets no cap.
	NameLength int
	// NameWords is the most words a file's name may hold before its first
	// dot, split at dashes, underscores and spaces; 0 sets no cap.
	NameWords int
}

// Finding is one thing wrong with one file.
type Finding struct {
	// Path is the file's path relative to the root, slash-separated.
	Path string `json:"path"`
	// Line is its line, from 1.
	Line int `json:"line"`
	// Col is where on the line it starts, from 1, in UTF-16 code units as
	// the Language Server Protocol counts them.
	Col int `json:"col"`
	// EndCol is where it ends, exclusive, counted as Col; 0 is the line's end.
	EndCol int `json:"end_col,omitempty"`
	// Rule is the rule that found it, one of Rules.
	Rule string `json:"rule"`
	// Message says what is wrong and, where it can, the remedy.
	Message string `json:"message"`
}

// String is the finding as `lint` prints it: path:line:col: rule: message.
func (f Finding) String() string {
	return fmt.Sprintf("%s:%d:%d: %s: %s", f.Path, f.Line, f.Col, f.Rule, f.Message)
}

// set is what one check reads: the files by path, and the config.
type set struct {
	files   map[string]File
	paths   []string // sorted
	config  Config
	folders []string
}

// Check answers every finding the rules make of files, sorted by path, line,
// column, rule and message. A file outside every managed folder is checked
// like any other: the caller hands what it manages.
func Check(files []File, c Config) []Finding {
	s := &set{files: map[string]File{}, config: c}
	for _, f := range files {
		s.files[f.Path] = f
		s.paths = append(s.paths, f.Path)
	}
	sort.Strings(s.paths)
	for _, folder := range c.Folders {
		if clean := path.Clean(folder); clean != "." {
			s.folders = append(s.folders, clean)
		}
	}
	var found []Finding
	templates, tf := s.templates()
	found = append(found, tf...)
	for _, p := range s.paths {
		f := s.files[p]
		found = append(found, s.names(f)...)
		if !f.Held || s.isTemplate(p) {
			continue
		}
		switch path.Ext(p) {
		case ".md", ".markdown":
			found = append(found, conform(f, templates)...)
			found = append(found, s.links(f)...)
		case ".json":
			found = append(found, s.schema(f)...)
		}
	}
	Sort(found)
	return found
}

// Sort orders findings as Check answers them.
func Sort(found []Finding) {
	sort.SliceStable(found, func(i, j int) bool {
		a, b := found[i], found[j]
		switch {
		case a.Path != b.Path:
			return a.Path < b.Path
		case a.Line != b.Line:
			return a.Line < b.Line
		case a.Col != b.Col:
			return a.Col < b.Col
		case a.Rule != b.Rule:
			return a.Rule < b.Rule
		}
		return a.Message < b.Message
	})
}

// managed says whether p lies in a managed folder.
func (s *set) managed(p string) bool {
	for _, folder := range s.folders {
		if p == folder || strings.HasPrefix(p, folder+"/") {
			return true
		}
	}
	return false
}

// stands says whether p is a file or a folder the files hold.
func (s *set) stands(p string) bool {
	if _, ok := s.files[p]; ok {
		return true
	}
	i := sort.SearchStrings(s.paths, p+"/")
	return i < len(s.paths) && strings.HasPrefix(s.paths[i], p+"/")
}

// names holds a file's name to its caps.
func (s *set) names(f File) []Finding {
	name := path.Base(f.Path)
	var found []Finding
	if n := len([]rune(name)); s.config.NameLength > 0 && n > s.config.NameLength {
		found = append(found, Finding{Path: f.Path, Line: 1, Col: 1, Rule: RuleName,
			Message: fmt.Sprintf("the name %s has %d characters, over the cap of %d: shorten it", name, n, s.config.NameLength)})
	}
	stem, _, _ := strings.Cut(name, ".")
	words := strings.FieldsFunc(stem, func(r rune) bool { return r == '-' || r == '_' || r == ' ' })
	if s.config.NameWords > 0 && len(words) > s.config.NameWords {
		found = append(found, Finding{Path: f.Path, Line: 1, Col: 1, Rule: RuleName,
			Message: fmt.Sprintf("the name %s has %d words, over the cap of %d: shorten it", name, len(words), s.config.NameWords)})
	}
	return found
}

// lines splits content into its lines, without their ends.
func lines(content string) []string {
	ls := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(ls) > 0 && ls[len(ls)-1] == "" {
		ls = ls[:len(ls)-1]
	}
	return ls
}

// fenced marks each line that lies in a fenced code block, its fences included.
func fenced(ls []string) []bool {
	in := make([]bool, len(ls))
	fence := ""
	for i, l := range ls {
		t := strings.TrimLeft(l, " ")
		if fence == "" {
			for _, mark := range []string{"```", "~~~"} {
				if strings.HasPrefix(t, mark) {
					fence = t[:len(t)-len(strings.TrimLeft(t, mark[:1]))]
				}
			}
			in[i] = fence != ""
			continue
		}
		in[i] = true
		if strings.HasPrefix(t, fence) && strings.TrimSpace(strings.TrimLeft(t, fence[:1])) == "" {
			fence = ""
		}
	}
	return in
}

// col is the byte offset i of line as a column from 1, in UTF-16 code units.
func col(line string, i int) int {
	if i > len(line) {
		i = len(line)
	}
	return len(utf16.Encode([]rune(line[:i]))) + 1
}
