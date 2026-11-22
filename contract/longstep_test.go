package contract

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"kern/bus"
	"kern/cli"
	"kern/host"
	"kern/index"
)

// The long step's limit, shortened (README.md "Tests"): replyDefault replaces
// NATS's own expiry of a reply permission that names none, two minutes, the
// one limit on the path a step that beats still runs into, and the fixture's
// local phase, gap seconds, is just longer.
const (
	replyDefault = "150ms"
	gap          = "0.3"
)

// A landing's shape against the real core, with the test as the session's
// host: a command in Python asks a GitHub step through kern's relay, the host
// answers it through the PostToolUse hook, the command works gap seconds,
// longer than the server's expiry of a reply permission, without a word but
// its beats, and asks the next step. That step reaches the relay's outbox and
// the hook, and the call lands. Before the bus took the reply's expiry off,
// the answer of the long step was refused, as two landings' were on 14 Nov.
func TestAStepAnswersAfterALongLocalPhase(t *testing.T) {
	root := checkout(t)
	dir := filepath.Join(root, "components", "gap")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{index.DeclarationFile, "__init__.py"} {
		data, err := os.ReadFile(filepath.Join("fixture", "gap", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	command, _ := json.Marshal([]string{pythonPath(t), "-m", "kern", "run", "components/gap"})
	wiring := fmt.Sprintf("[instances.gap]\ncomponent = \"gap\"\ncommand = %s\n\n"+
		"[instances.github]\ncomponent = \"github\"\nconfig = { backend = \"relay\" }\n\n"+
		"[instances.clock]\ncomponent = \"clock\"\n\n"+
		"[wires]\n\"gap.step\" = \"github.step\"\n\"github.issues\" = \"github/issues/<key>\"\n"+
		"\"clock.time\" = \"clock/time\"\n\"clock.second\" = \"clock/second\"\n\"github.second\" = \"clock/second\"\n"+
		"\"github.pulls\" = \"github/pulls/<key>\"\n\"github.comments\" = \"github/comments/<key>\"\n"+
		"\"github.checks\" = \"github/checks/<key>\"\n\"github.outbox\" = \"github/outbox/<key>\"\n", command)
	if err := os.WriteFile(filepath.Join(root, index.WiringFile), []byte(wiring), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"PYTHONPATH=" + sdk(t), "PYTHONDONTWRITEBYTECODE=1", cli.ReplyDefaultEnv + "=" + replyDefault}
	want(t, run(t, env, "start", "--root", root), 0, "kern: started")
	want(t, run(t, nil, "status", "--root", root, "--wait", wait), 0, "kern: running")
	s := standing(t, root)
	client, err := bus.Dial(s.Bus, bus.Client, s.Users[bus.Client])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)

	type answered struct {
		a   host.CallAnswer
		err error
	}
	done := make(chan answered, 1)
	go func() {
		m, err := client.Request(bus.Call+bus.Token("gap run"), []byte(`{"args":["`+gap+`"]}`), time.Minute)
		var a host.CallAnswer
		if err == nil {
			err = json.Unmarshal(m.Data, &a)
		}
		done <- answered{a, err}
	}()
	call := regexp.MustCompile(`(?m)^\d+\. (\S+) (\{.*\})$`)
	var ran []string
	for {
		select {
		case got := <-done:
			if got.err != nil || got.a.Error != "" || !strings.Contains(got.a.Text, "heard list_issues after "+gap+" s") {
				t.Fatalf("gap run answers %+v (%v) after the steps %q; the step after the long phase wants to reach the hook", got.a, got.err, ran)
			}
			if strings.Join(ran, " ") != "issue_read list_issues" {
				t.Fatalf("the host ran %q, and wants issue_read, then list_issues", ran)
			}
			return
		case <-time.After(10 * time.Millisecond):
		}
		var told struct {
			Out struct {
				Context string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if out := hookOut(t, root, "UserPromptSubmit", `{"session_id":"s1"}`); strings.TrimSpace(out) != "" {
			if err := json.Unmarshal([]byte(out), &told); err != nil {
				t.Fatalf("the hook prints %q: %v", out, err)
			}
		}
		for _, m := range call.FindAllStringSubmatch(told.Out.Context, -1) {
			var input map[string]any
			if err := json.Unmarshal([]byte(m[2]), &input); err != nil {
				t.Fatal(err)
			}
			data := `{"number": 7, "title": "One issue"}`
			if m[1] == "list_issues" {
				data = `{"issues": [], "pageInfo": {"hasNextPage": false}}`
			}
			payload, _ := json.Marshal(map[string]any{
				"session_id": "s1", "hook_event_name": "PostToolUse", "tool_name": "mcp__github__" + m[1],
				"tool_input": input, "tool_response": []map[string]string{{"type": "text", "text": data}}})
			hookOut(t, root, "PostToolUse", string(payload))
			ran = append(ran, m[1])
		}
	}
}

// hookOut runs `kern hook <event>` in root with stdin and answers what it printed.
func hookOut(t *testing.T, root, event, stdin string) string {
	t.Helper()
	cmd := exec.Command(kern, "hook", event, "--root", root)
	cmd.Dir, cmd.Stdin = root, strings.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("kern hook %s: %v", event, err)
	}
	return string(out)
}
