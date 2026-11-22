package lint

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"kern/form"
)

// isTemplate says whether p is one of the templates: a <name>.json directly
// in the templates folder.
func (s *set) isTemplate(p string) bool {
	dir := path.Clean(s.config.Templates)
	return s.config.Templates != "" && path.Dir(p) == dir && path.Ext(p) == ".json"
}

// templates reads every template the files hold, by name, and answers a
// finding on each that cannot be read: form.ParseTemplate refuses it, or it
// names itself otherwise than its file.
func (s *set) templates() (map[string]form.Template, []Finding) {
	ts := map[string]form.Template{}
	var found []Finding
	for _, p := range s.paths {
		f := s.files[p]
		if !s.isTemplate(p) || !f.Held {
			continue
		}
		name := strings.TrimSuffix(path.Base(p), ".json")
		t, err := form.ParseTemplate([]byte(f.Content))
		if err == nil && t.Name != name {
			err = fmt.Errorf("it names itself %q, and its file names it %s: name the form as its file", t.Name, name)
		}
		if err != nil {
			found = append(found, Finding{Path: p, Line: 1, Col: 1, Rule: RuleTemplate, Message: fmt.Sprintf("template %s: %v", name, err)})
			continue
		}
		ts[name] = t
	}
	return ts, found
}

// rendered answers the template a Markdown file was rendered from: the one
// all of whose markers stand in it as lines of their own, the one with the
// most markers where several do, the first by name among those; false where
// none does. A template without markers renders nothing a file can be told by.
func rendered(ls []string, templates map[string]form.Template) (form.Template, bool) {
	have := map[string]bool{}
	for _, l := range ls {
		have[strings.TrimSpace(l)] = true
	}
	names := make([]string, 0, len(templates))
	for n := range templates {
		names = append(names, n)
	}
	sort.Strings(names)
	var best form.Template
	found := false
	for _, n := range names {
		t := templates[n]
		if len(t.Markers) == 0 || (found && len(t.Markers) <= len(best.Markers)) {
			continue
		}
		all := true
		for _, m := range t.Markers {
			all = all && have[strings.TrimSpace(m)]
		}
		if all {
			best, found = t, true
		}
	}
	return best, found
}

// heading is an ATX heading: its level and its text.
var heading = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(.*?)(?:[ \t]+#+)?[ \t]*$`)

// section is one heading of the template's level and the lines under it.
type section struct {
	text string
	line int // from 1
	body []string
}

// conform holds a Markdown file to the template it was rendered from, as
// form.Template.Render writes one: each heading of the template's level is a
// field's, in the template's order, once; each field's value, read back from
// the lines under its heading, meets the field's shape
// (form.Template.FieldFindings) and the field's schema (form.Validate); and
// each label field's `<label>: <value>` line stands where it is required.
func conform(f File, templates map[string]form.Template) []Finding {
	ls := lines(f.Content)
	t, ok := rendered(ls, templates)
	if !ok {
		return nil
	}
	code := fenced(ls)
	marker := 1
	for i, l := range ls {
		if strings.TrimSpace(l) == strings.TrimSpace(t.Markers[len(t.Markers)-1]) {
			marker = i + 1
			break
		}
	}
	var sections []section
	var labels []string
	for i, l := range ls {
		if code[i] {
			if len(sections) > 0 {
				sections[len(sections)-1].body = append(sections[len(sections)-1].body, l)
			}
			continue
		}
		if m := heading.FindStringSubmatch(l); m != nil && len(m[1]) <= t.Level {
			if len(m[1]) == t.Level {
				sections = append(sections, section{text: strings.TrimSpace(m[2]), line: i + 1})
			} else {
				sections = append(sections, section{line: i + 1}) // a higher heading ends the field before it
			}
			continue
		}
		if len(sections) == 0 {
			labels = append(labels, l)
			continue
		}
		sections[len(sections)-1].body = append(sections[len(sections)-1].body, l)
	}

	byHeading := map[string]int{}
	var headings []string
	for i, fl := range t.Fields {
		if fl.InBody() {
			byHeading[fl.Heading] = i
			headings = append(headings, strings.Repeat("#", t.Level)+" "+fl.Heading)
		}
	}
	at := func(line int, format string, a ...any) Finding {
		return Finding{Path: f.Path, Line: line, Col: 1, Rule: RuleTemplate, Message: fmt.Sprintf("template %s: ", t.Name) + fmt.Sprintf(format, a...)}
	}
	var found []Finding
	closed := t.Schema["additionalProperties"] == false
	fields := map[string]any{}
	lineOf := map[string]int{}
	last, lastHeading := -1, ""
	for _, sec := range sections {
		if sec.text == "" {
			continue
		}
		i, known := byHeading[sec.text]
		if !known {
			if closed {
				found = append(found, at(sec.line, "%q is no heading of it: its headings are %s", strings.Repeat("#", t.Level)+" "+sec.text, strings.Join(headings, ", ")))
			}
			continue
		}
		fl := t.Fields[i]
		if _, twice := lineOf[fl.Name]; twice {
			found = append(found, at(sec.line, "the heading %q of field %s stands twice: merge them", sec.text, fl.Name))
			continue
		}
		if i < last {
			found = append(found, at(sec.line, "the heading %q of field %s stands after %q, and the template puts it before", sec.text, fl.Name, lastHeading))
		} else {
			last, lastHeading = i, sec.text
		}
		lineOf[fl.Name] = sec.line
		fields[fl.Name] = value(fl, sec.body)
	}
	for _, fl := range t.Fields {
		if fl.Label == "" {
			continue
		}
		for i, l := range labels {
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), fl.Label+":"); ok {
				fields[fl.Name] = strings.TrimSpace(v)
				lineOf[fl.Name] = i + 1
			}
		}
	}
	props, _ := t.Schema["properties"].(map[string]any)
	for _, ff := range t.FieldFindings(fields) {
		line, ok := lineOf[ff.Field]
		msg := ff.Message
		if !ok {
			line = marker
			for _, fl := range t.Fields {
				switch {
				case fl.Name != ff.Field:
				case fl.InBody():
					msg += fmt.Sprintf(" (its heading %q is missing)", strings.Repeat("#", t.Level)+" "+fl.Heading)
				case fl.Label != "":
					msg += fmt.Sprintf(" (its line %q is missing)", fl.Label+": ")
				}
			}
		}
		if ok || !isTitle(t, ff.Field) {
			found = append(found, at(line, "%s", msg))
		}
	}
	for _, fl := range t.Fields {
		v, ok := fields[fl.Name]
		schema, _ := props[fl.Name].(map[string]any)
		if !ok || schema == nil || fl.Kind == "object" {
			continue
		}
		for _, msg := range form.Validate(schema, v, "field "+fl.Name) {
			found = append(found, at(lineOf[fl.Name], "%s", msg))
		}
	}
	return found
}

// isTitle says whether field is the template's title: a file holds its body
// alone, so a title is never missing from it.
func isTitle(t form.Template, field string) bool {
	for _, fl := range t.Fields {
		if fl.Name == field {
			return fl.Source == "title"
		}
	}
	return false
}

// item is a list item: a bullet or a number, then its text.
var item = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+(.*)$`)

// task is a checklist item: its mark and its text.
var task = regexp.MustCompile(`^\s*[-*+]\s+\[([ xX])\]\s+(.*)$`)

// comment is a line that is one whole HTML comment, which a value reads past.
var comment = regexp.MustCompile(`^\s*<!--.*-->\s*$`)

// value reads a field's value back from the lines under its heading, as
// form's Render writes each kind: text as it stands, a list as its items, a
// checklist as {text, done}, code as the fence's content.
func value(fl form.TemplateField, body []string) any {
	var kept []string
	for _, l := range body {
		if !comment.MatchString(l) {
			kept = append(kept, l)
		}
	}
	text := strings.TrimSpace(strings.Join(kept, "\n"))
	switch fl.Kind {
	case "list", "issue":
		items := []any{}
		for _, l := range kept {
			if m := item.FindStringSubmatch(l); m != nil {
				items = append(items, strings.TrimSpace(m[1]))
			} else if fl.Kind == "issue" && strings.TrimSpace(l) != "" {
				items = append(items, strings.TrimSpace(l))
			}
		}
		if len(items) == 0 && text != "" {
			items = append(items, text)
		}
		return items
	case "checklist":
		items := []any{}
		for _, l := range kept {
			if m := task.FindStringSubmatch(l); m != nil {
				items = append(items, map[string]any{"text": strings.TrimSpace(m[2]), "done": m[1] != " "})
			}
		}
		return items
	case "code":
		ls := strings.Split(text, "\n")
		if len(ls) >= 2 && strings.HasPrefix(ls[0], "```") && strings.HasPrefix(ls[len(ls)-1], "```") {
			return strings.Join(ls[1:len(ls)-1], "\n")
		}
		return text
	}
	return text
}
