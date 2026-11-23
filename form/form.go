// Package form is the templates the checker reads: a template is data,
// <name>.json, that names the fields a body is filled from, their shapes and
// a JSON Schema of them, and the checker (package lint) holds a managed file
// that a template rendered to it (README.md "Templates"). It is no write
// path: a write to GitHub takes a filled template that the application's
// template component made and accepts (package filled), never fields kern
// checks itself.
package form

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// A template is what the checker holds a rendered managed file to. It is
// data: the application's own template engine generates it as <name>.json in
// the templates folder the checker's config names, so kern checks a file
// without calling that engine (README.md "Templates").
//
// Its file holds the template's name (form), what it is for (says), each
// field's shape in order (field_shape) and a JSON Schema 2020-12 of the fields
// (schema), as a template engine's JSON draft writes them (README.md
// "Templates" names one), and beside them
// what kern reads alone: the lines the body opens with (markers), the writes
// it serves (actions, issue.create or comment.create; none names both) and
// its fields' heading level (level, 2 by default). Other keys are read past.
type Template struct {
	Name    string
	Says    string
	Markers []string
	Actions []string
	Level   int
	Fields  []TemplateField
	Schema  map[string]any
}

// TemplateField is one field's shape: its kind, its heading in the body, and
// what a fill of it must hold.
type TemplateField struct {
	Name     string   `json:"-"`
	Kind     string   `json:"kind"`
	Heading  string   `json:"heading"`
	Required bool     `json:"required"`
	NoneOK   bool     `json:"none_ok"`
	Choices  []string `json:"choices"`
	MaxWords int      `json:"max_words"`
	Numbered bool     `json:"numbered"`
	Label    string   `json:"label"`
	Source   string   `json:"source"`
	Lang     string   `json:"lang"`
}

// The writes a template serves, as its actions name them.
const (
	IssueCreate   = "issue.create"
	CommentCreate = "comment.create"
)

// kinds of a field, as the template engine renders each: line, choice and
// text as they stand, list as items (numbered or bulleted), issue one per
// line, checklist as task items, object as `key: value` lines, code in a fence.
var fieldKinds = map[string]bool{"line": true, "choice": true, "text": true, "list": true, "issue": true, "checklist": true, "object": true, "code": true}

// ParseTemplate reads a template's JSON, keeping its fields in their order.
func ParseTemplate(data []byte) (Template, error) {
	var raw struct {
		Form       string          `json:"form"`
		Says       string          `json:"says"`
		Markers    []string        `json:"markers"`
		Actions    []string        `json:"actions"`
		Level      int             `json:"level"`
		FieldShape json.RawMessage `json:"field_shape"`
		Schema     map[string]any  `json:"schema"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Template{}, fmt.Errorf("no template JSON: %w", err)
	}
	t := Template{Name: raw.Form, Says: raw.Says, Markers: raw.Markers, Actions: raw.Actions, Level: raw.Level, Schema: raw.Schema}
	if t.Name == "" {
		return Template{}, errors.New("it names no form")
	}
	if t.Level == 0 {
		t.Level = 2
	}
	if t.Level < 1 || t.Level > 6 {
		return Template{}, fmt.Errorf("level %d is no heading level, 1 to 6", t.Level)
	}
	for _, a := range t.Actions {
		if a != IssueCreate && a != CommentCreate {
			return Template{}, fmt.Errorf("action %q is none of %s and %s", a, IssueCreate, CommentCreate)
		}
	}
	if props, _ := t.Schema["properties"].(map[string]any); t.Schema["type"] != "object" || props == nil {
		return Template{}, errors.New("its schema is no object with properties")
	}
	fields, err := orderedFields(raw.FieldShape)
	if err != nil {
		return Template{}, err
	}
	for _, f := range fields {
		switch {
		case !fieldKinds[f.Kind]:
			return Template{}, fmt.Errorf("field %s: %q is no kind", f.Name, f.Kind)
		case f.Source != "" && f.Source != "title":
			return Template{}, fmt.Errorf("field %s: source %q is not title", f.Name, f.Source)
		case f.inBody() && strings.TrimSpace(f.Heading) == "":
			return Template{}, fmt.Errorf("field %s has no heading", f.Name)
		}
	}
	t.Fields = fields
	return t, nil
}

// orderedFields reads field_shape, an object, in the order its keys stand.
func orderedFields(data json.RawMessage) ([]TemplateField, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("its field_shape is no object")
	}
	var fields []TemplateField
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("its field_shape: %w", err)
		}
		var f TemplateField
		if err := dec.Decode(&f); err != nil {
			return nil, fmt.Errorf("field %v: %w", tok, err)
		}
		f.Name = tok.(string)
		fields = append(fields, f)
	}
	if len(fields) == 0 {
		return nil, errors.New("it has no field")
	}
	return fields, nil
}

func (f TemplateField) inBody() bool { return f.Source == "" && f.Label == "" }

// InBody says whether the field stands in the body under its heading: it is
// neither the title nor a label line.
func (f TemplateField) InBody() bool { return f.inBody() }

// FieldFinding is one field's finding: the field it is about and what is wrong.
type FieldFinding struct {
	Field, Message string
}

// FieldFindings answers what each field's shape refuses of a fill, in the
// template's order: a required field filled, "None" only where it may be, a
// choice among its choices, at most max_words words. The checker (package
// lint) holds a rendered file to them.
func (t Template) FieldFindings(fields map[string]any) []FieldFinding {
	var found []FieldFinding
	props, _ := t.Schema["properties"].(map[string]any)
	for _, f := range t.Fields {
		v, given := fields[f.Name]
		says := ""
		if p, ok := props[f.Name].(map[string]any); ok {
			if d, ok := p["description"].(string); ok && d != "" {
				says = ": " + d
			}
		}
		add := func(format string, a ...any) {
			found = append(found, FieldFinding{Field: f.Name, Message: fmt.Sprintf(format, a...)})
		}
		if empty(v) {
			if f.Required {
				add("field %s is required%s", f.Name, says)
			}
			continue
		}
		if !given {
			continue
		}
		if none(v) && !f.NoneOK && f.Required {
			add("field %s takes no None: fill it%s", f.Name, says)
		}
		if s, ok := v.(string); ok {
			if f.Kind == "choice" && len(f.Choices) > 0 && !contains(f.Choices, s) {
				add("field %s is %q, none of %s", f.Name, s, strings.Join(f.Choices, ", "))
			}
			if n := len(strings.Fields(s)); f.MaxWords > 0 && n > f.MaxWords {
				add("field %s has %d words, over its %d", f.Name, n, f.MaxWords)
			}
		}
	}
	return found
}

// Validate checks v against a JSON Schema of the keywords a generated
// template carries (type, properties, required, additionalProperties, items
// and enum), naming each place from at: the checker (package lint) holds a
// managed JSON file to the schema it names with it.
func Validate(schema map[string]any, v any, at string) []string { return validate(schema, v, at) }

// validate checks v against a JSON Schema of the keywords a generated
// template carries: type, properties, required, additionalProperties,
// items and enum.
func validate(schema map[string]any, v any, at string) []string {
	if schema == nil {
		return nil
	}
	var found []string
	if want, ok := schema["type"].(string); ok && !typed(want, v) {
		return []string{fmt.Sprintf("%s is %s, and the template takes %s", at, typeOf(v), want)}
	}
	if enum, ok := schema["enum"].([]any); ok {
		in := false
		for _, e := range enum {
			in = in || fmt.Sprint(e) == fmt.Sprint(v)
		}
		if !in {
			found = append(found, fmt.Sprintf("%s is %v, none of %v", at, v, enum))
		}
	}
	switch x := v.(type) {
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				if _, has := x[fmt.Sprint(r)]; !has {
					found = append(found, fmt.Sprintf("%s.%v is required", at, r))
				}
			}
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p, known := props[k].(map[string]any)
			if !known {
				if schema["additionalProperties"] == false {
					found = append(found, fmt.Sprintf("%s.%s is no field of the template", at, k))
				}
				continue
			}
			found = append(found, validate(p, x[k], at+"."+k)...)
		}
	case []any:
		if items, ok := schema["items"].(map[string]any); ok {
			for i, item := range x {
				found = append(found, validate(items, item, fmt.Sprintf("%s[%d]", at, i))...)
			}
		}
	}
	return found
}

func typed(want string, v any) bool {
	switch want {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "number", "integer":
		_, ok := v.(float64)
		return ok
	}
	return true
}

func typeOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "an object"
	case []any:
		return "an array"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case float64:
		return "a number"
	}
	return fmt.Sprintf("%T", v)
}

func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

// none says whether v fills its field with "None", as text or as a list's one item.
func none(v any) bool {
	if s, ok := v.(string); ok {
		return strings.EqualFold(strings.TrimSpace(s), "none")
	}
	if l, ok := v.([]any); ok && len(l) == 1 {
		return none(l[0])
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
