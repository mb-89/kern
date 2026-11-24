package github_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"kern/components/github"
	"kern/host"
	"kern/index"
	"kern/mcp"
)

// door serves the MCP door against the real core, which carries g, and
// answers the SDK's own client connected to it.
func door(t *testing.T, w index.Wiring, comps ...*index.Component) *sdk.ClientSession {
	t.Helper()
	c, err := host.Serve(host.Options{Root: t.TempDir(), Wiring: w, Components: comps})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Stop("the case ends"); <-c.Ended() })
	s, _ := c.Standing()
	catalog, err := index.Collect(w, comps...)
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
// template the application's template component accepts, against the fake
// REST server, and the tool list marks the tool as writing. A value the
// template component refuses, and free text, reach GitHub not at all.
func TestTheDoorFilesAnIssueFromAFilledTemplate(t *testing.T) {
	t.Parallel()
	f, srv := newFake(t)
	f.created["/repos/alice/demo/issues"] = `{"number": 12, "html_url": "https://github.com/alice/demo/issues/12", "title": "door-files"}`
	g := github.New(github.Options{Proxy: noProxy, Clock: &clock{now: nine}, Token: token(""), Cloud: notCloud})
	accept := &acceptor{}
	w, err := index.ParseWiring([]byte(`
[instances.github]
component = "github"
config = { backend = "token", api = "` + srv.URL + `" }

[instances.template]
component = "template"

[wires]
"github.second" = "built-in"
"github.template-accept" = "template.accept"
`))
	if err != nil {
		t.Fatal(err)
	}
	cs := door(t, w, g.Component, accept.component())
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
	r := call("--repo", "alice/demo", "--label", "work", "--filled", jsonOf(t, fillOf("report", "door-files", odd)))
	if r.IsError || callText(r) != "filed alice/demo#12: https://github.com/alice/demo/issues/12" {
		t.Fatalf("github_write answers %q (error %v), and wants issue 12 filed", callText(r), r.IsError)
	}
	paths, sent := posts(t, f)
	if len(sent) != 1 || paths[0] != "/repos/alice/demo/issues" || sent[0]["title"] != "door-files" || sent[0]["body"] != odd || jsonOf(t, sent[0]["labels"]) != `["work"]` {
		t.Fatalf("GitHub was sent %v %v, and wants the one issue the template component accepted, titled door-files, labelled work, its document byte for byte:\n%q", paths, sent, odd)
	}

	r = call("--repo", "alice/demo", "--filled", `"free text"`)
	if text := callText(r); !r.IsError || !strings.Contains(text, "github issue-create takes a filled template, not free text: fill template report") || !strings.Contains(text, "nothing reached GitHub") {
		t.Fatalf("free text as the filled value answers %q (error %v), and wants accept's refusal", text, r.IsError)
	}
	r = call("--repo", "alice/demo", "--body", "free text")
	if !r.IsError || !strings.Contains(callText(r), "-body") {
		t.Fatalf("free text answers %q (error %v), and wants it refused", callText(r), r.IsError)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.posted) != 1 {
		t.Fatalf("GitHub was sent %q, and wants nothing after the one issue: a refused value reaches it not at all", f.posted)
	}
}
