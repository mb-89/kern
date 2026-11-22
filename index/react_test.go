package index_test

import (
	"fmt"
	"strings"
	"testing"

	"kern/index"
)

// A reaction is an I/O component's alone.
func TestAReactionWithoutTheIOFlagIsRefused(t *testing.T) {
	t.Parallel()
	c := index.Declare("poller", "Runs on each second.",
		index.Reacts(func(string, struct {
			Second int `index:"second"`
		}) {
		}))
	if err := c.Err(); err == nil || !strings.Contains(err.Error(), "carries no I/O flag") {
		t.Fatalf("the declaration answers %v, and wants it refused for its missing I/O flag", err)
	}
}

// An instance's reaction runs after each commit that changes a name it reads,
// with its inputs as the commit left them; a commit that changes nothing it
// reads, or rewrites the same value, runs none, and a switched-off instance's
// never runs.
func TestAReactionRunsWhenANameItReadsChanges(t *testing.T) {
	t.Parallel()
	var commit index.Commit
	second := index.Output("second", 0, "The seconds since the start.")
	other := index.Output("other", "", "A name no poller reads.")
	beat := index.Declare("beat", "Writes the second.", index.IO(), second, other,
		index.Inlet(func(_ string, _ index.Reader, c index.Commit) error { commit = c; return nil }))
	var ran []string
	poller := index.Declare("poller", "Runs on each second.", index.IO(),
		index.Reacts(func(instance string, in struct {
			Second int `index:"second"`
		}) {
			ran = append(ran, fmt.Sprintf("%s:%d", instance, in.Second))
		}))
	w, err := index.ParseWiring([]byte(`
[instances.beat]
component = "beat"

[instances.on]
component = "poller"

[instances.off]
component = "poller"
enabled = false

[wires]
"beat.second" = "beat/second"
"beat.other" = "beat/other"
"on.second" = "beat/second"
"off.second" = "beat/second"
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Start(w, beat, poller); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 0 {
		t.Fatalf("the start ran %v, and wants no reaction before a change", ran)
	}
	for _, writes := range [][]index.Write{{second.Set(1)}, {other.Set("x")}, {second.Set(1)}, {second.Set(2), other.Set("y")}} {
		if err := commit(writes...); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(ran, " "); got != "on:1 on:2" {
		t.Fatalf("the reactions ran %q, and want %q", got, "on:1 on:2")
	}
}
