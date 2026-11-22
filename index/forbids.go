package index

import (
	"reflect"
	"regexp"
	"runtime"
	"strings"
)

// Forbidden is an I/O component's forbid list: what it replaces, which only
// its own code may use. No other component's code may use an entry; the scan
// before a commit, the landing's suite and CI refuse one that does (package
// forbid, README.md "Forbid lists"), the catalog check at start does not.
type Forbidden struct {
	// Calls are functions, a Go <import path>.<name> such as time.Now or
	// os/exec.Command, or a Python <module>.<name> such as time.sleep or
	// datetime.now.
	Calls []string
	// Imports are packages, a Go import path or a Python module, and every
	// one below it: github.com/google/go-github.
	Imports []string
	// Commands are programs run by name, named in a string: gh.
	Commands []string
	// Hosts are hosts named in a string: api.github.com.
	Hosts []string
	// On, where set, is the config key of the I/O component whose value names
	// the folders the entries are forbidden on, such as the files module's
	// folders: another component may use an entry where it declares the
	// folders it writes (Writes) and none of them is one of those. Empty, the
	// entries are forbidden everywhere.
	On string
}

// Empty says whether the list names nothing.
func (f Forbidden) Empty() bool {
	return len(f.Calls)+len(f.Imports)+len(f.Commands)+len(f.Hosts) == 0
}

// Kinds are the list's entries by kind, in the order calls, imports,
// commands, hosts, each with its kind's key as component.toml spells it.
func (f Forbidden) Kinds() []ForbiddenKind {
	return []ForbiddenKind{{"calls", f.Calls}, {"imports", f.Imports}, {"commands", f.Commands}, {"hosts", f.Hosts}}
}

// ForbiddenKind is one kind of a forbid list's entries: its key and its entries.
type ForbiddenKind struct {
	Key     string
	Entries []string
}

var (
	forbiddenCall    = regexp.MustCompile(`^[A-Za-z0-9_./-]*[A-Za-z0-9_]\.[A-Za-z_][A-Za-z0-9_]*$`)
	forbiddenImport  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)
	forbiddenCommand = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.+-]*$`)
	forbiddenHost    = regexp.MustCompile(`^[a-z0-9-]+(\.[a-z0-9-]+)+$`)
)

// problems answers what is wrong with the list's entries, each naming its kind.
func (f Forbidden) problems() []string {
	var out []string
	check := func(key string, entries []string, re *regexp.Regexp, want string) {
		seen := map[string]bool{}
		for _, e := range entries {
			switch {
			case !re.MatchString(e):
				out = append(out, "forbids."+key+": "+quote(e)+" is no "+want)
			case seen[e]:
				out = append(out, "forbids."+key+": "+quote(e)+" is named twice")
			}
			seen[e] = true
		}
	}
	check("calls", f.Calls, forbiddenCall, "call: <package>.<name>, such as time.Now")
	check("imports", f.Imports, forbiddenImport, "import: a package path, such as github.com/google/go-github")
	check("commands", f.Commands, forbiddenCommand, "command: a program's name, such as gh")
	check("hosts", f.Hosts, forbiddenHost, "host: a lowercase DNS name, such as api.github.com")
	if f.Empty() {
		out = append(out, "forbids names nothing: list the calls, imports, commands or hosts only this I/O component may use")
	}
	return out
}

func quote(s string) string { return `"` + s + `"` }

type forbidsPart struct{ f Forbidden }

func (p forbidsPart) declare(c *Component) {
	if c.forbids != nil {
		c.fail("forbids is declared twice")
		return
	}
	f := p.f
	c.forbids = &f
}

// Forbids declares an I/O component's forbid list, what it replaces: the
// calls, imports, commands and hosts only its own code may use. A component
// without the I/O flag that declares one is refused.
func Forbids(f Forbidden) Part { return forbidsPart{f} }

// writesPart declares the folders a component writes with direct file calls.
type writesPart struct{ folders []string }

func (p writesPart) declare(c *Component) {
	if c.writes != nil {
		c.fail("writes is declared twice")
		return
	}
	c.writes = append([]string{}, p.folders...)
}

// Writes declares the folders, relative to the checkout and slash-separated,
// an I/O component writes with direct file calls, such as the session
// record's .se/.runtime/sessions: an entry a forbid list forbids only on some
// folders (Forbidden.On) passes in its code while none of them is one.
func Writes(folders ...string) Part { return writesPart{folders} }

// Writes answers the folders the component declares it writes, or none.
func (c *Component) Writes() []string { return append([]string(nil), c.writes...) }

// Forbids answers the component's forbid list, empty where it declares none.
func (c *Component) Forbids() Forbidden {
	if c.forbids == nil {
		return Forbidden{}
	}
	return *c.forbids
}

// Package is the Go import path of the package that declared the component,
// such as kern/components/clock, which the scan reads as the folder its own
// code lies in; empty for a component declared by its component.toml.
func (c *Component) Package() string { return c.pkg }

// checkForbids refuses a forbid list on a component without the I/O flag, and
// a broken entry.
func (c *Component) checkForbids() {
	if c.writes != nil {
		switch {
		case !c.io:
			c.fail("component %s declares the folders it writes but carries no I/O flag: a component that writes files reaches the outside", c.name)
		case len(c.writes) == 0:
			c.fail("writes names no folder")
		}
		for _, f := range c.writes {
			if !writtenFolder(f) {
				c.fail("writes: %s is no folder relative to the checkout, slash-separated, such as .se/.runtime/sessions", quote(f))
			}
		}
	}
	if c.forbids == nil {
		return
	}
	if !c.io {
		c.fail("component %s forbids what it replaces but carries no I/O flag: only an I/O component owns a forbid list", c.name)
		return
	}
	for _, p := range c.forbids.problems() {
		c.fail("%s", p)
	}
	if on := c.forbids.On; on != "" {
		if k, ok := c.config[on]; !ok || k.Type != reflect.TypeFor[[]string]() {
			c.fail("forbids.on: %s is no config key of the component's that lists folders: declare it as a list of strings", quote(on))
		}
	}
}

// writtenFolder says whether f is a folder relative to the checkout,
// slash-separated, that climbs out of it nowhere.
func writtenFolder(f string) bool {
	if f == "" || strings.HasPrefix(f, "/") || strings.Contains(f, "\\") || strings.Contains(f, ":") {
		return false
	}
	for _, seg := range strings.Split(f, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// callerPackage answers the import path of the package whose function is skip
// frames above it, read from the function's name: kern/components/clock.New
// is of kern/components/clock.
func callerPackage(skip int) string {
	pc, _, _, ok := runtime.Caller(skip + 1)
	if !ok {
		return ""
	}
	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return ""
	}
	return packageOf(fn.Name())
}

// packageOf answers the import path a function's full name starts with: the
// text to the first dot after its last slash.
func packageOf(name string) string {
	slash := strings.LastIndex(name, "/")
	dot := strings.Index(name[slash+1:], ".")
	if dot < 0 {
		return name
	}
	return name[:slash+1+dot]
}
