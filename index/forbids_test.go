package index_test

import (
	"reflect"
	"strings"
	"testing"

	"kern/index"
)

// An I/O component declares its forbid list, and knows the package that
// declared it; a component without the I/O flag that declares one is
// refused, as is a list that names nothing or an entry of no kind's form.
func TestAForbidListIsAnIOComponentsAlone(t *testing.T) {
	t.Parallel()
	list := index.Forbidden{Calls: []string{"time.Now", "os/exec.Command"}, Commands: []string{"gh"}, Hosts: []string{"api.github.com"}}
	door := index.Declare("door", "Reaches the outside.", index.IO(), index.Forbids(list))
	if err := door.Err(); err != nil {
		t.Fatal(err)
	}
	if got := door.Forbids(); !reflect.DeepEqual(got, list) {
		t.Fatalf("the forbid list reads %+v, and wants %+v", got, list)
	}
	if got := door.Package(); got != "kern/index_test" {
		t.Fatalf("the component is declared by %q, and wants kern/index_test", got)
	}
	if !index.Declare("plain", "Computes.").Forbids().Empty() {
		t.Fatal("a component without a forbid list reads one")
	}
	for _, c := range []struct {
		name  string
		parts []index.Part
		want  string
	}{
		{"compute", []index.Part{index.Forbids(list)}, "component compute forbids what it replaces but carries no I/O flag"},
		{"empty", []index.Part{index.IO(), index.Forbids(index.Forbidden{})}, "forbids names nothing"},
		{"twice", []index.Part{index.IO(), index.Forbids(list), index.Forbids(list)}, "forbids is declared twice"},
		{"bare", []index.Part{index.IO(), index.Forbids(index.Forbidden{Calls: []string{"Now"}})}, `forbids.calls: "Now" is no call`},
		{"url", []index.Part{index.IO(), index.Forbids(index.Forbidden{Hosts: []string{"https://api.github.com"}})}, `forbids.hosts: "https://api.github.com" is no host`},
		{"double", []index.Part{index.IO(), index.Forbids(index.Forbidden{Commands: []string{"gh", "gh"}})}, `forbids.commands: "gh" is named twice`},
	} {
		err := index.Declare(c.name, "A component.", c.parts...).Err()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: the declaration answers %v, and wants %q", c.name, err, c.want)
		}
	}
}
