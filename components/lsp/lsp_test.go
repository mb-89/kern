package lsp_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/textproto"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"kern/cli"
	"kern/components/files"
	"kern/components/lsp"
	"kern/components/lsp/lsptest"
	"kern/host"
	"kern/index"
	"kern/kerntest"
	"kern/lint"
	kernlsp "kern/lsp"
	"kern/mcp"
	"kern/waiting"
)

// wiring places the files module over docs and the checker beside it.
const wiring = `
[instances.files]
component = "files"
config = { folders = ["docs"], cookie-wait = "30s" }

[instances.lsp]
component = "lsp"
config = { templates = "docs/templates" }

[wires]
"files.model" = "files/model/<key>"
"files.watching" = "files/watching"
"files.folders" = "files/folders"
"lsp.model" = "files/model/<key>"
"lsp.folders" = "files/folders"
"lsp.findings" = "lsp/findings"
`

// note is a template: a marker, a choice and a required text.
const note = `{"form": "note", "markers": ["<!-- se:form note -->"],
 "field_shape": {"kind": {"kind": "choice", "heading": "Kind", "required": true, "choices": ["idea", "fact"]},
                 "gist": {"kind": "text", "heading": "Gist", "required": true}},
 "schema": {"type": "object", "properties": {"kind": {"type": "string"}, "gist": {"type": "string"}}}}
`

// broken breaks its template: its gist is missing and its kind is no choice.
const broken = "<!-- se:form note -->\n\n## Kind\n\nmaybe\n"

// fixed is the same file as it conforms.
const fixed = "<!-- se:form note -->\n\n## Kind\n\nidea\n\n## Gist\n\nIt holds.\n"

// checkout is a checkout with the wiring, the template, a file that breaks
// it and one with a dead link.
func checkout(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for p, content := range map[string]string{
		".git/HEAD":                "ref: refs/heads/main\n",
		index.WiringFile:           wiring,
		"docs/templates/note.json": note,
		"docs/a.md":                broken,
		"docs/b.md":                "# B\n\nSee [a](a.md) and [gone](gone.md).\n",
	} {
		put(t, root, p, content)
	}
	return root
}

func put(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// serve runs the core on the checkout's wiring, with the operating system's
// watcher, until the case ends.
func serve(t *testing.T, root string) {
	t.Helper()
	w, err := index.ReadWiring(filepath.Join(root, index.WiringFile))
	if err != nil {
		t.Fatal(err)
	}
	fc := files.New(files.Options{Root: root, Clock: host.Wall{}})
	core, err := host.Serve(host.Options{Root: root, Wiring: w, Components: []*index.Component{fc.Component, lsp.Component}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		core.Stop("the case ended")
		<-core.Ended()
		fc.Stop()
	})
}

// client is the editor's side of a language server served in the case.
type client struct {
	t   *testing.T
	in  io.WriteCloser
	out *bufio.Reader
	id  int
	// diags are the diagnostics each publish gave, by path from the root.
	got chan map[string]any
}

func connect(t *testing.T, root string) *client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan error, 1)
	go func() {
		ended <- kernlsp.Serve(ctx, kernlsp.Options{
			App: "kern", Root: root, Findings: "lsp/findings", NoStart: "the case runs its core",
			Core: kernlsp.Host{Host: mcp.Host{Root: root}},
		}, inR, outW)
		outW.Close()
	}()
	c := &client{t: t, in: inW, out: bufio.NewReader(outR), got: make(chan map[string]any, 64)}
	go c.listen()
	t.Cleanup(func() {
		cancel()
		inW.Close()
		if err := <-ended; err != nil {
			t.Errorf("the server ended with %v", err)
		}
	})
	return c
}

// send writes one message to the server.
func (c *client) send(method string, params any, request bool) {
	c.t.Helper()
	m := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if request {
		c.id++
		m["id"] = c.id
	}
	data, _ := json.Marshal(m)
	fmt.Fprintf(c.in, "Content-Length: %d\r\n\r\n%s", len(data), data)
}

// listen reads the server's messages and hands on each publish and each reply.
func (c *client) listen() {
	tp := textproto.NewReader(c.out)
	for {
		h, err := tp.ReadMIMEHeader()
		if err != nil {
			close(c.got)
			return
		}
		n, _ := strconv.Atoi(h.Get("Content-Length"))
		body := make([]byte, n)
		if _, err := io.ReadFull(c.out, body); err != nil {
			close(c.got)
			return
		}
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		c.got <- m
	}
}

// next answers the next message the server sends whose method is method
// ("" a reply); the deadline bounds a message that comes at once.
func (c *client) next(method string) map[string]any {
	c.t.Helper()
	deadline := waiting.Fails()
	for {
		select {
		case m, ok := <-c.got:
			if !ok {
				c.t.Fatalf("the server hung up before %q", method)
			}
			if got, _ := m["method"].(string); got == method {
				return m
			}
		case <-deadline:
			c.t.Fatalf("no %q from the server", method)
		}
	}
}

// diagnostics reads publishes until every path in want was published, and
// answers the last diagnostics of each path published.
func (c *client) diagnostics(root string, want ...string) map[string][]any {
	c.t.Helper()
	got := map[string][]any{}
	for {
		missing := false
		for _, p := range want {
			_, ok := got[p]
			missing = missing || !ok
		}
		if !missing {
			return got
		}
		m := c.next("textDocument/publishDiagnostics")
		params := m["params"].(map[string]any)
		got[pathOf(c.t, root, params["uri"].(string))] = params["diagnostics"].([]any)
	}
}

func uriOf(root, rel string) string {
	abs := filepath.ToSlash(filepath.Join(root, filepath.FromSlash(rel)))
	if !strings.HasPrefix(abs, "/") {
		abs = "/" + abs
	}
	return "file://" + abs
}

func pathOf(t *testing.T, root, uri string) string {
	t.Helper()
	for _, rel := range []string{"docs/a.md", "docs/b.md", "docs/c.md", "docs/gen.toml"} {
		if uriOf(root, rel) == uri {
			return rel
		}
	}
	t.Fatalf("a publish for %s, no file of the case", uri)
	return ""
}

// asDiagnostics are the findings as the server publishes them, by path, as JSON reads them.
func asDiagnostics(t *testing.T, found []lint.Finding) map[string][]any {
	t.Helper()
	by := map[string][]any{}
	for _, f := range found {
		data, _ := json.Marshal(kernlsp.Diagnostic(f))
		var d any
		_ = json.Unmarshal(data, &d)
		by[f.Path] = append(by[f.Path], d)
	}
	return by
}

// The language server, `lint` and the tests ask one checker: over the same
// files each returns the same findings, a file that breaks its template and
// a dead link among them.
func TestTheThreeRoadsFindTheSame(t *testing.T) {
	root := checkout(t)

	// The tests' road: lsptest runs the checkout's wiring on an index of its own.
	tests, err := lsptest.Findings(root)
	if err != nil {
		t.Fatal(err)
	}
	var shown []string
	for _, f := range tests {
		shown = append(shown, f.String())
	}
	want := []string{
		`docs/a.md:1:1: template: template note: field gist is required (its heading "## Gist" is missing)`,
		`docs/a.md:3:1: template: template note: field kind is "maybe", none of idea, fact`,
		`docs/b.md:3:26: link: the link to gone.md reaches nothing: docs/gone.md stands nowhere among the managed files`,
	}
	if strings.Join(shown, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the tests' road finds:\n%s\nwant:\n%s", strings.Join(shown, "\n"), strings.Join(want, "\n"))
	}
	if _, err := os.Stat(filepath.Join(root, "docs", files.Own+"cookie")); !os.IsNotExist(err) {
		t.Fatalf("the tests' road leaves its cookie: %v", err)
	}

	// The command line's road: `kern lint` asks the core the checkout runs.
	serve(t, root)
	var out, errs bytes.Buffer
	if code := cli.Run("kern", []string{"lint", "--root", root, "--json"}, &out, &errs); code != 1 {
		t.Fatalf("kern lint exits %d, and 1 where there is a finding; it said %s%s", code, out.String(), errs.String())
	}
	var linted lsp.Answer
	if err := json.Unmarshal(out.Bytes(), &linted); err != nil {
		t.Fatalf("kern lint --json prints %s: %v", out.String(), err)
	}
	if !reflect.DeepEqual(linted.Findings, tests) || linted.Checked != 3 {
		t.Fatalf("kern lint finds %+v over %d files, and the tests %+v", linted.Findings, linted.Checked, tests)
	}
	out.Reset()
	errs.Reset()
	cli.Run("kern", []string{"lint", "--root", root}, &out, &errs)
	if strings.TrimSpace(out.String()) != strings.Join(want, "\n") || !strings.Contains(errs.String(), "3 findings in 3 managed files") {
		t.Fatalf("kern lint prints:\n%s\nand says %s", out.String(), errs.String())
	}

	// The language server's road: the editor is shown each finding as a diagnostic.
	c := connect(t, root)
	c.send("initialize", map[string]any{"processId": nil, "rootUri": uriOf(root, ""), "capabilities": map[string]any{}}, true)
	init := c.next("")
	if caps, _ := init["result"].(map[string]any)["capabilities"].(map[string]any); caps["textDocumentSync"] == nil {
		t.Fatalf("initialize answers %v", init)
	}
	c.send("initialized", map[string]any{}, false)
	if got, want := c.diagnostics(root, "docs/a.md", "docs/b.md"), asDiagnostics(t, tests); !reflect.DeepEqual(got, want) {
		t.Fatalf("the language server publishes %v, and the tests find %v", got, want)
	}

	// Typing: the buffer the editor holds is checked in place of the file.
	c.send("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": uriOf(root, "docs/a.md"), "languageId": "markdown", "version": 1, "text": broken}}, false)
	c.send("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uriOf(root, "docs/a.md"), "version": 2},
		"contentChanges": []any{map[string]any{"text": fixed}}}, false)
	for {
		if got := c.diagnostics(root, "docs/a.md"); len(got["docs/a.md"]) == 0 {
			break
		}
	}

	// A change on the disk the model heard: a new file's finding is shown.
	put(t, root, "docs/c.md", "[x](nowhere.md)\n")
	for {
		if got := c.diagnostics(root, "docs/c.md"); len(got["docs/c.md"]) == 1 {
			break
		}
	}

	// Closing the buffer shows the file as the disk holds it again.
	c.send("textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": uriOf(root, "docs/a.md")}}, false)
	for {
		if got := c.diagnostics(root, "docs/a.md"); len(got["docs/a.md"]) == 2 {
			break
		}
	}
	c.send("shutdown", nil, true)
	if m := c.next(""); m["error"] != nil {
		t.Fatalf("shutdown answers %v", m)
	}
	c.send("exit", nil, false)
}

// The checker's command reads the open buffers it is handed in place of the
// files the model holds, and refuses an argument that is none.
func TestTheCommandReadsTheOpenBuffers(t *testing.T) {
	t.Parallel()
	f, err := kerntest.Expecting(lsp.Component, "expect.toml")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Given("a file breaks its template"); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]any{{"templates", "docs/templates"}} {
		if err := f.Config(kv[0].(string), kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	answer := func(args ...string) lsp.Answer {
		t.Helper()
		text, err := f.Call(lsp.Command, args...)
		if err != nil {
			t.Fatal(err)
		}
		var a lsp.Answer
		if err := json.Unmarshal([]byte(text), &a); err != nil {
			t.Fatalf("%s answers %s: %v", lsp.Command, text, err)
		}
		return a
	}
	if a := answer(); len(a.Findings) != 2 || a.Checked != 2 {
		t.Fatalf("over the model: %+v", a)
	}
	open, _ := json.Marshal(map[string]string{"docs/a.md": fixed, "docs/new.md": "[x](gone.md)\n", "elsewhere/x.md": "[x](gone.md)\n"})
	a := answer(string(open))
	if len(a.Findings) != 1 || a.Findings[0].Path != "docs/new.md" || a.Checked != 3 {
		t.Fatalf("with the open buffers: %+v", a)
	}
	if _, err := f.Call(lsp.Command, "not json"); err == nil || !strings.Contains(err.Error(), "the open buffers, a JSON object") {
		t.Fatalf("an argument that is no JSON: %v", err)
	}
	if err := f.Derive("findings"); err != nil {
		t.Fatal(err)
	}
	if v, _ := f.Out("findings"); len(v.V.([]lint.Finding)) != 2 {
		t.Fatalf("the derived findings: %v", v.V)
	}
}

// await answers the next message the server sends whose method is method
// ("" a reply). It waits on the messages alone, never on the clock: a case
// that hangs is ended by go test's own timeout.
func (c *client) await(method string) map[string]any {
	c.t.Helper()
	for m := range c.got {
		if got, _ := m["method"].(string); got == method {
			return m
		}
	}
	c.t.Fatalf("the server hung up before %q", method)
	return nil
}

// awaitDiagnostics reads publishes, as await does, until one for path holds
// diagnostics that ok accepts, and answers them.
func (c *client) awaitDiagnostics(root, path string, ok func([]any) bool) []any {
	c.t.Helper()
	for {
		params := c.await("textDocument/publishDiagnostics")["params"].(map[string]any)
		if got := params["diagnostics"].([]any); pathOf(c.t, root, params["uri"].(string)) == path && ok(got) {
			return got
		}
	}
}

// generatedToml is a file a program wrote, its marker on its first line.
const generatedToml = "# Code generated by gen.py write from docs/src.txt. DO NOT EDIT.\nx = 1\n"

// The notice each road shows on a generated file.
const generatedNotice = "generated by gen.py write from docs/src.txt; edit docs/src.txt"

// A generated file shows its generator and its source on each road, as
// information, never as an error: the tests and `lint` find its notice and
// fail on none, and the language server shows it at once, and on a buffer
// that gains a marker as it is typed, and drops it where the marker goes.
func TestAGeneratedFileShowsItsGeneratorAndSource(t *testing.T) {
	root := t.TempDir()
	for p, content := range map[string]string{
		".git/HEAD":      "ref: refs/heads/main\n",
		index.WiringFile: wiring,
		"docs/gen.toml":  generatedToml,
		"docs/c.md":      "# By hand\n",
	} {
		put(t, root, p, content)
	}

	// The tests' road: the notice on the generated file, none on the hand-written one, no error.
	tests, err := lsptest.Findings(root)
	if err != nil {
		t.Fatal(err)
	}
	want := "docs/gen.toml:1:1: generated notice: " + generatedNotice
	if len(tests) != 1 || tests[0].String() != want || len(lint.Errors(tests)) != 0 {
		t.Fatalf("the tests' road finds %v, and wants only %s", tests, want)
	}

	// The command line's road: the notice is printed, and lint exits 0 on notices alone.
	serve(t, root)
	var out, errs bytes.Buffer
	if code := cli.Run("kern", []string{"lint", "--root", root}, &out, &errs); code != 0 {
		t.Fatalf("kern lint exits %d over notices alone; it said %s%s", code, out.String(), errs.String())
	}
	if strings.TrimSpace(out.String()) != want || !strings.Contains(errs.String(), "0 findings and 1 notices in 2 managed files") {
		t.Fatalf("kern lint prints:\n%s\nand says %s", out.String(), errs.String())
	}

	// The language server's road: information on the generated file, published at once.
	c := connect(t, root)
	c.send("initialize", map[string]any{"processId": nil, "rootUri": uriOf(root, ""), "capabilities": map[string]any{}}, true)
	c.await("")
	c.send("initialized", map[string]any{}, false)
	shown := c.awaitDiagnostics(root, "docs/gen.toml", func([]any) bool { return true })
	if len(shown) != 1 {
		t.Fatalf("the generated file shows %v", shown)
	}
	d := shown[0].(map[string]any)
	if d["message"] != generatedNotice || d["severity"] != float64(kernlsp.SeverityInformation) || d["code"] != lint.RuleGenerated {
		t.Fatalf("the generated file's diagnostic is %v", d)
	}

	// Opening a file and typing a marker into it shows the notice; removing it drops it.
	marked := "<!-- Code generated by notes.py from docs/notes.yaml. DO NOT EDIT. -->\n# By hand\n"
	c.send("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": uriOf(root, "docs/c.md"), "languageId": "markdown", "version": 1, "text": marked}}, false)
	c.awaitDiagnostics(root, "docs/c.md", func(got []any) bool {
		return len(got) == 1 && got[0].(map[string]any)["message"] == "generated by notes.py from docs/notes.yaml; edit docs/notes.yaml"
	})
	c.send("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uriOf(root, "docs/c.md"), "version": 2},
		"contentChanges": []any{map[string]any{"text": "# By hand\n"}}}, false)
	c.awaitDiagnostics(root, "docs/c.md", func(got []any) bool { return len(got) == 0 })
	c.send("shutdown", nil, true)
	c.await("")
	c.send("exit", nil, false)
}
