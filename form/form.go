// Package form is kern's templates: a template is data, <name>.json, that
// names the fields a body is filled from, their shapes and a JSON Schema of
// them. The GitHub component checks a fill against one before anything
// reaches GitHub and renders the body from it (components/github, README.md
// "Templates"), and the checker (package lint) holds a managed file that a
// template rendered to the same template. One definition serves both, so a
// rule of a template is written once.
package form

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// A template is what a write to GitHub fills: a write names a template and
// its fields, never a body, and the component checks the fields against the
// template before anything reaches GitHub, then renders the title and the
// body from them. The template is data: the application's own template
// engine generates it as <name>.json in the folder the config key templates
// names, so kern checks a fill without calling that engine, and every caller
// that writes through kern passes the same check (README.md "Templates").
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

// kinds of a field and how each renders: line, choice and text as they
// stand, list as items (numbered or bulleted), issue one per line, checklist
// as task items, object as `key: value` lines, code in a fence.
var fieldKinds = map[string]bool{"line": true, "choice": true, "text": true, "list": true, "issue": true, "checklist": true, "object": true, "code": true}

var templateName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// LoadTemplate reads the template name from dir: <dir>/<name>.json.
func LoadTemplate(dir, name string) (Template, error) {
	if !templateName.MatchString(name) {
		return Template{}, fmt.Errorf("%q is no template name: letters, digits, dashes and underscores", name)
	}
	path := filepath.Join(dir, name+".json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Template{}, fmt.Errorf("no template %s: %s does not exist; the application writes its templates there (config templates)", name, filepath.ToSlash(path))
	}
	if err != nil {
		return Template{}, err
	}
	t, err := ParseTemplate(data)
	if err != nil {
		return Template{}, fmt.Errorf("template %s (%s): %w", name, filepath.ToSlash(path), err)
	}
	if t.Name != name {
		return Template{}, fmt.Errorf("template %s (%s) names itself %q", name, filepath.ToSlash(path), t.Name)
	}
	return t, nil
}

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

// Check answers the findings that refuse a fill of the template for the
// write action: the fields against the schema (its type, properties,
// required, additionalProperties, items and enum), then against each field's
// shape (FieldFindings), and an issue's title. None refuses nothing.
func (t Template) Check(action string, fields map[string]any) []string {
	var found []string
	if len(t.Actions) > 0 && !contains(t.Actions, action) {
		found = append(found, fmt.Sprintf("template %s serves %s, not %s", t.Name, strings.Join(t.Actions, " and "), action))
	}
	found = append(found, validate(t.Schema, fields, "fields")...)
	titled := false
	for _, f := range t.Fields {
		titled = titled || f.Source == "title"
	}
	for _, ff := range t.FieldFindings(fields) {
		found = append(found, ff.Message)
	}
	if action == IssueCreate && !titled {
		found = append(found, fmt.Sprintf("template %s has no title field (source title), so it files no issue", t.Name))
	}
	return found
}

// FieldFinding is one field's finding: the field it is about and what is wrong.
type FieldFinding struct {
	Field, Message string
}

// FieldFindings answers what each field's shape refuses of a fill, in the
// template's order: a required field filled, "None" only where it may be, a
// choice among its choices, at most max_words words. Check refuses a write
// with them, and the checker (package lint) holds a rendered file to them.
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

// wholeComment is a list item that is one whole HTML comment, kept as it is.
var wholeComment = regexp.MustCompile(`^\s*<!--.*-->\s*$`)

// Render answers the title and the body a fill of the template writes: the
// markers, each label field's `<label>: <value>` line, then each body field
// with a value under its heading, in the template's order.
func (t Template) Render(fields map[string]any) (title, body string) {
	var b strings.Builder
	for _, m := range t.Markers {
		b.WriteString(m + "\n")
	}
	var labels []string
	for _, f := range t.Fields {
		v := fields[f.Name]
		switch {
		case f.Source == "title":
			title, _ = v.(string)
			title = strings.TrimSpace(title)
		case f.Label != "" && !empty(v):
			labels = append(labels, fmt.Sprintf("%s: %s", f.Label, plain(v)))
		}
	}
	if len(labels) > 0 {
		b.WriteString("\n" + strings.Join(labels, "\n") + "\n")
	}
	hashes := strings.Repeat("#", t.Level)
	for _, f := range t.Fields {
		v, ok := fields[f.Name]
		if !f.inBody() || !ok || empty(v) {
			continue
		}
		fmt.Fprintf(&b, "\n%s %s\n\n%s\n", hashes, f.Heading, format(f, v))
	}
	return title, strings.TrimLeft(strings.TrimRight(b.String(), "\n"), "\n") + "\n"
}

func plain(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	data, _ := json.Marshal(v)
	return string(data)
}

// format writes a field's value as Markdown, by its kind.
func format(f TemplateField, v any) string {
	list, _ := v.([]any)
	var lines []string
	switch f.Kind {
	case "list":
		n := 1
		for _, e := range list {
			s := plain(e)
			switch {
			case wholeComment.MatchString(s):
				lines = append(lines, s)
			case f.Numbered:
				lines = append(lines, fmt.Sprintf("%d. %s", n, s))
				n++
			default:
				lines = append(lines, "- "+s)
			}
		}
	case "issue":
		for _, e := range list {
			lines = append(lines, plain(e))
		}
	case "checklist":
		for _, e := range list {
			m, _ := e.(map[string]any)
			mark := " "
			if done, _ := m["done"].(bool); done {
				mark = "x"
			}
			lines = append(lines, fmt.Sprintf("- [%s] %s", mark, plain(m["text"])))
		}
	case "object":
		if m, ok := v.(map[string]any); ok {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				lines = append(lines, fmt.Sprintf("%s: %s", k, plain(m[k])))
			}
		}
		for _, e := range list { // the schema's {"key", "value"} pairs, in their order
			p, _ := e.(map[string]any)
			lines = append(lines, fmt.Sprintf("%s: %s", plain(p["key"]), plain(p["value"])))
		}
	case "code":
		return fmt.Sprintf("```%s\n%s\n```", f.Lang, strings.TrimRight(plain(v), "\n"))
	default:
		return strings.TrimSpace(plain(v))
	}
	return strings.Join(lines, "\n")
}
