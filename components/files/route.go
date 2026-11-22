package files

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"kern/host"
	"kern/mcp"
)

// own is kern's own folder in a checkout, whose writes always pass: the
// runtime, the work tickets and the overlay wiring live there.
const own = ".se"

// guard is the lean routing of the agent's tools (README.md "Agent tools"): a
// host's PreToolUse that writes a managed file is refused, naming the
// module's tools; a Grep or a Glob over a managed folder is answered from the
// model (answerSearch); every other call passes.
func (f *Files) guard(_, event string, payload []byte) string {
	if event != host.PreToolUse && event != host.PreToolUseCamel {
		return ""
	}
	call := host.ParseToolCall(payload)
	if call.Name == "" {
		return ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.instance == "" {
		return ""
	}
	if refused := f.refusedWrites(call); len(refused) > 0 {
		return f.writeRemedy(call, refused)
	}
	return f.answerSearch(call)
}

// refusedWrites answers the managed files and folders the call writes,
// relative to the root and sorted. The caller holds the lock.
func (f *Files) refusedWrites(call host.ToolCall) []string {
	seen := map[string]bool{}
	for _, abs := range call.Writes(f.root) {
		rel, ok := f.relOfAny(abs)
		if !ok || rel == own || strings.HasPrefix(rel, own+"/") {
			continue
		}
		if f.managed(rel) || f.managedDir(rel) || f.holdsManaged(rel) {
			seen[rel] = true
		}
	}
	var out []string
	for rel := range seen {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

// holdsManaged says whether the folder rel holds a managed folder, as the
// root does: a write that removes or moves it removes them. The caller holds
// the lock.
func (f *Files) holdsManaged(rel string) bool {
	for _, folder := range f.folders {
		if strings.HasPrefix(folder, rel+"/") {
			return true
		}
	}
	return false
}

// relOfAny answers abs relative to the root, as relOf does, also where the
// root or the path reaches it through a symbolic link or another spelling of
// a folder, as macOS's temporary folders do: both sides are tried with their
// links resolved.
func (f *Files) relOfAny(abs string) (string, bool) {
	if rel, ok := f.relOf(abs); ok {
		return rel, true
	}
	real := resolved(abs)
	for _, root := range []string{f.root, f.real} {
		if root == "" {
			continue
		}
		for _, p := range []string{abs, real} {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			if rel != "." && rel != ".." && !strings.HasPrefix(rel, "../") {
				return rel, true
			}
		}
	}
	return "", false
}

// resolved answers p with the links of its longest standing prefix resolved,
// the rest of it joined as it is: a file a write is about to make does not
// stand yet.
func resolved(p string) string {
	rest := ""
	for dir := filepath.Clean(p); ; dir = filepath.Dir(dir) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return p
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

// writeRemedy is the refusal of a call that writes managed files: what it
// writes, and the module's tools that write them instead. The caller holds
// the lock.
func (f *Files) writeRemedy(call host.ToolCall, refused []string) string {
	read, write := mcp.ToolName(f.instance), mcp.ToolName(f.instance)+mcp.WriteSuffix
	first := refused[0]
	return fmt.Sprintf("kern refuses this %s: it writes %s, in the managed folders (%s), which only kern's files module writes, so its model of them stays true. "+
		"Do it through the module's MCP tools instead: read the file with the tool %s, command \"files read\", args [\"--path\", %q], which answers its content and its version; "+
		"then write it with the tool %s: command \"files edit\" with args [\"--path\", %q, \"--version\", \"<version>\", \"--old\", \"<text>\", \"--new\", \"<text>\"] to replace one string, "+
		"\"files write\" with \"--content\" for the whole file, or \"files remove\" to remove it. A new file names --version \"\". "+
		"Files outside the managed folders are written as before.",
		call.Name, strings.Join(refused, ", "), strings.Join(f.folders, ", "), read, first, write, first)
}
