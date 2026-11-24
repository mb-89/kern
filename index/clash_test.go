package index_test

import (
	"context"
	"strings"
	"testing"

	"kern/index"
)

// The catalog refuses a component that declares a port and a verb of one
// name (kern#23): a wire to that name would bind the port, never the verb. The
// refusal names the component, the port and the verb, for a request port, an
// output and an input of the verb's name, and for a component.toml's request.
func TestAPortAndAVerbOfOneNameAreRefusedNamingBoth(t *testing.T) {
	verb := index.Verb("file", "Files an item.", func(_ context.Context, item string) (int, error) { return 0, nil })
	for port, part := range map[string]index.Part{
		"request port": index.Request[string, int]("file", "Files an item through the verb.").Self("file"),
		"output":       index.Output("file", 0, "The items filed."),
	} {
		c := index.Declare("box", "The box.", index.IO(), part, verb)
		w, _ := index.ParseWiring([]byte("[instances.box]\ncomponent = \"box\"\n"))
		_, err := index.Start(w, c)
		want := "component box declares the " + port + " file and the verb file, of one name"
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("the index answers %v for the %s and a verb of one name, and wants %q", err, port, want)
		}
	}

	c := index.Declare("box", "The box.", index.IO(), verb,
		index.Derived("count", 0, "The items read.", func(in struct {
			File int `index:"file"`
		}) (int, error) {
			return in.File, nil
		}))
	w, _ := index.ParseWiring([]byte("[instances.box]\ncomponent = \"box\"\n\n[wires]\n\"box.file\" = \"built-in\"\n"))
	if _, err := index.Start(w, c); err == nil || !strings.Contains(err.Error(), "component box declares the input file and the verb file, of one name") {
		t.Fatalf("the index answers %v for an input and a verb of one name, and wants both named", err)
	}

	toml, err := index.ParseDeclaration([]byte(`name = "desk"
contract = 1
io = true
help = "Answers questions."

[requests.ask]
type = "string"
answer = "string"
help = "Asks the desk's own verb."

[verbs.ask]
type = "string"
answer = "string"
help = "Answers a question."
`))
	if err != nil {
		t.Fatal(err)
	}
	w, _ = index.ParseWiring([]byte("[instances.desk]\ncomponent = \"desk\"\n\n[wires]\n\"desk.ask\" = \"desk.ask\"\n"))
	if _, err := index.Start(w, toml); err == nil || !strings.Contains(err.Error(), "component desk declares the request port ask and the verb ask, of one name: a wire to ask would bind the request port, never the verb") {
		t.Fatalf("the index answers %v for a component.toml's request and verb of one name, and wants both named", err)
	}
}
