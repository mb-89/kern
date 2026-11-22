package cli_test

import (
	"encoding/json"
	"testing"

	"kern/cli"
)

// A deny is printed in the shape the payload's host reads: hookSpecificOutput
// for a snake_case payload (Claude Code, VS Code, the Copilot CLI's VS Code
// compatible format), the top level for the Copilot CLI's camelCase one.
func TestADenyIsPrintedInEachHostsFormat(t *testing.T) {
	t.Parallel()
	const reason = "docs/a.md is a managed file: write it through the tool files_write"
	var claude struct {
		Specific map[string]string `json:"hookSpecificOutput"`
		Decision string            `json:"permissionDecision"`
	}
	snake := []byte(`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"/r/docs/a.md"}}`)
	if err := json.Unmarshal(cli.DenyOutput("PreToolUse", snake, reason), &claude); err != nil {
		t.Fatal(err)
	}
	if claude.Specific["hookEventName"] != "PreToolUse" || claude.Specific["permissionDecision"] != "deny" ||
		claude.Specific["permissionDecisionReason"] != reason || claude.Decision != "" {
		t.Fatalf("Claude Code's deny reads %s", cli.DenyOutput("PreToolUse", snake, reason))
	}
	var copilot map[string]any
	camel := []byte(`{"sessionId":"s1","timestamp":1,"cwd":"/r","toolName":"create","toolArgs":"{\"path\":\"docs/a.md\",\"file_text\":\"x\"}"}`)
	if err := json.Unmarshal(cli.DenyOutput("preToolUse", camel, reason), &copilot); err != nil {
		t.Fatal(err)
	}
	if copilot["permissionDecision"] != "deny" || copilot["permissionDecisionReason"] != reason || copilot["hookSpecificOutput"] != nil {
		t.Fatalf("the Copilot CLI's camelCase deny reads %s", cli.DenyOutput("preToolUse", camel, reason))
	}
}
