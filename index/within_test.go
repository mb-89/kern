package index_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"kern/index"
)

// gate is an I/O component whose verb accept answers what it accepts and
// refuses free text, as an application's template component does.
var gate = index.Declare("gate", "Accepts what is filled.", index.IO(),
	index.Verb("accept", "Accepts a filled value.", func(_ context.Context, v string) (string, error) {
		filled, ok := strings.CutPrefix(v, "filled:")
		if !ok {
			return "", errors.New("gate: free text, fill it first")
		}
		return "accepted " + filled, nil
	}),
)

// door is an I/O component whose verb write asks the port accept while it
// serves, and writes only what accept answers; the wiring may leave accept
// unbound (index.RequestPort.Optional). Its command asks write through its own
// port. Loop asks its port again, bound back to loop, so it never ends.
func door(wrote *[]string) *index.Component {
	accept := index.Request[string, string]("accept", "Asks the gate whether it accepts the value.").Optional()
	write := index.Request[string, string]("write-self", "Writes through the door's own verb.").Self("write")
	again := index.Request[string, string]("again", "Asks loop once more.").Optional()
	return index.Declare("door", "Writes what the gate accepts.", index.IO(), accept, write, again,
		index.Verb("write", "Writes what the gate accepts.", func(ctx context.Context, v string) (string, error) {
			got, err := accept.Within(ctx, v)
			if err != nil {
				return "", err
			}
			*wrote = append(*wrote, got)
			return "wrote " + got, nil
		}),
		index.Verb("loop", "Asks itself again.", func(ctx context.Context, v string) (string, error) {
			return again.Within(ctx, v)
		}),
		index.Command(index.CommandDecl{Name: "door write", Usage: "door write VALUE", Summary: "Write one.", Help: "Writes it.", Group: "Door", Class: index.Batch, Writes: true, MCP: "Writes."},
			func(_ struct{}, args []string) (index.Result, error) {
				return index.Result{Requests: []index.Ask{write.Ask(args[0])}, Then: func(a []any) (index.Result, error) {
					return index.Result{Text: write.Answer(a[0])}, nil
				}}, nil
			}),
		index.Command(index.CommandDecl{Name: "door ask", Usage: "door ask VALUE", Summary: "Ask the gate.", Help: "Asks it.", Group: "Door", Class: index.Batch},
			func(_ struct{}, args []string) (index.Result, error) {
				return index.Result{Requests: []index.Ask{accept.Ask(args[0])}}, nil
			}),
	)
}

func doorIndex(t *testing.T, wires string, wrote *[]string) *index.Index {
	t.Helper()
	w, err := index.ParseWiring([]byte("[instances.door]\ncomponent = \"door\"\n\n[instances.gate]\ncomponent = \"gate\"\n\n[wires]\n" + wires))
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.Start(w, door(wrote), gate)
	if err != nil {
		t.Fatalf("the index refuses the door's wiring: %v", err)
	}
	return ix
}

// A verb asks a request port of its own component while it serves, as a
// command asks one in its result: the core delivers it to the verb the wiring
// binds, and a refusal there is the verb's, so the verb writes nothing.
func TestAVerbAsksItsComponentsRequestPortWhileItServes(t *testing.T) {
	var wrote []string
	ix := doorIndex(t, `"door.accept" = "gate.accept"`+"\n", &wrote)
	r, err := ix.Call("door write", "filled:one")
	if err != nil || r.Text != "wrote accepted one" || strings.Join(wrote, ";") != "accepted one" {
		t.Fatalf("door write answers %q, %v, wrote %v, and wants the gate's answer written", r.Text, err, wrote)
	}
	_, err = ix.Call("door write", "free text")
	if err == nil || !strings.Contains(err.Error(), "gate: free text, fill it first") || len(wrote) != 1 {
		t.Fatalf("free text answers %v and wrote %v, and wants the gate's refusal with nothing written", err, wrote)
	}
}

// The wiring may leave an optional request port unbound: the index starts,
// and an ask on it, from a verb or from a command, is refused naming the wire
// that binds it.
func TestAnOptionalRequestPortLeftUnboundIsRefusedNamingItsWire(t *testing.T) {
	var wrote []string
	ix := doorIndex(t, "", &wrote)
	want := `request port door.accept is bound to nothing: the wiring binds it, "door.accept" = "<instance>.<verb>"`
	if _, err := ix.Call("door write", "filled:one"); err == nil || !strings.Contains(err.Error(), want) || len(wrote) != 0 {
		t.Fatalf("a verb's ask on the unbound port answers %v and wrote %v, and wants %q", err, wrote, want)
	}
	if _, err := ix.Call("door ask", "filled:one"); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("a command's ask on the unbound port answers %v, and wants %q", err, want)
	}
	if c := ix.Catalog(); c.Instances[0].Name != "door" || c.Instances[0].Requests[0].Port != "accept" || c.Instances[0].Requests[0].Verb != "" {
		t.Fatalf("the catalog shows %+v, and wants door.accept bound to nothing", c.Instances[0].Requests)
	}
}

// A verb served outside a call of the core has no core to ask through, and an
// ask that comes back to its own verb ends at a depth, naming it.
func TestAVerbAsksOnlyWithinACallAndNotInACycle(t *testing.T) {
	var wrote []string
	if _, err := door(&wrote).Serve(context.Background(), "write", "filled:one"); err == nil || !strings.Contains(err.Error(), "request port accept is asked outside a call of the core") {
		t.Fatalf("a verb served on its own answers %v, and wants the ask refused", err)
	}
	w, err := index.ParseWiring([]byte("[instances.door]\ncomponent = \"door\"\n\n[instances.gate]\ncomponent = \"gate\"\n\n[instances.looper]\ncomponent = \"looper\"\n\n[wires]\n\"door.again\" = \"door.loop\"\n\"looper.ask\" = \"door.loop\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	ask := index.Request[string, string]("ask", "Asks loop.")
	looper := index.Declare("looper", "Asks the door's loop.", ask,
		index.Command(index.CommandDecl{Name: "loop", Usage: "loop", Summary: "Loop.", Help: "Loops.", Group: "Door", Class: index.Batch},
			func(_ struct{}, _ []string) (index.Result, error) {
				return index.Result{Requests: []index.Ask{ask.Ask("x")}}, nil
			}))
	ix, err := index.Start(w, door(&wrote), gate, looper)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Call("loop"); err == nil || !strings.Contains(err.Error(), "asks within asks 16 deep") {
		t.Fatalf("a cycle of asks answers %v, and wants it ended at its depth", err)
	}
}
