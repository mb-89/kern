package files

import (
	"bufio"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"kern/host"
)

// The arguments a search answered from the model may carry, each host's
// Grep and Glob alike; any other one is a doubt, and the search passes.
var (
	grepArgs = map[string]bool{"pattern": true, "path": true, "glob": true, "output_mode": true, "-i": true, "-n": true, "head_limit": true}
	globArgs = map[string]bool{"pattern": true, "path": true}
)

// answerSearch answers a Grep or a Glob over a managed folder from the model,
// as the reason of a deny, so the native tool does not run; "" lets it run
// and read the disk on any doubt: the model is not watched, the scope reaches
// past the managed folders, an argument or a pattern the model does not
// answer, a file the Grep would read that the model holds by hash alone, or
// more than MaxMatches answers. The caller holds the lock.
func (f *Files) answerSearch(call host.ToolCall) string {
	if !f.watching {
		return ""
	}
	switch call.Tool {
	case "Grep":
		return f.grep(call)
	case "Glob":
		return f.glob(call)
	}
	return ""
}

// known says whether every argument of the call is one the model answers.
func known(call host.ToolCall, args map[string]bool) bool {
	for k := range call.Input {
		if !args[k] {
			return false
		}
	}
	return true
}

// scope answers the managed folder, or the managed file, a search's path
// names, relative to the root, and whether it is one; the path defaults to
// the call's working directory, else the root.
func (f *Files) scope(call host.ToolCall) (rel string, file, ok bool) {
	cwd := call.Cwd
	if cwd == "" {
		cwd = f.root
	}
	dir := cwd
	if p := call.String("path"); p != "" {
		dir = absOf(cwd, p)
	}
	rel, ok = f.relOfAny(dir)
	if !ok {
		return "", false, false
	}
	if _, held := f.model[rel]; held {
		return rel, true, true
	}
	return rel, false, f.managedDir(rel)
}

// absOf answers p absolute, under cwd where it is relative.
func absOf(cwd, p string) string {
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return filepath.Clean(filepath.FromSlash(p))
	}
	return filepath.Join(cwd, filepath.FromSlash(p))
}

// header opens a search's answer: it says the answer is the search's own, so
// the agent reads it as the tool's result and does not run it again.
func (f *Files) header(tool string) string {
	return fmt.Sprintf("kern answered this %s from its warm model of the managed folders (%s), as the tool would; this is its result, not an error, so do not run it again:\n", tool, strings.Join(f.folders, ", "))
}

// grep answers a Grep from the held content: the files that match (its
// default), their lines (content) or their counts (count).
func (f *Files) grep(call host.ToolCall) string {
	if !known(call, grepArgs) {
		return ""
	}
	pattern := call.String("pattern")
	if b, _ := call.Input["-i"].(bool); b {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil || call.String("pattern") == "" {
		return ""
	}
	mode := call.String("output_mode")
	if mode == "" {
		mode = "files_with_matches"
	}
	if mode != "files_with_matches" && mode != "content" && mode != "count" {
		return ""
	}
	rel, isFile, ok := f.scope(call)
	if !ok {
		return ""
	}
	paths := []string{rel}
	if !isFile {
		paths = f.inFolder(rel)
	}
	filter := call.String("glob")
	if strings.ContainsAny(filter, "{}") {
		return ""
	}
	var lines []string
	matched := 0
	for _, p := range paths {
		if filter != "" && !globFilter(filter, rel, p) {
			continue
		}
		file := f.model[p]
		if !file.Held {
			if file.Size > int64(f.limit) {
				return "" // a large text file the native tool would read
			}
			continue // a binary file, which the native tool skips too
		}
		abs := f.abs(p)
		n := 0
		sc := bufio.NewScanner(strings.NewReader(file.Content))
		sc.Buffer(make([]byte, 64<<10), len(file.Content)+1)
		for line := 1; sc.Scan(); line++ {
			if !re.MatchString(sc.Text()) {
				continue
			}
			n++
			matched++
			if matched > MaxMatches {
				return ""
			}
			if mode == "content" {
				lines = append(lines, fmt.Sprintf("%s:%d:%s", abs, line, sc.Text()))
			}
		}
		switch {
		case n == 0:
		case mode == "files_with_matches":
			lines = append(lines, abs)
		case mode == "count":
			lines = append(lines, fmt.Sprintf("%s:%d", abs, n))
		}
	}
	lines = limit(call, lines)
	head := f.header("Grep")
	switch {
	case len(lines) == 0 && mode == "files_with_matches":
		return head + "No files found"
	case len(lines) == 0:
		return head + "No matches found"
	case mode == "files_with_matches":
		return head + fmt.Sprintf("Found %d %s\n", len(lines), plural(len(lines), "file")) + strings.Join(lines, "\n")
	}
	return head + strings.Join(lines, "\n")
}

// limit keeps the first head_limit lines, where the call names a limit above 0.
func limit(call host.ToolCall, lines []string) []string {
	if n, ok := call.Input["head_limit"].(float64); ok && n > 0 && int(n) < len(lines) {
		return lines[:int(n)]
	}
	return lines
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// globFilter says whether a Grep's glob takes the file p under the scope
// base: a glob without a slash matches the file's name, one with a slash its
// path below base, as ripgrep's --glob does.
func globFilter(glob, base, p string) bool {
	if !strings.Contains(glob, "/") {
		ok, _ := path.Match(glob, path.Base(p))
		return ok
	}
	return match(strings.Split(glob, "/"), strings.Split(strings.TrimPrefix(p, base+"/"), "/"))
}

// glob answers a Glob from the model's paths: every managed file under the
// pattern's base whose path below it the pattern matches, sorted.
func (f *Files) glob(call host.ToolCall) string {
	if !known(call, globArgs) {
		return ""
	}
	pattern := filepath.ToSlash(call.String("pattern"))
	if pattern == "" || strings.ContainsAny(pattern, "{}") || strings.HasPrefix(pattern, "/") || filepath.IsAbs(pattern) {
		return ""
	}
	cwd := call.Cwd
	if cwd == "" {
		cwd = f.root
	}
	dir := cwd
	if p := call.String("path"); p != "" {
		dir = absOf(cwd, p)
	}
	base, ok := f.relOfAny(dir)
	if !ok && filepath.Clean(dir) != filepath.Clean(f.root) && (f.real == "" || filepath.Clean(dir) != f.real) {
		return ""
	}
	segs := strings.Split(pattern, "/")
	// The pattern's literal segments move the base down, so a Glob from the
	// root that names a managed folder first is answered.
	for len(segs) > 1 && !strings.ContainsAny(segs[0], "*?[") && segs[0] != "." && segs[0] != ".." {
		base = path.Join(base, segs[0])
		segs = segs[1:]
	}
	if base == "" || !f.managedDir(base) {
		return ""
	}
	for _, s := range segs {
		if _, err := path.Match(s, ""); err != nil {
			return ""
		}
	}
	var found []string
	for _, p := range f.inFolder(base) {
		if match(segs, strings.Split(strings.TrimPrefix(p, base+"/"), "/")) {
			found = append(found, f.abs(p))
		}
	}
	if len(found) > MaxMatches {
		return ""
	}
	sort.Strings(found)
	if len(found) == 0 {
		return f.header("Glob") + "No files found"
	}
	return f.header("Glob") + strings.Join(found, "\n")
}

// match says whether the pattern's segments match the path's: ** matches any
// number of segments, every other segment one, as path.Match does.
func match(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if match(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	ok, _ := path.Match(pat[0], segs[0])
	return ok && match(pat[1:], segs[1:])
}
