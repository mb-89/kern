package host_test

import (
	"testing"

	"kern/host"
)

// A tool call reads the same from each host's payload: Claude Code's, the
// Copilot CLI's VS Code compatible one (Claude names, snake_case) and its
// camelCase one (runtime names, toolArgs as a JSON string or an object).
func TestAToolCallReadsTheSameFromEveryHost(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		payload, tool, path string
		camel               bool
	}{
		{`{"session_id":"s1","tool_name":"Write","tool_input":{"file_path":"/r/docs/a.md","content":"x"},"cwd":"/r"}`, "Write", "/r/docs/a.md", false},
		{`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"path":"docs/a.md"},"cwd":"/r"}`, "Edit", "docs/a.md", false},
		{`{"sessionId":"s1","cwd":"/r","toolName":"create","toolArgs":"{\"path\":\"docs/a.md\",\"file_text\":\"x\"}"}`, "Write", "docs/a.md", true},
		{`{"sessionId":"s1","cwd":"/r","toolName":"str_replace_editor","toolArgs":{"path":"docs/a.md"}}`, "Edit", "docs/a.md", true},
		{`{"sessionId":"s1","cwd":"/r","toolName":"report_intent","toolArgs":{}}`, "report_intent", "", true},
	} {
		got := host.ParseToolCall([]byte(c.payload))
		path := got.String("file_path")
		if path == "" {
			path = got.String("path")
		}
		if got.Tool != c.tool || path != c.path || got.Camel != c.camel || got.Cwd != "/r" {
			t.Errorf("%s reads %+v, and wants the tool %s at %q (camel %v)", c.payload, got, c.tool, c.path, c.camel)
		}
	}
	if got := host.ParseToolCall([]byte(`{"session_id":"s1"}`)); got.Name != "" || got.Input == nil {
		t.Errorf("a payload with no tool reads %+v, and wants no name and empty input", got)
	}
}
