package lint

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// inline is an inline link or image, `[text](target "title")`: its target.
var inline = regexp.MustCompile(`!?\[[^\]]*\]\(\s*(<[^>]*>|[^)\s]+)(?:\s+(?:"[^"]*"|'[^']*'|\([^)]*\)))?\s*\)`)

// reference is a link reference definition, `[label]: target`: its target.
var reference = regexp.MustCompile(`^ {0,3}\[[^\]]+\]:\s*(<[^>]*>|\S+)`)

// codeSpan is an inline code span, which holds no link.
var codeSpan = regexp.MustCompile("`+[^`]*`+")

// anchorName is an HTML anchor a fragment may name: name="…" or id="…".
var anchorName = regexp.MustCompile(`(?:name|id)\s*=\s*"([^"]+)"`)

// links holds every link of a Markdown file whose target lies in a managed
// folder to a file or a folder that stands, and a fragment into a Markdown
// file the files hold to one of its headings or anchors. A target outside
// every managed folder, or one that names a URL, is judged not: the checker
// knows only the managed files.
func (s *set) links(f File) []Finding {
	ls := lines(f.Content)
	code := fenced(ls)
	var found []Finding
	for i, l := range ls {
		if code[i] {
			continue
		}
		// A code span's text is blanked, so its offsets stay the line's.
		plain := codeSpan.ReplaceAllStringFunc(l, func(m string) string { return strings.Repeat(" ", len(m)) })
		var spans [][]int
		for _, m := range inline.FindAllStringSubmatchIndex(plain, -1) {
			spans = append(spans, m[2:4])
		}
		if m := reference.FindStringSubmatchIndex(plain); m != nil {
			spans = append(spans, m[2:4])
		}
		for _, sp := range spans {
			target := strings.Trim(plain[sp[0]:sp[1]], "<>")
			if why := s.dead(f.Path, target); why != "" {
				found = append(found, Finding{Path: f.Path, Line: i + 1, Col: col(l, sp[0]), EndCol: col(l, sp[1]), Rule: RuleLink, Message: why})
			}
		}
	}
	return found
}

// dead says why the link to target from the file at from reaches nothing,
// "" where it reaches something or is not judged.
func (s *set) dead(from, target string) string {
	if target == "" || scheme.MatchString(target) || strings.HasPrefix(target, "//") {
		return ""
	}
	p, frag, _ := strings.Cut(target, "#")
	p, _, _ = strings.Cut(p, "?")
	if u, err := url.PathUnescape(p); err == nil {
		p = u
	}
	to := from
	if p != "" {
		to = resolve(from, p)
		if to == ".." || strings.HasPrefix(to, "../") || !s.managed(to) {
			return ""
		}
		if !s.stands(to) {
			return fmt.Sprintf("the link to %s reaches nothing: %s stands nowhere among the managed files", target, to)
		}
	}
	if frag == "" {
		return ""
	}
	tf, ok := s.files[to]
	if !ok || !tf.Held || (path.Ext(to) != ".md" && path.Ext(to) != ".markdown") {
		return ""
	}
	if !anchors(tf.Content)[strings.ToLower(frag)] {
		return fmt.Sprintf("the link to %s reaches nothing: %s has no heading or anchor #%s", target, to, frag)
	}
	return ""
}

// anchors answers every fragment a Markdown file answers: each heading's
// slug as GitHub writes it (lowercase, punctuation dropped, spaces as
// dashes, a repeated one numbered from -1), and each HTML name or id.
func anchors(content string) map[string]bool {
	ls := lines(content)
	code := fenced(ls)
	seen := map[string]int{}
	have := map[string]bool{}
	for i, l := range ls {
		if code[i] {
			continue
		}
		for _, m := range anchorName.FindAllStringSubmatch(l, -1) {
			have[strings.ToLower(m[1])] = true
		}
		m := heading.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		slug := slugOf(m[2])
		if n := seen[slug]; n > 0 {
			have[fmt.Sprintf("%s-%d", slug, n)] = true
		} else {
			have[slug] = true
		}
		seen[slug]++
	}
	return have
}

// slugOf is a heading's slug as GitHub writes it.
func slugOf(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(text)) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127 && !isPunct(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isPunct says whether a rune above ASCII is punctuation GitHub drops from a slug.
func isPunct(r rune) bool {
	return strings.ContainsRune("–—‘’“”…«»·", r)
}
