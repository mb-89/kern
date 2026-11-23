package github_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"kern/components/github"
	"kern/filled"
	"kern/index"
)

// sealed is the seal the fake template component's fill makes: a value of the
// filled template's shape without it is free text, as a value the real
// template component did not make is.
const sealed = "made-by-fill"

// odd is a document whose every byte must reach GitHub as it stands: a marker,
// a carriage return, trailing blanks, a tab, non-ASCII text and no final newline.
const odd = "<!-- report -->\r\n\n## Situation\n\nThe door files.  \n\tä😀 — kept as it stands"

// fillOf is a filled template of template, as the fake template component's
// fill answers it, JSON written by its fill alone.
func fillOf(template, title, document string) map[string]any {
	v := map[string]any{"template": template, "document": document, "seal": sealed}
	if title != "" {
		v["title"] = title
	}
	return v
}

// acceptor is the fake of an application's template component: its verb
// accept answers the title and the document of a filled template its fill
// made, and refuses anything else, naming the template to fill, as SE's
// template component does. It records each request it was asked.
type acceptor struct {
	mu    sync.Mutex
	asked []filled.Accept
	// answer, where set, is what accept answers in place of the value's own.
	answer *filled.Accepted
}

func (a *acceptor) component() *index.Component {
	return index.Declare("template", "The application's template component, faked.", index.IO(),
		index.JSONVerb("accept", "Accepts a filled template its fill made.", filled.AcceptShape, filled.AcceptedShape,
			func(_ context.Context, req filled.Accept) (filled.Accepted, error) {
				a.mu.Lock()
				a.asked = append(a.asked, req)
				answer := a.answer
				a.mu.Unlock()
				m, _ := req.Filled.(map[string]any)
				name, _ := m["template"].(string)
				if m["seal"] != sealed {
					if name == "" {
						name = "report"
					}
					return filled.Accepted{}, &refusal{req.Target + " takes a filled template, not free text: fill template " + name + " with the template component's fill"}
				}
				if answer != nil {
					return *answer, nil
				}
				out := filled.Accepted{Template: name, Version: "1", Document: m["document"].(string)}
				if title, ok := m["title"].(string); ok {
					out.Title = &title
				}
				return out, nil
			}),
	)
}

func (a *acceptor) requests() []filled.Accept {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]filled.Accept{}, a.asked...)
}

type refusal struct{ s string }

func (r *refusal) Error() string { return r.s }

// writing starts the component with config, beside the fake template
// component where accept is one, its port template-accept bound to that
// component's verb accept, and beside the writer where it is one of more.
func writing(t *testing.T, o github.Options, config string, accept *acceptor, more ...*index.Component) (*github.GitHub, *index.Index) {
	t.Helper()
	g := github.New(o)
	comps := append([]*index.Component{g.Component}, more...)
	text := "[instances.github]\ncomponent = \"github\"\nconfig = { " + config + " }\n"
	wires := "\n[wires]\n\"github.second\" = \"built-in\"\n"
	if accept != nil {
		comps = append(comps, accept.component())
		text += "\n[instances.template]\ncomponent = \"template\"\n"
		wires += "\"github.template-accept\" = \"template.accept\"\n"
	}
	if len(more) > 0 {
		text += "\n[instances.writer]\ncomponent = \"writer\"\n"
		wires += "\"writer.create\" = \"github.issue-create\"\n"
	}
	w, err := index.ParseWiring([]byte(text + wires))
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.Start(w, comps...)
	if err != nil {
		t.Fatal(err)
	}
	return g, ix
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// posts reads what the fake GitHub was sent: each path and its JSON payload.
func posts(t *testing.T, f *fakeGitHub) (paths []string, sent []map[string]any) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.posted {
		path, body, _ := strings.Cut(p, " ")
		var m map[string]any
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatalf("GitHub was sent %q, no JSON: %v", p, err)
		}
		paths, sent = append(paths, path), append(sent, m)
	}
	return paths, sent
}

// A comment posts the document the template component accepted, byte for
// byte: the component asks accept with its target and the value as it was
// handed, and takes the answer as it stands.
func TestACommentPostsTheAcceptedDocumentAsItStands(t *testing.T) {
	t.Parallel()
	f, srv := newFake(t)
	f.created["/repos/alice/demo/issues/7/comments"] = `{"id": 99, "html_url": "https://github.com/alice/demo/issues/7#issuecomment-99"}`
	accept := &acceptor{}
	_, ix := writing(t, github.Options{Clock: &clock{now: nine}, Token: token("")}, `backend = "token", api = "`+srv.URL+`"`, accept)
	value := fillOf("note", "", odd)
	r, err := ix.Call("github issue-comment", "--repo", "alice/demo", "--number", "7", "--filled", jsonOf(t, value))
	if err != nil || r.Text != "commented on alice/demo#7: https://github.com/alice/demo/issues/7#issuecomment-99" {
		t.Fatalf("github issue-comment answers %q, %v, and wants the comment linked", r.Text, err)
	}
	paths, sent := posts(t, f)
	if len(sent) != 1 || paths[0] != "/repos/alice/demo/issues/7/comments" || sent[0]["body"] != odd || len(sent[0]) != 1 {
		t.Fatalf("GitHub was sent %v %v, and wants the accepted document alone, byte for byte:\n%q", paths, sent, odd)
	}
	asked := accept.requests()
	if len(asked) != 1 || asked[0].Target != "github issue-comment" || asked[0].Template != "" || asked[0].Path != "" || jsonOf(t, asked[0].Filled) != jsonOf(t, value) {
		t.Fatalf("accept was asked %+v, and wants the target github issue-comment and the value as it was handed", asked)
	}
}

// Through the relay, the write is a step of the session's own GitHub tool:
// issue_write with method create and the accepted title and document; its
// answer names the issue filed.
func TestTheRelayFilesThroughTheSessionsTool(t *testing.T) {
	t.Parallel()
	g, ix := writing(t, github.Options{Clock: &clock{now: nine}, Token: noToken, Cloud: notCloud}, `step-wait = "10s"`, &acceptor{})
	body := "<!-- report -->\n\n## Situation\n\nThe relay files.\n"
	done := make(chan called, 1)
	go func() {
		r, err := ix.Call("github issue-create", "--repo", "alice/demo", "--filled", jsonOf(t, fillOf("report", "relay-files", body)))
		done <- called{r, err}
	}()
	told := waitOutbox(t, g, "issue_write")
	if !strings.Contains(told, `"method":"create"`) || !strings.Contains(told, `"title":"relay-files"`) || !strings.Contains(told, `file the issue "relay-files" in alice/demo, from template report`) {
		t.Fatalf("the relay tells %q, and wants issue_write create with the accepted title", told)
	}
	if err := ix.Hear(github.PostToolUse, toolUse("mcp__github__issue_write",
		map[string]any{"method": "create", "owner": "alice", "repo": "demo", "title": "relay-files", "body": body},
		`{"id": "4021", "url": "https://github.com/alice/demo/issues/21"}`)); err != nil {
		t.Fatal(err)
	}
	c := <-done
	if c.err != nil || c.r.Text != "filed alice/demo#21: https://github.com/alice/demo/issues/21" {
		t.Fatalf("github issue-create through the relay answers %q, %v, and wants issue 21", c.r.Text, c.err)
	}
}

// writer is another component, which asks the verb issue-create in JSON,
// as a component in another language does: its command sends what it is given.
var writer = func() *index.Component {
	create := index.JSONRequest("create", "Files an issue.", github.IssueCreateShape, github.WrittenShape)
	return index.Declare("writer", "Files through the github component.", create,
		index.Command(index.CommandDecl{Name: "write", Usage: "write JSON", Summary: "Write one.", Help: "Sends it.", Group: "Fixtures", Class: index.Batch, Writes: true, MCP: "Writes."},
			func(_ struct{}, args []string) (index.Result, error) {
				var ask any
				if err := json.Unmarshal([]byte(args[0]), &ask); err != nil {
					return index.Result{}, err
				}
				return index.Result{Requests: []index.Ask{create.Ask(ask)}}, nil
			}),
	)
}()

// The acceptance: the verb itself, whoever asks it, takes no free text. A
// request with a body, a title, a template's name or its fields is refused
// before the template component is asked; free text as the filled value, and
// a value of its shape the template component's fill did not make, are
// refused by accept, naming the template to fill, before any GitHub call. The
// accepted title and document are filed byte for byte.
func TestAWriteVerbTakesNoFreeText(t *testing.T) {
	t.Parallel()
	f, srv := newFake(t)
	f.created["/repos/alice/demo/issues"] = `{"number": 1, "html_url": "https://github.com/alice/demo/issues/1"}`
	accept := &acceptor{}
	_, ix := writing(t, github.Options{Clock: &clock{now: nine}, Token: token("")}, `backend = "token", api = "`+srv.URL+`"`, accept, writer)
	for _, key := range []string{"body", "title", "template", "fields"} {
		_, err := ix.Call("write", `{"owner": "alice", "repo": "demo", "filled": "free text", "`+key+`": "free text"}`)
		if err == nil || !strings.Contains(err.Error(), `unknown field "`+key+`"`) || !strings.Contains(err.Error(), "never free text") {
			t.Fatalf("a request with a %s answers %v, and wants it refused as free text", key, err)
		}
	}
	if asked := accept.requests(); len(asked) != 0 {
		t.Fatalf("accept was asked %+v, and wants a request with free text keys refused before", asked)
	}
	unsealed := fillOf("report", "t", "a body of its shape")
	delete(unsealed, "seal")
	for _, value := range []any{"free text", map[string]any{"title": "t", "body": "free text"}, unsealed, nil} {
		_, err := ix.Call("write", jsonOf(t, map[string]any{"owner": "alice", "repo": "demo", "filled": value}))
		if err == nil || !strings.Contains(err.Error(), "github issue-create takes a filled template, not free text: fill template report") || !strings.Contains(err.Error(), "nothing reached GitHub") {
			t.Fatalf("the filled value %v answers %v, and wants accept's refusal naming the template to fill", value, err)
		}
	}
	if paths, _ := posts(t, f); len(paths) != 0 {
		t.Fatalf("GitHub was sent %v, and wants nothing: a refused value reaches it not at all", paths)
	}
	r, err := ix.Call("github issue-create", "--repo", "alice/demo", "--label", "work", "--filled", jsonOf(t, fillOf("report", "Title — as it stands ", odd)))
	if err != nil || r.Text != "filed alice/demo#1: https://github.com/alice/demo/issues/1" {
		t.Fatalf("an accepted filled template answers %q, %v, and wants it filed", r.Text, err)
	}
	paths, sent := posts(t, f)
	if len(sent) != 1 || paths[0] != "/repos/alice/demo/issues" || sent[0]["title"] != "Title — as it stands " || sent[0]["body"] != odd || jsonOf(t, sent[0]["labels"]) != `["work"]` {
		t.Fatalf("GitHub was sent %v %v, and wants the accepted title and document byte for byte, labelled work", paths, sent)
	}
	t.Logf("filed byte for byte: title %q, body %q", sent[0]["title"], sent[0]["body"])
}

// An answer of accept that holds no filled template's document, one that
// names the value free, and an issue's without a title, reach GitHub not at
// all: a write takes no free text, and an issue needs its title.
func TestAnAcceptedAnswerWithoutADocumentOrAnIssuesTitleIsRefused(t *testing.T) {
	t.Parallel()
	f, srv := newFake(t)
	empty := ""
	for _, c := range []struct {
		answer filled.Accepted
		want   string
	}{
		{filled.Accepted{Free: true}, "the template component accepts no filled template, and a write takes no free text"},
		{filled.Accepted{Template: "report", Title: &empty, Document: "d"}, "the filled template of report has no title, which an issue needs"},
		{filled.Accepted{Template: "report", Document: "d"}, "the filled template of report has no title, which an issue needs"},
		{filled.Accepted{Template: "report", Title: &empty}, "the template component accepts no filled template"},
	} {
		_, ix := writing(t, github.Options{Clock: &clock{now: nine}, Token: token("")}, `backend = "token", api = "`+srv.URL+`"`, &acceptor{answer: &c.answer})
		_, err := ix.Call("github issue-create", "--repo", "alice/demo", "--filled", jsonOf(t, fillOf("report", "t", "d")))
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "nothing reached GitHub") {
			t.Errorf("accept answering %+v answers %v, and wants %q", c.answer, err, c.want)
		}
	}
	if paths, _ := posts(t, f); len(paths) != 0 {
		t.Fatalf("GitHub was sent %v, and wants nothing", paths)
	}
}

// The acceptance: with the port template-accept unbound, the component still
// starts, and a write is refused, naming the binding, before any GitHub call.
func TestAWriteWithTheAcceptPortUnboundIsRefusedNamingTheBinding(t *testing.T) {
	t.Parallel()
	f, srv := newFake(t)
	f.created["/repos/alice/demo/issues"] = `{"number": 1}`
	_, ix := writing(t, github.Options{Clock: &clock{now: nine}, Token: token("")}, `backend = "token", api = "`+srv.URL+`"`, nil)
	want := `request port github.template-accept is bound to nothing: the wiring binds it, "github.template-accept" = "<instance>.<verb>"`
	for _, args := range [][]string{
		{"github issue-create", "--repo", "alice/demo", "--filled", jsonOf(t, fillOf("report", "t", "d"))},
		{"github issue-comment", "--repo", "alice/demo", "--number", "7", "--filled", jsonOf(t, fillOf("note", "", "d"))},
	} {
		_, err := ix.Call(args[0], args[1:]...)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "nothing reached GitHub") {
			t.Fatalf("%s with the port unbound answers %v, and wants %q", args[0], err, want)
		}
		t.Logf("%s refused: %v", args[0], err)
	}
	if paths, _ := posts(t, f); len(paths) != 0 {
		t.Fatalf("GitHub was sent %v, and wants nothing", paths)
	}
}

// A command takes the filled value as JSON, and neither free text nor a
// missing value.
func TestAWriteCommandTakesTheFilledValueAsJSON(t *testing.T) {
	t.Parallel()
	_, ix := writing(t, github.Options{Clock: &clock{now: nine}, Token: token("")}, `backend = "token", api = "http://127.0.0.1:1"`, &acceptor{})
	for args, want := range map[string]string{
		"--repo alice/demo":                         "names --repo OWNER/REPO and --filled JSON",
		"--repo alice/demo --filled {not-json":      "--filled is no JSON value",
		"--repo alice/demo --filled {} stray":       `takes no argument "stray"`,
		"--repo alice/demo --template report":       "flag provided but not defined: -template",
		"--repo alice/demo --fields {} --filled {}": "flag provided but not defined: -fields",
	} {
		if _, err := ix.Call("github issue-create", strings.Fields(args)...); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("github issue-create %s answers %v, and wants %q", args, err, want)
		}
	}
}
