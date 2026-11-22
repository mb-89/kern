package lint

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	"kern/form"
)

// scheme is a URL's scheme: a target that names one lies outside the checkout.
var scheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

// schema holds a JSON file to JSON and to the schema its "$schema" names:
// a path relative to the file, or from the root where it starts with a
// slash, to a schema the files hold. A "$schema" that names a URL is the
// editor's to fetch, and judged not.
func (s *set) schema(f File) []Finding {
	var doc any
	if err := json.Unmarshal([]byte(f.Content), &doc); err != nil {
		line, c := 1, 1
		var syn *json.SyntaxError
		if errors.As(err, &syn) {
			line, c = position(f.Content, int(syn.Offset)-1) // the offset is past the character it refuses
		}
		return []Finding{{Path: f.Path, Line: line, Col: c, Rule: RuleSchema, Message: fmt.Sprintf("no JSON: %v", err)}}
	}
	obj, _ := doc.(map[string]any)
	named, _ := obj["$schema"].(string)
	if named == "" || scheme.MatchString(named) || strings.HasPrefix(named, "//") {
		return nil
	}
	line := lineOfKey(f.Content, "$schema")
	target := resolve(f.Path, named)
	sf, ok := s.files[target]
	if !ok {
		return []Finding{{Path: f.Path, Line: line, Col: 1, Rule: RuleSchema,
			Message: fmt.Sprintf("its \"$schema\" names %s, which stands nowhere among the managed files: name a schema that stands", target)}}
	}
	if !sf.Held {
		return nil
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(sf.Content), &schema); err != nil {
		return []Finding{{Path: f.Path, Line: line, Col: 1, Rule: RuleSchema,
			Message: fmt.Sprintf("its \"$schema\" names %s, which is no JSON object: %v", target, err)}}
	}
	delete(obj, "$schema")
	var found []Finding
	for _, msg := range form.Validate(schema, obj, "$") {
		at := 1
		if key, ok := topKey(msg); ok {
			at = lineOfKey(f.Content, key)
		}
		found = append(found, Finding{Path: f.Path, Line: at, Col: 1, Rule: RuleSchema, Message: fmt.Sprintf("by schema %s: %s", target, msg)})
	}
	return found
}

// resolve answers the path a target names from the file at from: relative
// to its folder, or from the root where it starts with a slash.
func resolve(from, target string) string {
	if strings.HasPrefix(target, "/") {
		return path.Clean(strings.TrimPrefix(target, "/"))
	}
	return path.Join(path.Dir(from), target)
}

// topKey answers the top-level key a validation message names, "$.<key>…".
func topKey(msg string) (string, bool) {
	rest, ok := strings.CutPrefix(msg, "$.")
	if !ok {
		return "", false
	}
	end := strings.IndexAny(rest, ".[ ")
	if end < 0 {
		end = len(rest)
	}
	return rest[:end], end > 0
}

// lineOfKey answers the first line that holds key as a JSON object key, 1 where none does.
func lineOfKey(content, key string) int {
	quoted, _ := json.Marshal(key)
	for i, l := range lines(content) {
		if j := strings.Index(l, string(quoted)); j >= 0 && strings.HasPrefix(strings.TrimSpace(l[j+len(quoted):]), ":") {
			return i + 1
		}
	}
	return 1
}

// position answers the line and the column, each from 1, of byte offset in content.
func position(content string, offset int) (int, int) {
	offset = max(0, min(offset, len(content)))
	before := content[:offset]
	line := strings.Count(before, "\n") + 1
	start := strings.LastIndex(before, "\n") + 1
	return line, col(content[start:], offset-start)
}
