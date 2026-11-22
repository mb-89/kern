package files_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kern/components/files"
	"kern/host"
	"kern/index"
	"kern/mcp"
)

// claudeCall is a PreToolUse payload as Claude Code, and the Copilot CLI's
// VS Code compatible format, hand it: snake_case, the Claude tool name.
func claudeCall(tool string, input map[string]any, cwd string) []byte {
	in, _ := json.Marshal(input)
	return []byte(`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"` + tool + `","tool_input":` + string(in) + `,"cwd":` + quote(cwd) + `}`)
}

// copilotCall is a preToolUse payload as the Copilot CLI's camelCase format
// hands it: the runtime tool name, toolArgs as a JSON string.
func copilotCall(tool string, args map[string]any, cwd string) []byte {
	in, _ := json.Marshal(args)
	return []byte(`{"sessionId":"s1","timestamp":1,"cwd":` + quote(cwd) + `,"toolName":"` + tool + `","toolArgs":` + quote(string(in)) + `}`)
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// A write to a managed file is refused in both hosts' formats, by Write,
// Edit and a parsed shell command, naming the files module's tools and the
// file; a write elsewhere, a read, kern's own folder and another event pass.
func TestAWriteToAManagedFileIsRefusedWithItsRemedy(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, rel := range []string{"docs/a.md", "notes/n.md"} {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("a\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := started(t, root, map[string]any{"folders": []string{"docs"}})
	s.watched("docs")
	a := filepath.Join(root, "docs", "a.md")
	guard := func(event string, payload []byte) string {
		return s.files.Component.Guard("files", event, payload)
	}
	for _, c := range []struct {
		name, event string
		payload     []byte
	}{
		{"Claude Code's Write", host.PreToolUse, claudeCall("Write", map[string]any{"file_path": a, "content": "x"}, root)},
		{"Claude Code's Edit", host.PreToolUse, claudeCall("Edit", map[string]any{"file_path": a, "old_string": "a", "new_string": "b"}, root)},
		{"Claude Code's Bash", host.PreToolUse, claudeCall("Bash", map[string]any{"command": "cd docs && echo x > a.md"}, root)},
		{"Copilot's create", host.PreToolUseCamel, copilotCall("create", map[string]any{"path": "docs/a.md", "file_text": "x"}, root)},
		{"Copilot's edit", host.PreToolUseCamel, copilotCall("edit", map[string]any{"path": a, "old_str": "a", "new_str": "b"}, root)},
		{"Copilot's bash", host.PreToolUseCamel, copilotCall("bash", map[string]any{"command": "sed -i s/a/b/ docs/a.md"}, root)},
		{"Copilot's VS Code compatible Bash", host.PreToolUse, claudeCall("Bash", map[string]any{"command": "rm docs/a.md"}, root)},
	} {
		reason := guard(c.event, c.payload)
		for _, want := range []string{"docs/a.md", "managed", "files read", "files_write", "files edit", "--version"} {
			if !strings.Contains(reason, want) {
				t.Errorf("%s of a managed file answers %q, and wants the refusal to name %q", c.name, reason, want)
			}
		}
	}
	if reason := guard(host.PreToolUse, claudeCall("Bash", map[string]any{"command": "rm -r docs"}, root)); !strings.Contains(reason, "docs") {
		t.Errorf("removing a managed folder answers %q, and wants it refused", reason)
	}
	for _, c := range []struct {
		name, event string
		payload     []byte
	}{
		{"a Write outside the managed folders", host.PreToolUse, claudeCall("Write", map[string]any{"file_path": filepath.Join(root, "notes", "n.md")}, root)},
		{"a Copilot create outside them", host.PreToolUseCamel, copilotCall("create", map[string]any{"path": "notes/n.md"}, root)},
		{"a Write into kern's own folder", host.PreToolUse, claudeCall("Write", map[string]any{"file_path": filepath.Join(root, ".se", "x.md")}, root)},
		{"a Read of a managed file", host.PreToolUse, claudeCall("Read", map[string]any{"file_path": a}, root)},
		{"a shell read of a managed file", host.PreToolUse, claudeCall("Bash", map[string]any{"command": "cat docs/a.md > /dev/null"}, root)},
		{"a shell write it cannot be sure of", host.PreToolUse, claudeCall("Bash", map[string]any{"command": `echo x > "$F"`}, root)},
		{"a write into git's folder under a managed one", host.PreToolUse, claudeCall("Write", map[string]any{"file_path": filepath.Join(root, "docs", ".git", "x")}, root)},
		{"a PostToolUse of a write", "PostToolUse", claudeCall("Write", map[string]any{"file_path": a}, root)},
	} {
		if reason := guard(c.event, c.payload); reason != "" {
			t.Errorf("%s answers %q, and wants it to pass", c.name, reason)
		}
	}
}

// Without a started instance the guard lets every call pass: an application
// that manages no folder has nothing to refuse.
func TestAGuardWithoutAnInstancePasses(t *testing.T) {
	t.Parallel()
	f := files.New(files.Options{Root: t.TempDir(), Watch: func() (files.Watcher, error) { return newFed(), nil }})
	if reason := f.Component.Guard("files", host.PreToolUse, claudeCall("Write", map[string]any{"file_path": "/docs/a.md"}, "/")); reason != "" {
		t.Fatalf("a guard with no instance answers %q", reason)
	}
}

// indexed starts the component on a real index over root, so its commands
// ask its own verbs through the core's request delivery.
func indexed(t *testing.T, root string, folders ...string) (*files.Files, *index.Index) {
	t.Helper()
	f := files.New(files.Options{Root: root, Clock: clock{make(chan time.Time)}, Watch: func() (files.Watcher, error) { return newFed(), nil }})
	w := index.Wiring{Instances: []index.Instance{{Name: "files", Component: files.Name, Enabled: true, Line: 1,
		Config: map[string]index.ConfigValue{"folders": {Value: anyOf(folders), Line: 1}}}}}
	ix, err := index.Start(w, f.Component)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Stop)
	return f, ix
}

func anyOf(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// The door's commands read and write through the model: read answers the
// content and its version, edit replaces one string naming that version,
// write writes the whole file, remove removes it, and each refuses a stale
// version, naming re-read; the door serves them as files and files_write.
func TestTheDoorCommandsWriteThroughTheModel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "a.md"), []byte("one two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, ix := indexed(t, root, "docs")
	r, err := ix.Call("files read", "--path", "docs/a.md")
	if err != nil {
		t.Fatal(err)
	}
	head, content, _ := strings.Cut(r.Text, "\n")
	version := strings.Fields(head)[3]
	if !strings.HasPrefix(head, "docs/a.md at version ") || content != "one two\n" {
		t.Fatalf("files read answers %q", r.Text)
	}
	if _, err := ix.Call("files edit", "--path", "docs/a.md", "--version", "0000", "--old", "two", "--new", "three"); err == nil || !strings.Contains(err.Error(), "re-read it") {
		t.Fatalf("an edit naming a stale version answers %v, and wants it refused naming re-read", err)
	}
	if _, err := ix.Call("files edit", "--path", "docs/a.md", "--version", version, "--old", "four", "--new", "five"); err == nil || !strings.Contains(err.Error(), "not in docs/a.md") {
		t.Fatalf("an edit whose old text is not in the file answers %v", err)
	}
	if r, err := ix.Call("files edit", "--path", "docs/a.md", "--version", version, "--old", "two", "--new", "three"); err != nil || !strings.Contains(r.Text, "edited docs/a.md, now at version ") {
		t.Fatalf("files edit answers %q, %v", r.Text, err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "docs", "a.md")); string(data) != "one three\n" {
		t.Fatalf("after the edit the disk holds %q", data)
	}
	if _, err := ix.Call("files write", "--path", "docs/a.md", "--version", version, "--content", "x"); err == nil || !strings.Contains(err.Error(), "re-read it") {
		t.Fatalf("a write naming the version before the edit answers %v, and wants it refused naming re-read", err)
	}
	if r, err := ix.Call("files write", "--path", "docs/new/b.md", "--version", "", "--content", "b\n"); err != nil || !strings.Contains(r.Text, "wrote docs/new/b.md") {
		t.Fatalf("files write of a new file answers %q, %v", r.Text, err)
	}
	r, _ = ix.Call("files read", "--path", "docs/new/b.md")
	if _, err := ix.Call("files remove", "--path", "docs/new/b.md", "--version", strings.Fields(r.Text)[3]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "new", "b.md")); !os.IsNotExist(err) {
		t.Fatalf("after files remove the file stands: %v", err)
	}
	w := index.Wiring{Instances: []index.Instance{{Name: "files", Component: files.Name, Enabled: true, Line: 1, Config: map[string]index.ConfigValue{}}}}
	catalog, err := index.Collect(w, files.New(files.Options{}).Component)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := mcp.Tools("kern", catalog, nil)
	if err != nil {
		t.Fatal(err)
	}
	served := map[string][]string{}
	for _, tool := range tools {
		for _, c := range tool.Commands {
			served[tool.Name] = append(served[tool.Name], c.Name)
		}
	}
	if strings.Join(served["files"], ",") != "files read" || strings.Join(served["files_write"], ",") != "files edit,files remove,files write" {
		t.Fatalf("the door serves %v, and wants files read in files and the writes in files_write", served)
	}
}

// searched is a started instance over a docs folder the model holds and a
// notes folder it does not, watched.
func searched(t *testing.T, config map[string]any) (*setup, string) {
	t.Helper()
	root := t.TempDir()
	for rel, content := range map[string]string{"docs/a.md": "alpha\nbeta\n", "docs/sub/b.txt": "Beta gamma\nbeta\n", "notes/n.md": "beta\n"} {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if config == nil {
		config = map[string]any{}
	}
	config["folders"] = []string{"docs"}
	s := started(t, root, config)
	s.watched("docs")
	return s, root
}

// Grep and Glob over a managed folder are answered from the model, in both
// hosts' formats, as the native tool answers them: the files, the lines or the
// counts; a change on the disk the watcher has not handed yet is not seen.
func TestASearchOverAManagedFolderIsAnsweredFromTheModel(t *testing.T) {
	t.Parallel()
	s, root := searched(t, nil)
	docs := filepath.Join(root, "docs")
	a, b := filepath.Join(docs, "a.md"), filepath.Join(docs, "sub", "b.txt")
	s.put("docs/a.md", "unheard\n") // on the disk, not yet handed by the watcher
	guard := func(event string, payload []byte) string { return s.files.Component.Guard("files", event, payload) }
	for _, c := range []struct {
		name, event string
		payload     []byte
		want        []string
		not         []string
	}{
		{"Claude Code's Grep", host.PreToolUse, claudeCall("Grep", map[string]any{"pattern": "beta", "path": docs}, root),
			[]string{"from its warm model", "Found 2 files", a, b}, []string{"unheard"}},
		{"its content mode", host.PreToolUse, claudeCall("Grep", map[string]any{"pattern": "^beta", "path": docs, "output_mode": "content", "-n": true}, root),
			[]string{a + ":2:beta", b + ":2:beta"}, []string{"Beta gamma"}},
		{"its count mode, case folded", host.PreToolUse, claudeCall("Grep", map[string]any{"pattern": "beta", "path": "docs", "output_mode": "count", "-i": true}, root),
			[]string{a + ":1", b + ":2"}, nil},
		{"its glob filter", host.PreToolUse, claudeCall("Grep", map[string]any{"pattern": "beta", "path": docs, "glob": "*.txt"}, root),
			[]string{"Found 1 file", b}, []string{a}},
		{"a Grep of one managed file", host.PreToolUse, claudeCall("Grep", map[string]any{"pattern": "alpha", "path": a}, root),
			[]string{"Found 1 file", a}, nil},
		{"a Grep that finds nothing", host.PreToolUse, claudeCall("Grep", map[string]any{"pattern": "omega", "path": docs}, root),
			[]string{"No files found"}, nil},
		{"Copilot's grep", host.PreToolUseCamel, copilotCall("grep", map[string]any{"pattern": "gamma", "path": "docs"}, root),
			[]string{"Found 1 file", b}, nil},
		{"Claude Code's Glob", host.PreToolUse, claudeCall("Glob", map[string]any{"pattern": "**/*.txt", "path": docs}, root),
			[]string{b}, []string{a}},
		{"a Glob from the root into a managed folder", host.PreToolUse, claudeCall("Glob", map[string]any{"pattern": "docs/**/*.md"}, root),
			[]string{a}, []string{b}},
		{"Copilot's glob", host.PreToolUseCamel, copilotCall("glob", map[string]any{"pattern": "docs/*.md"}, root),
			[]string{a}, []string{b}},
	} {
		reason := guard(c.event, c.payload)
		for _, want := range c.want {
			if !strings.Contains(reason, want) {
				t.Errorf("%s answers %q, and wants it to hold %q", c.name, reason, want)
			}
		}
		for _, not := range c.not {
			if strings.Contains(reason, not) {
				t.Errorf("%s answers %q, which holds %q", c.name, reason, not)
			}
		}
	}
}

// On any doubt a search passes, so the native tool reads the disk: a scope
// beyond the managed folders, an option or a pattern the model does not
// answer, a file it holds by hash alone, and a model that is not watched.
func TestASearchInDoubtPassesToTheDisk(t *testing.T) {
	t.Parallel()
	s, root := searched(t, nil)
	docs := filepath.Join(root, "docs")
	for _, c := range []struct {
		name    string
		payload []byte
	}{
		{"a Grep of the whole checkout", claudeCall("Grep", map[string]any{"pattern": "beta"}, root)},
		{"a Grep outside the managed folders", claudeCall("Grep", map[string]any{"pattern": "beta", "path": filepath.Join(root, "notes")}, root)},
		{"a Grep with context lines", claudeCall("Grep", map[string]any{"pattern": "beta", "path": docs, "-A": 2}, root)},
		{"a Grep by file type", claudeCall("Grep", map[string]any{"pattern": "beta", "path": docs, "type": "md"}, root)},
		{"a Grep across lines", claudeCall("Grep", map[string]any{"pattern": "a\\nb", "path": docs, "multiline": true}, root)},
		{"a pattern Go's regexp does not take", claudeCall("Grep", map[string]any{"pattern": "(?<=a)b", "path": docs}, root)},
		{"a Glob with braces", claudeCall("Glob", map[string]any{"pattern": "**/*.{md,txt}", "path": docs}, root)},
		{"a Glob beyond the managed folders", claudeCall("Glob", map[string]any{"pattern": "**/*.md"}, root)},
		{"a Glob outside them", claudeCall("Glob", map[string]any{"pattern": "notes/*.md"}, root)},
	} {
		if reason := s.files.Component.Guard("files", host.PreToolUse, c.payload); reason != "" {
			t.Errorf("%s answers %q, and wants it to pass", c.name, reason)
		}
	}

	big, root := searched(t, map[string]any{"content-limit": 8})
	if reason := big.files.Component.Guard("files", host.PreToolUse, claudeCall("Grep", map[string]any{"pattern": "beta", "path": filepath.Join(root, "docs")}, root)); reason != "" {
		t.Errorf("a Grep over a file held by hash alone answers %q, and wants it to pass", reason)
	}
	if reason := big.files.Component.Guard("files", host.PreToolUse, claudeCall("Glob", map[string]any{"pattern": "*.md", "path": filepath.Join(root, "docs")}, root)); !strings.Contains(reason, "a.md") {
		t.Errorf("a Glob, which reads no content, answers %q over a file held by hash alone, and wants it answered", reason)
	}

	unwatched, uroot := func() (*setup, string) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		s := started(t, root, map[string]any{"folders": []string{"docs"}})
		s.clock.fire <- time.Time{} // the cookie's wait ends unheard
		s.settle()
		return s, root
	}()
	if reason := unwatched.files.Component.Guard("files", host.PreToolUse, claudeCall("Glob", map[string]any{"pattern": "*", "path": filepath.Join(uroot, "docs")}, uroot)); reason != "" {
		t.Errorf("a Glob over an unwatched model answers %q, and wants it to pass", reason)
	}
}
