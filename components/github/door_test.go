package github_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"kern/components/github"
	"kern/host"
	"kern/index"
	"kern/mcp"
)

// report is a template as an application's engine generates it: its fields'
// shapes in order and their JSON Schema, with the marker its body opens with.
const report = `{
  "form": "report",
  "says": "A report of one finding.",
  "markers": ["<!-- report -->"],
  "actions": ["issue.create"],
  "fields": {"title": "", "situation": "", "requirements": [], "kind": ""},
  "field_shape": {
    "title": {"kind": "line", "required": true, "source": "title"},
    "situation": {"kind": "text", "heading": "Situation", "required": true, "max_words": 20},
    "requirements": {"kind": "list", "heading": "Requirements", "required": true, "numbered": true},
    "kind": {"kind": "choice", "heading": "Kind", "choices": ["standard", "patch"]}
  },
  "schema": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "properties": {
      "title": {"type": "string", "description": "The name."},
      "situation": {"type": "string", "description": "What is true today."},
      "requirements": {"type": "array", "items": {"type": "string"}, "description": "What it must do."},
      "kind": {"type": "string", "enum": ["standard", "patch"], "description": "Its kind."}
    },
    "required": ["title", "situation", "requirements"],
    "additionalProperties": false
  }
}`

// templates writes the templates into a folder of their own and answers it.
func templates(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// door serves the MCP door against the real core, which carries g, and
// answers the SDK's own client connected to it.
func door(t *testing.T, g *github.GitHub, w index.Wiring) *sdk.ClientSession {
	t.Helper()
	c, err := host.Serve(host.Options{Root: t.TempDir(), Wiring: w, Components: []*index.Component{g.Component}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Stop("the case ends"); <-c.Ended() })
	s, _ := c.Standing()
	catalog, err := index.Collect(w, g.Component)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	served := make(chan error, 1)
	go func() {
		served <- mcp.Serve(ctx, mcp.Options{App: "kern", Version: "0", Root: s.Root, Checkout: true, Catalog: catalog,
			Core: mcp.Host{Root: s.Root}, Session: "s1", StartWait: 10 * time.Second, Renew: time.Hour}, inR, outW)
	}()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, &sdk.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cs.Close()
		inW.Close()
		cancel()
		<-served
	})
	return cs
}

func callText(r *sdk.CallToolResult) string {
	var parts []string
	for _, c := range r.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// The acceptance of the door's writing tools: through the door and the real
// core, the GitHub component's writing tool files an issue from a filled
// template against the fake REST server, and the tool list marks the tool as
// writing. A fill the template refuses, and free text, reach GitHub not at all.
func TestTheDoorFilesAnIssueFromAFilledTemplate(t *testing.T) {
	t.Parallel()
	f, srv := newFake(t)
	f.created["/repos/alice/demo/issues"] = `{"number": 12, "html_url": "https://github.com/alice/demo/issues/12", "title": "door-files"}`
	dir := templates(t, map[string]string{"report": report})
	g := github.New(github.Options{Clock: &clock{now: nine}, Token: token(""), Cloud: notCloud})
	w, err := index.ParseWiring([]byte(`
[instances.github]
component = "github"
config = { backend = "token", api = "` + srv.URL + `", templates = '` + filepath.ToSlash(dir) + `' }

[wires]
"github.second" = "built-in"
`))
	if err != nil {
		t.Fatal(err)
	}
	cs := door(t, g, w)
	ctx := context.Background()

	listed, err := cs.ListTools(ctx, &sdk.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	var write *sdk.Tool
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		if tool.Name == "github_write" {
			write = tool
		}
	}
	if write == nil || strings.Join(names, " ") != "github_write status" {
		t.Fatalf("tools/list answers %v, and wants github_write beside status", names)
	}
	a := write.Annotations
	if a == nil || a.ReadOnlyHint || a.DestructiveHint == nil || !*a.DestructiveHint || a.IdempotentHint {
		t.Fatalf("github_write carries %+v, and wants it marked as writing: not read-only, destructive, not idempotent", a)
	}
	if !strings.Contains(write.Description, "`github issue-create`: Files a GitHub issue from a filled template, never free text") ||
		!strings.Contains(write.Description, "`github issue-comment`: Comments on a GitHub issue") {
		t.Fatalf("github_write's description is %q, and wants both writing commands", write.Description)
	}

	call := func(args ...string) *sdk.CallToolResult {
		t.Helper()
		r, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "github_write", Arguments: map[string]any{"command": "github issue-create", "args": args}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := call("--repo", "alice/demo", "--template", "report", "--label", "work",
		"--fields", `{"title": "door-files", "situation": "The door files.", "requirements": ["File it.", "Comment on it."], "kind": "patch"}`)
	if r.IsError || callText(r) != "filed alice/demo#12: https://github.com/alice/demo/issues/12" {
		t.Fatalf("github_write answers %q (error %v), and wants issue 12 filed", callText(r), r.IsError)
	}
	f.mu.Lock()
	posted := append([]string{}, f.posted...)
	f.mu.Unlock()
	var sent struct {
		Title, Body string
		Labels      []string
	}
	if len(posted) == 1 {
		path, body, _ := strings.Cut(posted[0], " ")
		if err := json.Unmarshal([]byte(body), &sent); err != nil || path != "/repos/alice/demo/issues" {
			t.Fatalf("GitHub was sent %q (%v), and wants the issue at /repos/alice/demo/issues", posted[0], err)
		}
	}
	body := "<!-- report -->\n\n## Situation\n\nThe door files.\n\n## Requirements\n\n1. File it.\n2. Comment on it.\n\n## Kind\n\npatch\n"
	if len(posted) != 1 || sent.Title != "door-files" || sent.Body != body || strings.Join(sent.Labels, ",") != "work" {
		t.Fatalf("GitHub was sent %q, and wants the one issue the template renders, titled door-files, labelled work, its body:\n%s", posted, body)
	}

	r = call("--repo", "alice/demo", "--template", "report", "--fields", `{"title": "door-files", "requirements": [], "kind": "big", "body": "free text"}`)
	text := callText(r)
	for _, finding := range []string{"template report refuses the fill, and nothing reached GitHub", "fields.body is no field of the template",
		"fields.kind is big, none of [standard patch]", "field situation is required: What is true today.", "field requirements is required: What it must do."} {
		if !r.IsError || !strings.Contains(text, finding) {
			t.Fatalf("a refused fill answers %q (error %v), and wants the finding %q", text, r.IsError, finding)
		}
	}
	r = call("--repo", "alice/demo", "--template", "report", "--body", "free text")
	if !r.IsError || !strings.Contains(callText(r), "-body") {
		t.Fatalf("free text answers %q (error %v), and wants it refused", callText(r), r.IsError)
	}
	r = call("--repo", "alice/demo", "--template", "absent", "--fields", `{}`)
	if !r.IsError || !strings.Contains(callText(r), "no template absent") {
		t.Fatalf("an absent template answers %q (error %v), and wants it named", callText(r), r.IsError)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.posted) != 1 {
		t.Fatalf("GitHub was sent %q, and wants nothing after the one issue: a refused fill reaches it not at all", f.posted)
	}
}
