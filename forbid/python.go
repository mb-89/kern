package forbid

import "strings"

// pyToken is one token of a Python file as the scan reads it: a name, a dot,
// a string's text, the end of a statement, or anything else.
type pyToken struct {
	kind byte // 'n' name, '.' dot, 's' string, ';' a statement's end, 'o' other
	text string
	line int
}

// pyLex reads a Python file's tokens: comments are dropped, a string's text is
// kept without its quotes and escapes resolved only so far as to find its end,
// and a newline outside brackets ends a statement, as does a semicolon.
func pyLex(src string) []pyToken {
	var out []pyToken
	line, depth := 1, 0
	emit := func(kind byte, text string, at int) { out = append(out, pyToken{kind, text, at}) }
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '\n':
			if depth == 0 {
				emit(';', "", line)
			}
			line++
			i++
		case c == '\\' && i+1 < len(src) && src[i+1] == '\n':
			line++
			i += 2
		case c == ' ' || c == '\t' || c == '\r' || c == '\f':
			i++
		case c == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '"' || c == '\'':
			at := line
			text, n, lines := pyString(src[i:])
			emit('s', text, at)
			line += lines
			i += n
		case pyNameStart(c):
			j := i
			for j < len(src) && pyNameByte(src[j]) {
				j++
			}
			word := src[i:j]
			if j < len(src) && (src[j] == '"' || src[j] == '\'') && pyPrefix(word) {
				at := line
				text, n, lines := pyString(src[j:])
				emit('s', text, at)
				line += lines
				i = j + n
				continue
			}
			emit('n', word, line)
			i = j
		case c >= '0' && c <= '9':
			for i < len(src) && (pyNameByte(src[i]) || src[i] == '.') {
				i++
			}
			emit('o', "", line)
		case c == '.':
			emit('.', ".", line)
			i++
		case c == ';':
			emit(';', "", line)
			i++
		default:
			switch c {
			case '(', '[', '{':
				depth++
			case ')', ']', '}':
				if depth > 0 {
					depth--
				}
			}
			emit('o', string(c), line)
			i++
		}
	}
	return append(out, pyToken{';', "", line})
}

func pyNameStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func pyNameByte(c byte) bool { return pyNameStart(c) || c >= '0' && c <= '9' }

// pyPrefix says whether a name directly before a quote is a string's prefix: r, b, u, f, t and their pairs.
func pyPrefix(w string) bool {
	if len(w) > 2 {
		return false
	}
	for _, c := range strings.ToLower(w) {
		if !strings.ContainsRune("rbuft", c) {
			return false
		}
	}
	return true
}

// pyString reads the string at the start of s, single or triple quoted, and
// answers its text, the bytes it spans and the newlines in it.
func pyString(s string) (text string, n, lines int) {
	q := s[:1]
	if strings.HasPrefix(s, q+q+q) {
		q = q + q + q
	}
	var b strings.Builder
	i := len(q)
	for i < len(s) {
		if strings.HasPrefix(s[i:], q) {
			return b.String(), i + len(q), lines
		}
		c := s[i]
		if c == '\n' {
			lines++
			if len(q) == 1 {
				return b.String(), i, lines - 1 // an unclosed string ends at its line
			}
		}
		if c == '\\' && i+1 < len(s) {
			if s[i+1] == '\n' {
				lines++
			}
			b.WriteByte(c)
			b.WriteByte(s[i+1])
			i += 2
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String(), i, lines
}

// pyUses answers what a Python file uses that a list may name: each module it
// imports, each dotted name that reads a forbidden call (time.sleep, a name
// imported from time as sleep, datetime.now on datetime's class), and each
// string's commands and hosts.
func pyUses(_ string, src []byte, owners []Owner) ([]use, error) {
	calls := entries(owners, "calls")
	var out []use
	alias := map[string]string{} // a name the file binds by an import, to what it stands for
	toks := pyLex(string(src))
	for start := 0; start < len(toks); {
		end := start
		for end < len(toks) && toks[end].kind != ';' {
			end++
		}
		stmt := toks[start:end]
		start = end + 1
		if len(stmt) == 0 {
			continue
		}
		if stmt[0].kind == 'n' && (stmt[0].text == "import" || stmt[0].text == "from") {
			out = append(out, pyImport(stmt, alias, owners)...)
			continue
		}
		for i := 0; i < len(stmt); i++ {
			t := stmt[i]
			if t.kind == 's' {
				out = append(out, stringUses(t.line, t.text, owners)...)
				continue
			}
			if t.kind != 'n' || (i > 0 && stmt[i-1].kind == '.') {
				continue
			}
			parts := []string{t.text}
			for i+2 < len(stmt) && stmt[i+1].kind == '.' && stmt[i+2].kind == 'n' {
				parts = append(parts, stmt[i+2].text)
				i += 2
			}
			if a, ok := alias[parts[0]]; ok {
				parts[0] = a
			}
			chain := strings.Join(parts, ".")
			for _, e := range calls {
				if chain == e || strings.HasPrefix(chain, e+".") || strings.HasSuffix(chain, "."+e) {
					out = append(out, use{t.line, "call", e})
				}
			}
		}
	}
	return out, nil
}

// pyImport reads one import statement: its modules' uses of an import entry,
// and the names it binds into alias.
func pyImport(stmt []pyToken, alias map[string]string, owners []Owner) []use {
	var out []use
	// dotted reads a dotted name from stmt[i:], answering it and the index after it.
	dotted := func(i int) (string, int) {
		var parts []string
		for i < len(stmt) && stmt[i].kind == 'n' {
			parts = append(parts, stmt[i].text)
			if i+1 < len(stmt) && stmt[i+1].kind == '.' {
				i += 2
				continue
			}
			i++
			break
		}
		return strings.Join(parts, "."), i
	}
	// as reads an optional "as <name>" at stmt[i:].
	as := func(i int) (string, int) {
		if i+1 < len(stmt) && stmt[i].kind == 'n' && stmt[i].text == "as" && stmt[i+1].kind == 'n' {
			return stmt[i+1].text, i + 2
		}
		return "", i
	}
	if stmt[0].text == "import" {
		for i := 1; i < len(stmt); {
			mod, j := dotted(i)
			if mod == "" {
				i++
				continue
			}
			out = append(out, importUses(stmt[i].line, mod, ".", owners)...)
			name, k := as(j)
			if name != "" {
				alias[name] = mod
			} else {
				top := strings.SplitN(mod, ".", 2)[0]
				alias[top] = top
			}
			i = k
		}
		return out
	}
	i := 1
	if i < len(stmt) && stmt[i].kind == '.' {
		return nil // a relative import reaches only the component's own modules
	}
	mod, i := dotted(i)
	if mod == "" {
		return nil
	}
	out = append(out, importUses(stmt[1].line, mod, ".", owners)...)
	if i < len(stmt) && stmt[i].kind == 'n' && stmt[i].text == "import" {
		i++
	}
	for i < len(stmt) {
		if stmt[i].kind != 'n' {
			i++
			continue
		}
		n := stmt[i].text
		name, j := as(i + 1)
		if name == "" {
			name = n
		}
		alias[name] = mod + "." + n
		i = j
	}
	return out
}
