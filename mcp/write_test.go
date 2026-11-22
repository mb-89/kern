package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"kern/host"
	"kern/index"
)

// desk is a declared component with a read-only command, a writing one that
// files through its request port post, and one that is neither.
const desk = `name = "desk"
contract = 1
help = "The desk: what waits on it."

[commands."desk list"]
usage = "desk list [--all]"
summary = "list the desk"
group = "Desk"
help = "list the desk"
class = "interactive"
read-only = true
mcp = "Lists what waits on the desk."

[commands."desk file"]
usage = "desk file TITLE"
summary = "file an item"
group = "Desk"
help = "file an item"
class = "batch"
writes = true
mcp = "Files one item on the desk; use it to file what you rendered."

[commands."desk sync"]
usage = "desk sync"
summary = "sync the desk"
group = "Desk"
help = "sync the desk"
class = "interactive"
mcp = "Syncs the checkout; it writes past its requests, so it is declared neither."

[requests.post]
type = "string"
answer = "int"
help = "Posts an item."
`

// postBox is the I/O component desk's request port is wired to.
const postBox = `name = "post"
contract = 1
io = true
help = "The post box."

[verbs.file]
type = "string"
answer = "int"
help = "Files an item."
`

// writingCatalog collects the declarations under the wiring's instances and wires.
func writingCatalog(t *testing.T, decl, wiring string) (index.Catalog, error) {
	t.Helper()
	comp, err := index.ParseDeclaration([]byte(decl))
	if err != nil {
		t.Fatal(err)
	}
	post, err := index.ParseDeclaration([]byte(postBox))
	if err != nil {
		t.Fatal(err)
	}
	w, err := index.ParseWiring([]byte(wiring))
	if err != nil {
		t.Fatal(err)
	}
	return index.Collect(w, comp, post)
}

const deskWiring = "[instances.desk]\ncomponent = \"desk\"\n\n[instances.post]\ncomponent = \"post\"\n\n[wires]\n\"desk.post\" = \"post.file\"\n"

// A command declared writing is served as the instance's writing tool,
// <tool>_write, beside its read-only tool: tools/list marks it as writing
// (not read-only, destructive, not idempotent), and its call runs the command
// through the core, as every command tool's does.
func TestAWritingCommandIsServedAsAWritingTool(t *testing.T) {
	c, err := writingCatalog(t, desk, deskWiring)
	if err != nil {
		t.Fatal(err)
	}
	core := &fakeCore{running: true, answers: map[string]host.CallAnswer{"desk file": {Text: "filed item 3"}}}
	o := options(core, c)
	o.Greeting = nil
	cl := open(t, o)
	cl.initialize()
	var l listed
	if err := json.Unmarshal(cl.ask("tools/list", map[string]any{}), &l); err != nil {
		t.Fatal(err)
	}
	byName := map[string]int{}
	var names []string
	for i, tool := range l.Tools {
		byName[tool.Name] = i
		names = append(names, tool.Name)
	}
	if strings.Join(names, " ") != "desk desk_write status" {
		t.Fatalf("tools/list answers %v, and wants desk, desk_write and status", names)
	}
	read, write := l.Tools[byName["desk"]], l.Tools[byName["desk_write"]]
	if !read.Annotations.ReadOnly || *read.Annotations.Destructive {
		t.Errorf("desk carries %+v, and wants read-only and not destructive", read.Annotations)
	}
	if write.Annotations.ReadOnly || write.Annotations.Destructive == nil || !*write.Annotations.Destructive || write.Annotations.OpenWorld == nil || !*write.Annotations.OpenWorld {
		t.Errorf("desk_write carries %+v, and wants it marked as writing: not read-only, destructive, open world", write.Annotations)
	}
	if !strings.Contains(write.Description, "it writes, each write a request the core delivers to the I/O verb the wiring binds") ||
		!strings.Contains(write.Description, "`desk file`: Files one item on the desk; use it to file what you rendered. Usage: `desk file TITLE`") ||
		strings.Contains(write.Description, "desk list") || strings.Contains(write.Description, "desk sync") {
		t.Fatalf("desk_write's description is %q, and wants its writing command alone, with its mcp description and usage", write.Description)
	}
	if strings.Contains(read.Description, "desk file") {
		t.Fatalf("desk's description is %q, and wants no writing command in the read-only tool", read.Description)
	}
	enum := write.InputSchema["properties"].(map[string]any)["command"].(map[string]any)["enum"]
	if fmt.Sprint(enum) != "[desk file]" {
		t.Fatalf("desk_write's command enum is %v, and wants [desk file]", enum)
	}
	if r := cl.call("desk", map[string]any{"command": "desk file", "args": []any{"one"}}); !r.IsError {
		t.Fatalf("the read-only tool runs desk file (%+v), and wants it refused: it offers no writing command", r)
	}
	r := cl.call("desk_write", map[string]any{"command": "desk file", "args": []any{"one"}})
	if r.IsError || r.Content[0].Text != "filed item 3" {
		t.Fatalf("desk_write answers %+v, and wants the command's answer", r)
	}
	core.mu.Lock()
	calls := strings.Join(core.calls, "; ")
	core.mu.Unlock()
	if calls != "desk file one" {
		t.Fatalf("the core is called %q, and wants desk file one alone", calls)
	}
	cl.hangUp()
}

// The catalog check refuses a writing command without its mcp line, one of
// an instance with no request to write through, and a writing tool's name
// that another tool takes; a command declared read-only and writing is
// refused by the declaration.
func TestTheCatalogCheckHoldsWritingCommands(t *testing.T) {
	noMCP := strings.Replace(desk, "mcp = \"Files one item on the desk; use it to file what you rendered.\"\n", "", 1)
	c, err := writingCatalog(t, noMCP, deskWiring)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Tools("app", c, nil); err == nil || !strings.Contains(err.Error(), `command "desk file" of instance desk is writing and has no mcp description`) {
		t.Fatalf("Tools answers %v, and wants the writing command without its mcp line refused", err)
	}

	noRequest := strings.Replace(desk, "[requests.post]\ntype = \"string\"\nanswer = \"int\"\nhelp = \"Posts an item.\"\n", "", 1)
	c, err = writingCatalog(t, noRequest, "[instances.desk]\ncomponent = \"desk\"\n\n[instances.post]\ncomponent = \"post\"\n")
	if err != nil {
		t.Fatal(err)
	}
	tools, err := Tools("app", c, nil)
	if err == nil || !strings.Contains(err.Error(), `command "desk file" of instance desk is writing, and the instance has no request to write through`) {
		t.Fatalf("Tools answers %v, and wants a writing command with no request to write through refused", err)
	}
	for _, tool := range tools {
		if tool.Writes {
			t.Fatalf("Tools answers the writing tool %s for a refused command, and wants none", tool.Name)
		}
	}

	// desk-write's read-only tool is desk_write, the name desk's writing tool would take.
	twin := strings.Replace(strings.Replace(desk, `"desk `, `"twin `, -1), `name = "desk"`, `name = "twin"`, 1)
	twin = strings.Replace(twin, "writes = true\n", "", 1)
	comp, _ := index.ParseDeclaration([]byte(desk))
	other, _ := index.ParseDeclaration([]byte(twin))
	post, _ := index.ParseDeclaration([]byte(postBox))
	w, err := index.ParseWiring([]byte(deskWiring + "\"desk-write.post\" = \"post.file\"\n\n[instances.desk-write]\ncomponent = \"twin\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	c, err = index.Collect(w, comp, other, post)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Tools("app", c, nil); err == nil || !strings.Contains(err.Error(), `instance desk offers writing commands, and "desk_write" is no tool name of its own, it is instance desk-write's read-only tool's`) {
		t.Fatalf("Tools answers %v, and wants the writing tool's name another tool takes refused", err)
	}

	both := strings.Replace(desk, "writes = true\n", "writes = true\nread-only = true\n", 1)
	if _, err := index.ParseDeclaration([]byte(both)); err == nil || !strings.Contains(err.Error(), `command "desk file" is declared read-only and writing`) {
		t.Fatalf("the declaration reads %v, and wants a command read-only and writing refused", err)
	}
}
