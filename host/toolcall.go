package host

import (
	"encoding/json"
)

// PreToolUse is the host's event before a tool runs, the one a guard
// (index.Guards) refuses a tool call on: Claude Code's name, and the Copilot
// CLI's in its VS Code compatible format. PreToolUseCamel is the Copilot CLI's
// name in its camelCase format.
const (
	PreToolUse      = "PreToolUse"
	PreToolUseCamel = "preToolUse"
)

// ToolCall is the tool call a PreToolUse or PostToolUse payload names, read
// the same whichever host wrote it.
type ToolCall struct {
	// Name is the tool's name as the host gave it.
	Name string
	// Tool is its Claude Code name: a Copilot runtime name is mapped onto the
	// Claude name the Copilot CLI's hook reference gives it (CopilotTools), and
	// a name without one is kept.
	Tool string
	// Input is the tool's arguments: tool_input, or the Copilot CLI's
	// toolArgs, which may come as a JSON string.
	Input map[string]any
	// Cwd is the working directory the host names, "" for none.
	Cwd string
	// Camel says the payload is the Copilot CLI's camelCase format (toolName),
	// whose hook answer it reads at the top level; else it is snake_case
	// (tool_name), Claude Code's and VS Code's.
	Camel bool
}

// CopilotTools maps the Copilot CLI's runtime tool names onto the Claude Code
// names its hook reference gives them ("Claude-format matchers"): a PascalCase
// PreToolUse payload already reports the Claude name, a camelCase one the
// runtime name.
var CopilotTools = map[string]string{
	"bash": "Bash", "powershell": "Bash", "view": "Read", "create": "Write",
	"edit": "Edit", "str_replace_editor": "Edit", "apply_patch": "Edit",
	"grep": "Grep", "rg": "Grep", "glob": "Glob",
}

// ParseToolCall reads the tool call of a hook payload; a payload that names
// none answers a ToolCall without a Name.
func ParseToolCall(payload []byte) ToolCall {
	var p struct {
		ToolName  string          `json:"tool_name"`
		ToolCamel string          `json:"toolName"`
		Input     json.RawMessage `json:"tool_input"`
		Args      json.RawMessage `json:"toolArgs"`
		Cwd       string          `json:"cwd"`
	}
	_ = json.Unmarshal(payload, &p)
	t := ToolCall{Name: p.ToolName, Cwd: p.Cwd}
	if t.Name == "" && p.ToolCamel != "" {
		t.Name, t.Camel = p.ToolCamel, true
	}
	t.Tool = t.Name
	if claude, ok := CopilotTools[t.Name]; ok {
		t.Tool = claude
	}
	raw := p.Input
	if len(raw) == 0 {
		raw = p.Args
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		raw = []byte(text)
	}
	_ = json.Unmarshal(raw, &t.Input)
	if t.Input == nil {
		t.Input = map[string]any{}
	}
	return t
}

// String answers the string argument key of the call, "" where it holds none.
func (t ToolCall) String(key string) string {
	s, _ := t.Input[key].(string)
	return s
}
