package form_test

import (
	"strings"
	"testing"

	"kern/form"
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

// A template is read as the engine wrote it, its fields in order, one that is
// broken is refused, naming why, and a field's shape counts its words.
func TestATemplateIsReadOrRefused(t *testing.T) {
	tpl, err := form.ParseTemplate([]byte(report))
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
		if _, err := form.ParseTemplate([]byte(text)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s reads %v, and wants %q", text, err, want)
		}
	}
	found := tpl.FieldFindings(map[string]any{"title": "t", "situation": strings.Repeat("word ", 21), "requirements": []any{"r"}})
	if len(found) != 1 || found[0].Field != "situation" || found[0].Message != "field situation has 21 words, over its 20" {
		t.Errorf("a long field reads %+v, and wants its words counted against max_words", found)
	}
}
