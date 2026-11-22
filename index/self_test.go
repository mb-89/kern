package index_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"kern/index"
)

// box is an I/O component whose own command asks its own verb file through
// its request port file, bound to that verb where the wiring binds it to nothing.
func box(served *[]string) *index.Component {
	file := index.Request[string, int]("file", "Files an item through the box's own verb.").Self("file")
	return index.Declare("box", "The box: files items.", index.IO(), file,
		index.Verb("file", "Files an item.", func(_ context.Context, item string) (int, error) {
			*served = append(*served, "box "+item)
			return len(*served), nil
		}),
		index.Command(index.CommandDecl{Name: "box file", Usage: "box file ITEM", Summary: "File an item.", Help: "Files it.", Group: "Box", Class: index.Batch, Writes: true, MCP: "Files an item."},
			func(_ struct{}, args []string) (index.Result, error) {
				return index.Result{Requests: []index.Ask{file.Ask(args[0])}, Then: func(a []any) (index.Result, error) {
					return index.Result{Text: fmt.Sprintf("filed %d", file.Answer(a[0]))}, nil
				}}, nil
			}),
	)
}

// other is a second I/O component with the same verb, which a wiring may bind the box's port to instead.
func other(served *[]string) *index.Component {
	return index.Declare("other", "Another box.", index.IO(),
		index.Verb("file", "Files an item elsewhere.", func(_ context.Context, item string) (int, error) {
			*served = append(*served, "other "+item)
			return 10, nil
		}),
	)
}

// A request port that names a verb of its own component is bound to it where
// the wiring binds the port to nothing; the call still goes through the
// core's delivery, and the catalog shows the binding. A wire binds it elsewhere.
func TestARequestPortBindsItsOwnVerbWhereTheWiringBindsNone(t *testing.T) {
	var served []string
	w, err := index.ParseWiring([]byte("[instances.box]\ncomponent = \"box\"\n\n[instances.other]\ncomponent = \"other\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.Start(w, box(&served), other(&served))
	if err != nil {
		t.Fatalf("the index refuses a self-bound request port without a wire: %v", err)
	}
	r, err := ix.Call("box file", "one")
	if err != nil || r.Text != "filed 1" || strings.Join(served, ";") != "box one" || len(r.Requests) != 1 {
		t.Fatalf("box file answers %+v, %v, the verbs served %v, and wants the box's own verb to file one", r, err, served)
	}
	c := ix.Catalog()
	var verb string
	for _, in := range c.Instances {
		if in.Name == "box" {
			verb = in.Requests[0].Verb
		}
	}
	if verb != "box.file" {
		t.Fatalf("the catalog shows box.file bound to %q, and wants box.file, its own verb", verb)
	}

	served = nil
	w, err = index.ParseWiring([]byte("[instances.box]\ncomponent = \"box\"\n\n[instances.other]\ncomponent = \"other\"\n\n[wires]\n\"box.file\" = \"other.file\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	ix, err = index.Start(w, box(&served), other(&served))
	if err != nil {
		t.Fatal(err)
	}
	if r, err := ix.Call("box file", "two"); err != nil || r.Text != "filed 10" || strings.Join(served, ";") != "other two" {
		t.Fatalf("box file answers %+v, %v, the verbs served %v, and wants the wire's verb, other.file", r, err, served)
	}
}

// Self names a verb its own component declares; one it lacks is refused.
func TestASelfBindingNamesAVerbOfItsOwn(t *testing.T) {
	file := index.Request[string, int]("file", "Files.").Self("post")
	c := index.Declare("box", "The box.", index.IO(), file,
		index.Verb("file", "Files an item.", func(_ context.Context, item string) (int, error) { return 0, nil }))
	w, _ := index.ParseWiring([]byte("[instances.box]\ncomponent = \"box\"\n"))
	if _, err := index.Start(w, c); err == nil || !strings.Contains(err.Error(), "request port box.file names its own verb post, which component box does not declare") {
		t.Fatalf("the index answers %v, and wants a self-binding to a verb the component lacks refused", err)
	}
}
