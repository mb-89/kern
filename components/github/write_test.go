package github_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"kern/components/github"
	"kern/index"
)

// note is a comment's template: a label line, then a text, a checklist, an
// object and a code block, the last kinds a body renders.
const note = `{
  "form": "note",
  "says": "A note on an issue.",
  "actions": ["comment.create"],
  "level": 3,
  "field_shape": {
    "status": {"kind": "line", "label": "Status", "required": true},
    "text": {"kind": "text", "heading": "Note", "required": true},
    "checks": {"kind": "checklist", "heading": "Checks"},
    "facts": {"kind": "object", "heading": "Facts"},
    "log": {"kind": "code", "heading": "Log", "lang": "sh"},
    "blocked_by": {"kind": "issue", "heading": "Blocked by", "required": true, "none_ok": true}
  },
  "schema": {
    "type": "object",
    "properties": {
      "status": {"type": "string"},
      "text": {"type": "string"},
      "checks": {"type": "array", "items": {"type": "object", "properties": {"done": {"type": "boolean"}, "text": {"type": "string"}}, "required": ["done", "text"], "additionalProperties": false}},
      "facts": {"type": "array", "items": {"type": "object", "properties": {"key": {"type": "string"}, "value": {"type": "string"}}, "required": ["key", "value"], "additionalProperties": false}},
      "log": {"type": "string"},
      "blocked_by": {"type": "array", "items": {"type": "string"}}
    },
    "required": ["status", "text"],
    "additionalProperties": false
  }
}`

func writing(t *testing.T, o github.Options, config string, more ...*index.Component) (*github.GitHub, *index.Index) {
	t.Helper()
	g := github.New(o)
	w, err := index.ParseWiring([]byte(`
[instances.github]
component = "github"
config = { ` + config + ` }
` + wiresOf(more)))
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.Start(w, append([]*index.Component{g.Component}, more...)...)
	if err != nil {
		t.Fatal(err)
	}
	return g, ix
}

// wiresOf wires the clock's second built-in, and the writer where it is one of more.
func wiresOf(more []*index.Component) string {
	if len(more) == 0 {
		return "\n[wires]\n\"github.second\" = \"built-in\"\n"
	}
	return "\n[instances.writer]\ncomponent = \"writer\"\n\n[wires]\n\"github.second\" = \"built-in\"\n\"writer.create\" = \"github.issue-create\"\n"
}

// A comment is posted from a filled template: its label line, then each
// field under its heading at the template's level, None filling the field
// that takes it; the answer links the comment.
func TestACommentIsPostedFromAFilledTemplate(t *testing.T) {
	t.Parallel()
	f, srv := newFake(t)
	f.created["/repos/alice/demo/issues/7/comments"] = `{"id": 99, "html_url": "https://github.com/alice/demo/issues/7#issuecomment-99"}`
	dir := templates(t, map[string]string{"note": note, "report": report})
	_, ix := writing(t, github.Options{Clock: &clock{now: nine}, Token: token("")},
		`backend = "token", api = "`+srv.URL+`", templates = '`+filepath.ToSlash(dir)+`'`)
	fields := `{"status": "landed", "text": "It landed.", "checks": [{"done": true, "text": "suite"}, {"done": false, "text": "release"}],
		"facts": [{"key": "pin", "value": "v5"}], "log": "go test ./...", "blocked_by": ["None"]}`
	r, err := ix.Call("github issue-comment", "--repo", "alice/demo", "--number", "7", "--template", "note", "--fields", fields)
	if err != nil || r.Text != "commented on alice/demo#7: https://github.com/alice/demo/issues/7#issuecomment-99" {
		t.Fatalf("github issue-comment answers %q, %v, and wants the comment linked", r.Text, err)
	}
	f.mu.Lock()
	posted := append([]string{}, f.posted...)
	f.mu.Unlock()
	var sent struct{ Body string }
	if len(posted) == 1 {
		path, body, _ := strings.Cut(posted[0], " ")
		if err := json.Unmarshal([]byte(body), &sent); err != nil || path != "/repos/alice/demo/issues/7/comments" {
			t.Fatalf("GitHub was sent %q (%v)", posted[0], err)
		}
	}
	want := "Status: landed\n\n### Note\n\nIt landed.\n\n### Checks\n\n- [x] suite\n- [ ] release\n\n### Facts\n\npin: v5\n\n### Log\n\n```sh\ngo test ./...\n```\n\n### Blocked by\n\nNone\n"
	if len(posted) != 1 || sent.Body != want {
		t.Fatalf("GitHub was sent %q, and wants the comment the template renders:\n%s", posted, want)
	}

	// A template serves the writes its actions name.
	_, err = ix.Call("github issue-comment", "--repo", "alice/demo", "--number", "7", "--template", "report",
		"--fields", `{"title": "t", "situation": "s", "requirements": ["r"]}`)
	if err == nil || !strings.Contains(err.Error(), "template report serves issue.create, not comment.create") {
		t.Fatalf("a comment from an issue's template answers %v, and wants it refused", err)
	}
	// None fills only the field that takes it; an empty required field is refused.
	_, err = ix.Call("github issue-comment", "--repo", "alice/demo", "--number", "7", "--template", "note", "--fields", `{"status": "None", "text": "t", "blocked_by": []}`)
	if err == nil || !strings.Contains(err.Error(), "field status takes no None") || !strings.Contains(err.Error(), "field blocked_by is required") {
		t.Fatalf("a fill with None where it may not stand and an empty required field answers %v, and wants both refused", err)
	}
}

// Through the relay, the write is a step of the session's own GitHub tool:
// issue_write with method create and the rendered title and body; its answer
// names the issue filed.
func TestTheRelayFilesThroughTheSessionsTool(t *testing.T) {
	t.Parallel()
	dir := templates(t, map[string]string{"report": report})
	g, ix := writing(t, github.Options{Clock: &clock{now: nine}, Token: noToken, Cloud: notCloud}, `step-wait = "10s", templates = '`+filepath.ToSlash(dir)+`'`)
	done := make(chan called, 1)
	go func() {
		r, err := ix.Call("github issue-create", "--repo", "alice/demo", "--template", "report",
			"--fields", `{"title": "relay-files", "situation": "The relay files.", "requirements": ["File it."]}`)
		done <- called{r, err}
	}()
	told := waitOutbox(t, g, "issue_write")
	if !strings.Contains(told, `"method":"create"`) || !strings.Contains(told, `"title":"relay-files"`) || !strings.Contains(told, `file the issue "relay-files" in alice/demo, from template report`) {
		t.Fatalf("the relay tells %q, and wants issue_write create with the rendered title", told)
	}
	body := "<!-- report -->\n\n## Situation\n\nThe relay files.\n\n## Requirements\n\n1. File it.\n"
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

// The verb itself takes no free text: a request with a body or a title is
// refused before anything reaches GitHub, whoever asks it.
func TestAWriteVerbTakesNoFreeText(t *testing.T) {
	t.Parallel()
	f, srv := newFake(t)
	f.created["/repos/alice/demo/issues"] = `{"number": 1}`
	dir := templates(t, map[string]string{"report": report})
	_, ix := writing(t, github.Options{Clock: &clock{now: nine}, Token: token("")},
		`backend = "token", api = "`+srv.URL+`", templates = '`+filepath.ToSlash(dir)+`'`, writer)
	for _, key := range []string{"body", "title"} {
		_, err := ix.Call("write", `{"owner": "alice", "repo": "demo", "template": "report", "fields": {"title": "t", "situation": "s", "requirements": ["r"]}, "`+key+`": "free text"}`)
		if err == nil || !strings.Contains(err.Error(), `unknown field "`+key+`"`) || !strings.Contains(err.Error(), "never free text") {
			t.Fatalf("a request with a %s answers %v, and wants it refused as free text", key, err)
		}
	}
	if _, err := ix.Call("write", `{"owner": "alice", "repo": "demo", "template": "report", "fields": {"title": "t", "situation": "s", "requirements": ["r"]}}`); err != nil {
		t.Fatalf("a filled template answers %v, and wants it filed", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.posted) != 1 {
		t.Fatalf("GitHub was sent %q, and wants the filled template's issue alone", f.posted)
	}
}

// A template is read as the engine wrote it, its fields in order, and one
// that is broken or misnamed is refused, naming why.
func TestATemplateIsReadOrRefused(t *testing.T) {
	tpl, err := github.ParseTemplate([]byte(report))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range tpl.Fields {
		names = append(names, f.Name)
	}
	if strings.Join(names, " ") != "title situation requirements kind" || tpl.Level != 2 {
		t.Fatalf("the template reads fields %v at level %d, and wants them in the file's order at level 2", names, tpl.Level)
	}
	for text, want := range map[string]string{
		`{"says": "x"}`: "names no form",
		`{"form": "x", "schema": {"type": "object", "properties": {}}, "field_shape": {"a": {"kind": "prose", "heading": "A"}}}`:                         `"prose" is no kind`,
		`{"form": "x", "schema": {"type": "object", "properties": {}}, "field_shape": {"a": {"kind": "text"}}}`:                                          "field a has no heading",
		`{"form": "x", "schema": {"type": "object", "properties": {}}, "field_shape": {}}`:                                                               "it has no field",
		`{"form": "x", "field_shape": {"a": {"kind": "text", "heading": "A"}}}`:                                                                          "its schema is no object with properties",
		`{"form": "x", "actions": ["pr.merge"], "schema": {"type": "object", "properties": {}}, "field_shape": {"a": {"kind": "text", "heading": "A"}}}`: `action "pr.merge"`,
	} {
		if _, err := github.ParseTemplate([]byte(text)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s reads %v, and wants %q", text, err, want)
		}
	}
	dir := templates(t, map[string]string{"report": report, "other": report})
	if _, err := github.LoadTemplate(dir, "../report"); err == nil || !strings.Contains(err.Error(), "is no template name") {
		t.Errorf("a path as a name reads %v, and wants it refused", err)
	}
	if _, err := github.LoadTemplate(dir, "other"); err == nil || !strings.Contains(err.Error(), `names itself "report"`) {
		t.Errorf("a misnamed template reads %v, and wants it refused", err)
	}
	if found := tpl.Check(github.IssueCreate, map[string]any{"title": "t", "situation": strings.Repeat("word ", 21), "requirements": []any{"r"}}); strings.Join(found, ";") != "field situation has 21 words, over its 20" {
		t.Errorf("a long field reads %v, and wants its words counted against max_words", found)
	}
}
