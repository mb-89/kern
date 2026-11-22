package host_test

import (
	"path/filepath"
	"slices"
	"testing"

	"kern/host"
)

// A shell command's writes are the files its redirections, its writing
// commands and its in-place edits name, each resolved against the working
// directory and any cd before it; a target it cannot be sure of (a variable,
// a glob, a command substitution) is no write, so the call passes.
func TestAShellCommandsWritesAreParsed(t *testing.T) {
	t.Parallel()
	cwd := filepath.Join(string(filepath.Separator), "r")
	at := func(rel ...string) []string {
		var out []string
		for _, r := range rel {
			out = append(out, filepath.Join(cwd, filepath.FromSlash(r)))
		}
		return out
	}
	for _, c := range []struct {
		command string
		want    []string
	}{
		{`echo hi > docs/a.md`, at("docs/a.md")},
		{`echo hi>>docs/a.md 2>/dev/null`, at("docs/a.md")},
		{`printf x | tee -a docs/a.md docs/b.md >/dev/null`, at("docs/a.md", "docs/b.md")},
		{`cat <<'EOF' > docs/a.md
a line with > inside
EOF
ls`, at("docs/a.md")},
		{`cp src.md docs/a.md && mv docs/old.md docs/new.md`, at("docs/a.md", "docs/old.md", "docs/new.md")},
		{`rm -f docs/a.md; touch "docs/b c.md"`, at("docs/a.md", "docs/b c.md")},
		{`sed -i 's/a/b/' docs/a.md docs/b.md`, at("docs/a.md", "docs/b.md")},
		{`sed -e 's/a/b/' docs/a.md`, nil},
		{`perl -pi -e 's/a/b/' docs/a.md`, at("docs/a.md")},
		{`cd docs && echo x > a.md`, at("docs/a.md")},
		{`dd if=/dev/zero of=docs/a.bin bs=1 count=1`, at("docs/a.bin")},
		{`truncate -s 0 docs/a.md`, at("docs/a.md")},
		{`sudo FOO=1 tee docs/a.md`, at("docs/a.md")},
		{`echo x > /tmp/abs.md`, []string{filepath.Join(string(filepath.Separator), "tmp", "abs.md")}},
		{`Set-Content -Path docs/a.md -Value x; Remove-Item docs/b.md`, at("docs/a.md", "docs/b.md")},
		{`echo x > "$OUT"; echo y > docs/*.md; echo z > $(pick)`, nil},
		{`cat docs/a.md | grep x; ls docs > /dev/null 2>&1`, nil},
		{`git diff 2>&1 >&2`, nil},
	} {
		got := host.ShellWrites(c.command, cwd)
		if !slices.Equal(got, c.want) {
			t.Errorf("%q writes %q, and wants %q", c.command, got, c.want)
		}
	}
}

// A tool call's writes: Claude Code's Write, Edit, MultiEdit and NotebookEdit,
// the Copilot CLI's create, edit, str_replace_editor (but its view) and
// apply_patch, and a shell call's parsed writes; a read writes nothing.
func TestAToolCallsWritesAreItsFiles(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "r")
	a := filepath.Join(root, "docs", "a.md")
	for _, c := range []struct {
		payload string
		want    []string
	}{
		{`{"tool_name":"Write","tool_input":{"file_path":"/r/docs/a.md","content":"x"}}`, []string{a}},
		{`{"tool_name":"Edit","tool_input":{"file_path":"docs/a.md"},"cwd":"/r"}`, []string{a}},
		{`{"tool_name":"MultiEdit","tool_input":{"file_path":"/r/docs/a.md","edits":[]}}`, []string{a}},
		{`{"tool_name":"NotebookEdit","tool_input":{"notebook_path":"/r/docs/a.md"}}`, []string{a}},
		{`{"tool_name":"Bash","tool_input":{"command":"echo x > docs/a.md"},"cwd":"/r"}`, []string{a}},
		{`{"toolName":"create","toolArgs":"{\"path\":\"docs/a.md\",\"file_text\":\"x\"}","cwd":"/r"}`, []string{a}},
		{`{"toolName":"edit","toolArgs":{"path":"/r/docs/a.md","old_str":"a","new_str":"b"}}`, []string{a}},
		{`{"toolName":"str_replace_editor","toolArgs":{"command":"view","path":"/r/docs/a.md"}}`, nil},
		{`{"toolName":"apply_patch","toolArgs":{"input":"*** Begin Patch\n*** Update File: docs/a.md\n@@\n-a\n+b\n*** End Patch"},"cwd":"/r"}`, []string{a}},
		{`{"toolName":"powershell","toolArgs":{"command":"Out-File -FilePath docs/a.md"},"cwd":"/r"}`, []string{a}},
		{`{"tool_name":"Read","tool_input":{"file_path":"/r/docs/a.md"}}`, nil},
	} {
		got := host.ParseToolCall([]byte(c.payload)).Writes(root)
		for i := range got {
			got[i] = filepath.Clean(got[i])
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s writes %q, and wants %q", c.payload, got, c.want)
		}
	}
}
