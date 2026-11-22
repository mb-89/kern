package lint_test

import (
	"strings"
	"testing"

	"kern/lint"
)

// note is a template as a template engine writes one: a marker, a choice,
// a text of at most five words, a required list, and closed fields.
const note = `{
  "form": "note",
  "says": "A note.",
  "markers": ["<!-- se:form note -->"],
  "field_shape": {
    "title": {"kind": "line", "source": "title", "required": true},
    "kind": {"kind": "choice", "heading": "Kind", "required": true, "choices": ["idea", "fact"]},
    "gist": {"kind": "text", "heading": "Gist", "required": true, "max_words": 5},
    "steps": {"kind": "list", "heading": "Steps", "required": true, "numbered": true},
    "owner": {"kind": "line", "label": "Owner", "required": true}
  },
  "schema": {
    "type": "object",
    "additionalProperties": false,
    "properties": {
      "title": {"type": "string"},
      "kind": {"type": "string", "enum": ["idea", "fact"]},
      "gist": {"type": "string", "description": "What it says, in a few words."},
      "steps": {"type": "array", "items": {"type": "string"}},
      "owner": {"type": "string"}
    }
  }
}`

// good is a note as form's Render writes it.
const good = `<!-- se:form note -->

Owner: mb

## Kind

idea

## Gist

It holds the shape.

## Steps

1. Read it.
2. Check it.
`

var config = lint.Config{Folders: []string{"docs"}, Templates: "docs/templates", NameLength: 40, NameWords: 5}

func held(path, content string) lint.File { return lint.File{Path: path, Content: content, Held: true} }

func check(t *testing.T, files ...lint.File) []string {
	t.Helper()
	var got []string
	for _, f := range lint.Check(append(files, held("docs/templates/note.json", note)), config) {
		got = append(got, f.String())
	}
	return got
}

// expect fails unless got holds exactly want, in order.
func expect(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A file rendered from its template, links that reach what stands and a
// name under its caps hold no finding.
func TestAConformingFileHoldsNoFinding(t *testing.T) {
	t.Parallel()
	expect(t, check(t,
		held("docs/a.md", good+"\nSee [b](b.md#the-shape), [the folder](sub/), [the top](#steps) and [out](../README.md).\n"),
		held("docs/b.md", "# The shape\n"),
		held("docs/sub/c.md", "c\n"),
	))
}

// A file that breaks its template is found at the lines that break it: a
// heading missing, one out of order, one the template does not know, a
// choice none of its choices, a text over its words, and a label line gone.
func TestAFileThatBreaksItsTemplateIsFound(t *testing.T) {
	t.Parallel()
	broken := `<!-- se:form note -->

## Gist

It holds the shape of every managed file.

## Kind

maybe

## Notes

more
`
	expect(t, check(t, held("docs/a.md", broken)),
		`docs/a.md:1:1: template: template note: field owner is required (its line "Owner: " is missing)`,
		`docs/a.md:1:1: template: template note: field steps is required (its heading "## Steps" is missing)`,
		`docs/a.md:3:1: template: template note: field gist has 8 words, over its 5`,
		`docs/a.md:7:1: template: template note: field kind is "maybe", none of idea, fact`,
		`docs/a.md:7:1: template: template note: field kind is maybe, none of [idea fact]`,
		`docs/a.md:7:1: template: template note: the heading "Kind" of field kind stands after "Gist", and the template puts it before`,
		`docs/a.md:11:1: template: template note: "## Notes" is no heading of it: its headings are ## Kind, ## Gist, ## Steps`,
	)
}

// A Markdown file no template's markers name is held to no template, and a
// heading inside a code fence is no heading.
func TestAFileOfNoTemplateIsHeldToNone(t *testing.T) {
	t.Parallel()
	fencedHeading := good + "\n```md\n## Notes\n```\n"
	expect(t, check(t, held("docs/free.md", "# Anything\n\n## Goes\n"), held("docs/a.md", fencedHeading)))
}

// A template that cannot be read is found on its own file.
func TestABrokenTemplateIsFound(t *testing.T) {
	t.Parallel()
	expect(t, check(t, held("docs/templates/other.json", `{"form": "else", "field_shape": {"a": {"kind": "text", "heading": "A"}}, "schema": {"type": "object", "properties": {}}}`)),
		`docs/templates/other.json:1:1: template: template other: it names itself "else", and its file names it other: name the form as its file`)
}

// A link into a managed folder that reaches nothing is found where it
// stands; one to a URL, one out of every managed folder, and one in code
// are judged not.
func TestADeadLinkIsFound(t *testing.T) {
	t.Parallel()
	body := "# Top\n\nSee [gone](gone.md), [a fragment](b.md#nowhere) and ![img](sub/x.png).\n" +
		"[web](https://example.com/x) [out](../src/main.go) `[code](gone.md)`\n" +
		"\n```\n[fenced](gone.md)\n```\n\n[ref]: ./also-gone.md\n"
	expect(t, check(t, held("docs/a.md", body), held("docs/b.md", "# B\n")),
		`docs/a.md:3:12: link: the link to gone.md reaches nothing: docs/gone.md stands nowhere among the managed files`,
		`docs/a.md:3:35: link: the link to b.md#nowhere reaches nothing: docs/b.md has no heading or anchor #nowhere`,
		`docs/a.md:3:60: link: the link to sub/x.png reaches nothing: docs/sub/x.png stands nowhere among the managed files`,
		`docs/a.md:10:8: link: the link to ./also-gone.md reaches nothing: docs/also-gone.md stands nowhere among the managed files`,
	)
}

// A JSON file that is no JSON, names a schema that stands nowhere, or breaks
// the schema it names is found at the line that breaks it.
func TestAJSONFileIsHeldToItsSchema(t *testing.T) {
	t.Parallel()
	schema := `{"type": "object", "required": ["name"], "additionalProperties": false, "properties": {"name": {"type": "string"}, "size": {"type": "number"}}}`
	expect(t, check(t,
		held("docs/schemas/thing.json", schema),
		held("docs/good.json", `{"$schema": "schemas/thing.json", "name": "a", "size": 2}`),
		held("docs/bad.json", "{\n  \"$schema\": \"schemas/thing.json\",\n  \"size\": \"big\",\n  \"colour\": 1\n}\n"),
		held("docs/lost.json", `{"$schema": "/docs/schemas/none.json"}`),
		held("docs/web.json", `{"$schema": "https://json-schema.org/draft/2020-12/schema"}`),
		held("docs/broken.json", "{\n  \"a\": 1,\n}\n"),
	),
		`docs/bad.json:1:1: schema: by schema docs/schemas/thing.json: $.name is required`,
		`docs/bad.json:3:1: schema: by schema docs/schemas/thing.json: $.size is a string, and the template takes number`,
		`docs/bad.json:4:1: schema: by schema docs/schemas/thing.json: $.colour is no field of the template`,
		`docs/broken.json:3:1: schema: no JSON: invalid character '}' looking for beginning of object key string`,
		`docs/lost.json:1:1: schema: its "$schema" names docs/schemas/none.json, which stands nowhere among the managed files: name a schema that stands`,
	)
}

// A file whose name is over a cap is found, held by its hash alone or not.
func TestANameOverItsCapsIsFound(t *testing.T) {
	t.Parallel()
	expect(t, check(t,
		held("docs/one-two-three-four-five.md", "a\n"),
		held("docs/one-two-three-four-five-six.md", "a\n"),
		lint.File{Path: "docs/" + strings.Repeat("x", 41) + ".bin"},
	),
		`docs/one-two-three-four-five-six.md:1:1: name: the name one-two-three-four-five-six.md has 6 words, over the cap of 5: shorten it`,
		`docs/`+strings.Repeat("x", 41)+`.bin:1:1: name: the name `+strings.Repeat("x", 41)+`.bin has 45 characters, over the cap of 40: shorten it`,
	)
}

// A column counts UTF-16 code units, as the Language Server Protocol does.
func TestAColumnCountsUTF16(t *testing.T) {
	t.Parallel()
	got := lint.Check([]lint.File{held("docs/a.md", "ä😀 [x](gone.md)\n")}, config)
	if len(got) != 1 || got[0].Col != 9 || got[0].EndCol != 16 {
		t.Fatalf("a link after ä and an emoji: %+v", got)
	}
}
