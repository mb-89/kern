package host

import (
	"path/filepath"
	"strings"
)

// Writes answers the files the tool call writes, each absolute: relative
// paths resolve against the call's working directory, else root. A tool that
// writes no file, or one whose target it cannot be sure of, answers none, so
// a guard refuses only what it knows a call writes (README.md "Agent tools").
//   - Claude Code's Write, Edit and MultiEdit (file_path) and NotebookEdit
//     (notebook_path); the Copilot CLI's create, edit and str_replace_editor
//     (path), but str_replace_editor's view, and apply_patch (the files its
//     patch adds, updates, deletes or moves to).
//   - A shell call (Bash, the Copilot CLI's bash and powershell): the files
//     its command writes, as ShellWrites parses them.
func (t ToolCall) Writes(root string) []string {
	cwd := t.Cwd
	if cwd == "" {
		cwd = root
	}
	var paths []string
	switch t.Tool {
	case "Write", "Edit", "MultiEdit", "NotebookEdit":
		if t.Name == "str_replace_editor" && t.String("command") == "view" {
			return nil
		}
		if t.Name == "apply_patch" {
			return patchWrites(t.String("input")+t.String("patch"), cwd)
		}
		for _, key := range []string{"file_path", "notebook_path", "path"} {
			if p := t.String(key); p != "" {
				paths = append(paths, absPath(cwd, p))
				break
			}
		}
	case "Bash":
		return ShellWrites(t.String("command"), cwd)
	}
	return paths
}

// patchWrites answers the files an apply_patch patch names: those it adds,
// updates or deletes, and where it moves one to.
func patchWrites(patch, cwd string) []string {
	var paths []string
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimSpace(line)
		for _, head := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "} {
			if p, ok := strings.CutPrefix(line, head); ok && strings.TrimSpace(p) != "" {
				paths = append(paths, absPath(cwd, strings.TrimSpace(p)))
			}
		}
	}
	return paths
}

// absPath answers p absolute: as it is where it is absolute or starts at a
// separator, else under cwd.
func absPath(cwd, p string) string {
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return filepath.Clean(filepath.FromSlash(p))
	}
	return filepath.Join(cwd, filepath.FromSlash(p))
}

// shellWord is one shell word: its text with the quotes taken off, and whether it
// is sure, holding no expansion the parser does not run (a variable, a
// command substitution, a glob, a leading ~).
type shellWord struct {
	text string
	sure bool
	op   bool // an operator: ; && || | & ( ) or a redirection
}

// ShellWrites answers the files a shell command writes, each absolute,
// relative ones resolved against cwd and any cd before them in the command:
// the targets of its output redirections (but /dev/ ones), and the files its
// writing commands name: tee, cp, mv (its sources too), install, ln, rm,
// unlink, touch, truncate, shred, dd's of=, sed -i and perl -i, and
// PowerShell's Set-Content, Add-Content, Out-File, Clear-Content, New-Item,
// Remove-Item, Copy-Item and Move-Item. A heredoc's body is no command. A
// target that is not sure is left out: the parser refuses only what it knows.
func ShellWrites(command, cwd string) []string {
	var paths []string
	var cmd []shellWord
	flush := func() {
		paths = append(paths, commandWrites(cmd, &cwd)...)
		cmd = nil
	}
	words := shellWords(command)
	for i := 0; i < len(words); i++ {
		w := words[i]
		if !w.op {
			cmd = append(cmd, w)
			continue
		}
		if target, ok := redirection(w.text); ok {
			if target && i+1 < len(words) && !words[i+1].op {
				i++
				if t := words[i]; t.sure && !strings.HasPrefix(t.text, "/dev/") && !strings.HasPrefix(t.text, "&") {
					paths = append(paths, absPath(cwd, t.text))
				}
			} else if !target && i+1 < len(words) && !words[i+1].op {
				i++ // an input's source, or a heredoc's delimiter
			}
			continue
		}
		flush()
	}
	flush()
	return paths
}

// redirection says whether op is a redirection, and whether its target is
// written: >, >>, >|, &>, &>>, N> and N>> are; <, <<, <<< and N>&M (a
// duplication, whose target stands in the operator) are not.
func redirection(op string) (target, ok bool) {
	op = strings.TrimLeft(op, "0123456789")
	switch op {
	case ">", ">>", ">|", "&>", "&>>":
		return true, true
	case "<", "<<", "<<-", "<<<", "<>":
		return false, true
	}
	if strings.HasPrefix(op, ">&") || strings.HasPrefix(op, "<&") {
		return false, true
	}
	return false, false
}

// commandWrites answers the files one simple command writes, past its leading
// assignments and wrappers; a cd moves cwd for the commands after it.
func commandWrites(cmd []shellWord, cwd *string) []string {
	for len(cmd) > 0 && (strings.Contains(cmd[0].text, "=") && !strings.HasPrefix(cmd[0].text, "=") || wrappers[cmd[0].text]) {
		cmd = cmd[1:]
	}
	if len(cmd) == 0 || !cmd[0].sure {
		return nil
	}
	name, args := cmd[0].text, cmd[1:]
	at := func(ws []shellWord) []string {
		var out []string
		for _, w := range ws {
			if w.sure {
				out = append(out, absPath(*cwd, w.text))
			}
		}
		return out
	}
	switch strings.ToLower(name) {
	case "cd":
		if pos := operands(args, nil); len(pos) == 1 && pos[0].sure {
			*cwd = absPath(*cwd, pos[0].text)
		}
		return nil
	case "tee", "rm", "unlink", "touch", "shred":
		return at(operands(args, nil))
	case "truncate":
		return at(operands(args, map[string]bool{"-s": true, "--size": true, "-r": true, "--reference": true}))
	case "cp", "install", "ln":
		if pos := operands(args, map[string]bool{"-t": true, "--target-directory": true, "-m": true, "--mode": true, "-o": true, "-g": true, "-S": true, "--suffix": true}); len(pos) >= 2 && !hasFlag(args, "-t", "--target-directory") {
			return at(pos[len(pos)-1:])
		}
	case "mv":
		if pos := operands(args, map[string]bool{"-t": true, "--target-directory": true, "-S": true, "--suffix": true}); len(pos) >= 2 && !hasFlag(args, "-t", "--target-directory") {
			return at(pos)
		}
	case "dd":
		for _, a := range args {
			if p, ok := strings.CutPrefix(a.text, "of="); ok && a.sure {
				return []string{absPath(*cwd, p)}
			}
		}
	case "sed":
		if !inPlace(args, "-i", "--in-place") {
			return nil
		}
		pos := operands(args, map[string]bool{"-e": true, "--expression": true, "-f": true, "--file": true, "-l": true})
		if !hasFlag(args, "-e", "--expression", "-f", "--file") && len(pos) > 0 {
			pos = pos[1:] // the script
		}
		return at(pos)
	case "perl":
		if !inPlace(args, "-i", "") {
			return nil
		}
		return at(operands(args, map[string]bool{"-e": true, "-E": true}))
	case "set-content", "add-content", "out-file", "clear-content", "new-item", "remove-item", "copy-item", "move-item":
		return psWrites(strings.ToLower(name), args, at)
	}
	return nil
}

// wrappers run the command after them: their own words are no command.
var wrappers = map[string]bool{"sudo": true, "env": true, "command": true, "nohup": true, "time": true, "exec": true, "builtin": true}

// operands answers the words that are no flag, past each flag that takes a
// value (valued) and that value; a -- ends the flags.
func operands(args []shellWord, valued map[string]bool) []shellWord {
	var out []shellWord
	flags := true
	for i := 0; i < len(args); i++ {
		a := args[i].text
		switch {
		case flags && a == "--":
			flags = false
		case flags && strings.HasPrefix(a, "-") && len(a) > 1:
			if valued[a] {
				i++
			}
		default:
			out = append(out, args[i])
		}
	}
	return out
}

// hasFlag says whether one of the flags stands among args, alone or, for a
// long flag, with its value after =.
func hasFlag(args []shellWord, flags ...string) bool {
	for _, a := range args {
		for _, f := range flags {
			if f != "" && (a.text == f || strings.HasPrefix(f, "--") && strings.HasPrefix(a.text, f+"=")) {
				return true
			}
		}
	}
	return false
}

// inPlace says whether a sed's or a perl's flags edit in place: -i, -i.bak,
// or -i among a cluster of short flags such as -pi; or the long flag.
func inPlace(args []shellWord, short, long string) bool {
	for _, a := range args {
		t := a.text
		if long != "" && (t == long || strings.HasPrefix(t, long+"=")) {
			return true
		}
		if strings.HasPrefix(t, "-") && !strings.HasPrefix(t, "--") && strings.Contains(t[1:], short[1:]) {
			if strings.HasPrefix(t, short) || !strings.ContainsAny(t[1:strings.Index(t, short[1:])], "e") {
				return true
			}
		}
	}
	return false
}

// psWrites answers the files a PowerShell cmdlet writes: its -Path,
// -LiteralPath or -FilePath, else its first operand; a copy's -Destination,
// else its second; a move's source and destination both.
func psWrites(name string, args []shellWord, at func([]shellWord) []string) []string {
	var path, dest []shellWord
	var pos []shellWord
	for i := 0; i < len(args); i++ {
		a := strings.ToLower(args[i].text)
		if strings.HasPrefix(a, "-") && i+1 < len(args) {
			switch a {
			case "-path", "-literalpath", "-filepath":
				path = append(path, args[i+1])
			case "-destination":
				dest = append(dest, args[i+1])
			}
			if a != "-force" && a != "-recurse" && a != "-append" && a != "-nonewline" {
				i++
			}
			continue
		}
		if !strings.HasPrefix(a, "-") {
			pos = append(pos, args[i])
		}
	}
	if len(path) == 0 && len(pos) > 0 {
		path, pos = pos[:1], pos[1:]
	}
	if len(dest) == 0 && len(pos) > 0 {
		dest = pos[:1]
	}
	switch name {
	case "copy-item":
		return at(dest)
	case "move-item":
		return at(append(path, dest...))
	}
	return at(path)
}

// shellWords splits a command into words and operators: quotes are taken off
// (single quotes keep everything, double quotes keep all but \ before $ ` " \),
// a backslash escapes the next character, and a heredoc's body, from the line
// after its operator to its delimiter, is dropped.
func shellWords(s string) []shellWord {
	var words []shellWord
	var cur strings.Builder
	in, sure := false, true
	var heredocs []string
	end := func() {
		if in {
			words = append(words, shellWord{text: cur.String(), sure: sure})
		}
		cur.Reset()
		in, sure = false, true
	}
	op := func(text string) {
		end()
		words = append(words, shellWord{text: text, op: true})
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			in = true
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				j = len(s) - i - 1
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 1
		case c == '"':
			in = true
			for i++; i < len(s) && s[i] != '"'; i++ {
				switch {
				case s[i] == '\\' && i+1 < len(s) && strings.IndexByte("$`\"\\\n", s[i+1]) >= 0:
					i++
					cur.WriteByte(s[i])
				case s[i] == '$' || s[i] == '`':
					sure = false
					cur.WriteByte(s[i])
				default:
					cur.WriteByte(s[i])
				}
			}
		case c == '\\' && i+1 < len(s):
			in = true
			i++
			if s[i] != '\n' {
				cur.WriteByte(s[i])
			}
		case c == ' ' || c == '\t' || c == '\r':
			end()
		case c == '\n':
			end()
			if len(heredocs) > 0 {
				i = skipHeredocs(s, i+1, heredocs) - 1
				heredocs = nil
			}
			op("\n")
		case c == '#' && !in:
			for i < len(s) && s[i] != '\n' {
				i++
			}
			i--
		case strings.IndexByte(";&|()", c) >= 0:
			if c == '&' && i+1 < len(s) && s[i+1] == '>' {
				j := i + 2
				if j < len(s) && s[j] == '>' {
					j++
				}
				op(s[i:j])
				i = j - 1
				continue
			}
			j := i + 1
			if j < len(s) && (c == '&' || c == '|') && s[j] == c {
				j++
			}
			op(s[i:j])
			i = j - 1
		case c == '>' || c == '<':
			digits := ""
			if in && !strings.ContainsFunc(cur.String(), func(r rune) bool { return r < '0' || r > '9' }) {
				digits = cur.String()
				cur.Reset()
				in = false
			}
			end()
			j := i + 1
			for j < len(s) && strings.IndexByte("<>|-", s[j]) >= 0 && j-i < 3 {
				if s[j] == '-' && s[j-1] != '<' {
					break
				}
				j++
			}
			if j < len(s) && s[j] == '&' {
				j++
				for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == '-') {
					j++
				}
			}
			text := digits + s[i:j]
			words = append(words, shellWord{text: text, op: true})
			if strings.HasPrefix(strings.TrimLeft(text, "0123456789"), "<<") && !strings.HasPrefix(strings.TrimLeft(text, "0123456789"), "<<<") {
				k := j
				for k < len(s) && (s[k] == ' ' || s[k] == '\t') {
					k++
				}
				delim := heredocDelimiter(s[k:])
				heredocs = append(heredocs, delim)
			}
			i = j - 1
		default:
			in = true
			if strings.IndexByte("$`*?[", c) >= 0 || c == '~' && cur.Len() == 0 {
				sure = false
			}
			cur.WriteByte(c)
		}
	}
	end()
	return words
}

// heredocDelimiter answers the delimiter a heredoc's operator is followed by,
// its quotes taken off.
func heredocDelimiter(s string) string {
	end := strings.IndexAny(s, " \t\n;&|<>()")
	if end < 0 {
		end = len(s)
	}
	return strings.NewReplacer(`'`, "", `"`, "", `\`, "").Replace(s[:end])
}

// skipHeredocs answers where the command goes on after the heredocs' bodies
// that start at i: past the line of each delimiter in turn.
func skipHeredocs(s string, i int, delims []string) int {
	for _, d := range delims {
		for i < len(s) {
			j := strings.IndexByte(s[i:], '\n')
			line := s[i:]
			if j >= 0 {
				line = s[i : i+j]
			}
			if j < 0 {
				i = len(s)
			} else {
				i += j + 1
			}
			if strings.TrimLeft(line, "\t") == d {
				break
			}
		}
	}
	return i
}
