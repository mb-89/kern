// Package mcp is kern's MCP door: `<name> mcp`, an MCP server over stdio that
// offers the core's read-only commands (index.CommandDecl.ReadOnly) and its
// writing ones (index.CommandDecl.Writes) to an agent host as a few coarse
// tools, generated from the catalog (index.Catalog), never written per tool.
//
// A host spawns its configured stdio servers as a session starts, so the door
// answers initialize and tools/list at once, from the catalog it reads without
// the core (host.Catalog), and starts the core detached, or attaches to the one
// that runs, in the background. It holds a session lease for as long as the
// connection lives and returns it on EOF (kern/README.md "The MCP door").
package mcp

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"kern/index"
)

// The door's own tools, beside one per instance that offers read-only
// commands and one per instance that offers writing commands.
const (
	// StatusTool says whether the core runs and its health.
	StatusTool = "status"
	// GreetingTool answers the application's greeting: the names and
	// commands it names (Options.Greeting).
	GreetingTool = "greeting"
	// WriteSuffix ends the name of an instance's writing tool: <tool>_write.
	WriteSuffix = "_write"
)

// Tool is one tool the door offers, as the catalog makes it.
type Tool struct {
	Name, Title, Description string
	// Instance is the instance a command tool runs the commands of; empty for
	// the door's own tools.
	Instance string
	// Commands are the commands a command tool runs, by name: read-only
	// ones, or writing ones where Writes is set; empty for the door's own tools.
	Commands []index.CommandInfo
	// Writes is set on an instance's writing tool: its commands write, each
	// only through its instance's requests to the I/O verbs the wiring binds.
	Writes bool
	// Greeting are the greeting tool's entries: each a name or a command.
	Greeting []Entry
	// OpenWorld is set where the tool may read outside services: every
	// tool that runs a component's command.
	OpenWorld bool
}

// Entry is one part of the greeting: a name the catalog's instances write,
// or a read-only command it runs without arguments.
type Entry struct {
	Name    string
	Command bool
	Help    string
}

var toolName = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// ToolName is an instance's tool name, in snake_case as hosts name tools:
// its dashes read as underscores.
func ToolName(instance string) string { return strings.ReplaceAll(instance, "-", "_") }

// Tools answers the door's tools for the catalog: status, the greeting where
// the application names one, one tool per switched-on instance that offers
// read-only commands (index.CommandDecl.ReadOnly), named after the instance
// in snake_case (ToolName), and one per switched-on instance that offers
// writing commands (index.CommandDecl.Writes), named the same with
// WriteSuffix. A command declared neither is in no tool, whatever its class.
//
// It is the door's catalog check: it refuses a read-only or writing command
// without an MCP description (index.CommandDecl.MCP), a writing command of an
// instance that has no request to write through, an instance whose tool name
// is none or takes one of the door's own or another tool's, and a greeting
// entry that is a command not declared read-only. A greeting entry the
// catalog lacks is left out, so a checkout that wires less greets with less.
func Tools(app string, c index.Catalog, greeting []string) ([]Tool, error) {
	var errs []string
	enabled := map[string]index.InstanceInfo{}
	outputs := map[string]index.PortInfo{}
	for _, in := range c.Instances {
		if !in.Enabled {
			continue
		}
		enabled[in.Name] = in
		for _, out := range in.Outputs {
			if out.Name != "" && !out.Family {
				outputs[out.Name] = out
			}
		}
	}
	byInstance := map[string][]index.CommandInfo{}
	writing := map[string][]index.CommandInfo{}
	for _, ci := range c.Commands {
		if !ci.ReadOnly && !ci.Writes {
			continue
		}
		what := "read-only"
		if ci.Writes {
			what = "writing"
		}
		if strings.TrimSpace(ci.MCP) == "" {
			errs = append(errs, fmt.Sprintf("command %q of instance %s is %s and has no mcp description", ci.Name, ci.Instance, what))
			continue
		}
		if ci.ReadOnly {
			byInstance[ci.Instance] = append(byInstance[ci.Instance], ci)
			continue
		}
		if len(enabled[ci.Instance].Requests) == 0 {
			errs = append(errs, fmt.Sprintf("command %q of instance %s is writing, and the instance has no request to write through: declare a request port and wire it to an I/O verb", ci.Name, ci.Instance))
			continue
		}
		writing[ci.Instance] = append(writing[ci.Instance], ci)
	}
	tools := []Tool{{
		Name: StatusTool, Title: app + " status",
		Description: fmt.Sprintf("Whether %s's core runs in this checkout, and its health: pid, bus, the sessions holding a lease, the instances and the placed processes. "+
			"Use it when another tool of this server answers \"starting\" or fails, or to see what the core carries; it never starts anything the door has not.", app),
	}}
	var entries []Entry
	for _, e := range greeting {
		if out, ok := outputs[e]; ok {
			entries = append(entries, Entry{Name: e, Help: out.Help})
			continue
		}
		if ci, ok := c.Command(e); ok {
			if _, on := enabled[ci.Instance]; !on {
				continue
			}
			if !ci.ReadOnly {
				errs = append(errs, fmt.Sprintf("greeting entry %q is a command not declared read-only: the greeting only reads", e))
				continue
			}
			entries = append(entries, Entry{Name: e, Command: true, Help: ci.MCP})
		}
	}
	if len(entries) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "%s's start greeting, live from its core: what is due and what is queued. Use it at the start of a session, and whenever you ask what to do next. It reads:", app)
		for _, e := range entries {
			fmt.Fprintf(&b, "\n- `%s`: %s", e.Name, strings.TrimSpace(e.Help))
		}
		tools = append(tools, Tool{Name: GreetingTool, Title: app + " greeting", Description: b.String(), Greeting: entries, OpenWorld: true})
	}
	taken := map[string]string{StatusTool: "the door's own", GreetingTool: "the door's own"}
	for _, n := range sortedKeys(byInstance) {
		name := ToolName(n)
		if by, ok := taken[name]; ok || !toolName.MatchString(name) {
			errs = append(errs, fmt.Sprintf("instance %s offers read-only commands, and %q is no tool name of its own%s: rename it in the wiring", n, name, takenBy(by)))
			continue
		}
		taken[name] = "instance " + n + "'s read-only tool"
		in := enabled[n]
		var b strings.Builder
		fmt.Fprintf(&b, "%s Runs one of its read-only commands, named in `command`, with `args` as its usage shows; it changes nothing. Use it:", strings.TrimSpace(in.Help))
		for _, ci := range byInstance[n] {
			fmt.Fprintf(&b, "\n- `%s`: %s Usage: `%s`", ci.Name, strings.TrimSpace(ci.MCP), oneLine(ci.Usage))
		}
		tools = append(tools, Tool{Name: name, Title: app + " " + n, Description: b.String(), Instance: n, Commands: byInstance[n], OpenWorld: true})
	}
	for _, n := range sortedKeys(writing) {
		name := ToolName(n) + WriteSuffix
		if by, ok := taken[name]; ok || !toolName.MatchString(name) {
			errs = append(errs, fmt.Sprintf("instance %s offers writing commands, and %q is no tool name of its own%s: rename it in the wiring", n, name, takenBy(by)))
			continue
		}
		taken[name] = "instance " + n + "'s writing tool"
		in := enabled[n]
		var b strings.Builder
		fmt.Fprintf(&b, "%s Runs one of its writing commands, named in `command`, with `args` as its usage shows: it writes, each write a request the core delivers to the I/O verb the wiring binds, as %s's every outside write goes. Use it:", strings.TrimSpace(in.Help), app)
		for _, ci := range writing[n] {
			fmt.Fprintf(&b, "\n- `%s`: %s Usage: `%s`", ci.Name, strings.TrimSpace(ci.MCP), oneLine(ci.Usage))
		}
		tools = append(tools, Tool{Name: name, Title: app + " " + n + " (writes)", Description: b.String(), Instance: n, Commands: writing[n], Writes: true, OpenWorld: true})
	}
	if len(errs) > 0 {
		return tools, fmt.Errorf("the MCP door's catalog check: %s", strings.Join(errs, "; "))
	}
	return tools, nil
}

// takenBy says whose tool a name already is, where it is one.
func takenBy(by string) string {
	if by == "" {
		return ""
	}
	return ", it is " + by + "'s"
}

func sortedKeys(m map[string][]index.CommandInfo) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// oneLine joins a usage argparse wrapped onto one line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
