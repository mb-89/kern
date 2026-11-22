package fixture_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kern/index"
	"kern/index/fixture"
	"kern/kerntest"
)

// The worked example of README.md "Expectations": each reader's test is
// answered from its own expectation file, each writer's test meets what the
// pool holds under the names it claims, and a wiring that loads them is
// complete.

// pythonTally is the sample Python component's expectation file, which the
// pool holds beside the Go fixtures' own.
var pythonTally = filepath.Join("..", "..", "contract", "fixture", "tally", "expect.toml")

func TestTallysTestIsAnsweredFromItsExpectations(t *testing.T) {
	t.Parallel()
	f, err := kerntest.Expecting(fixture.Tally, "tally.expect.toml")
	must(t, err)
	must(t, f.Derive("total"))
	if got, _ := f.Out("total"); got != (index.Value{V: 0, Provided: true}) {
		t.Fatalf("with no numbers, total is %+v", got)
	}
	must(t, f.Given("numbers stand"))
	must(t, f.Config("scale", 10))
	must(t, f.Derive("total"))
	if got, _ := f.Out("total"); got != (index.Value{V: 60, Provided: true}) {
		t.Fatalf("given numbers stand, total is %+v", got)
	}
	if text, err := f.Call("tally note", "kept"); err != nil || text != `noted "kept" beside 3 numbers` {
		t.Fatalf("tally note answered %q, %v", text, err)
	}
}

func TestClerksTestIsAnsweredFromItsExpectations(t *testing.T) {
	t.Parallel()
	f, err := kerntest.Expecting(fixture.Clerk, "clerk.expect.toml")
	must(t, err)
	if text, err := f.Call("clerk file", "a", "b"); err != nil || text != "filing 2 words\nfiled b as line 1" {
		t.Fatalf("clerk file answered %q, %v", text, err)
	}
	must(t, f.Given("two lines stand"))
	if text, err := f.Call("clerk file", "c"); err != nil || text != "filing 1 words\nfiled c as line 3" {
		t.Fatalf("given two lines stand, clerk file answered %q, %v", text, err)
	}
	if got, _ := f.Out("filed"); got != (index.Value{V: "c as line 3", Provided: true}) {
		t.Fatalf("filed is %+v", got)
	}
	if _, err := f.Call("clerk file", "x", "", "y"); err == nil || !strings.Contains(err.Error(), "request 2 of 3, on post, failed: refused, as expect 2") {
		t.Fatalf("an empty line was posted: %v", err)
	}
}

// counter and ledger are the writers of the example, each with its own setup
// of the givens it meets.
func counter() kerntest.Module {
	return kerntest.Module{Component: fixture.Counter, File: "counter.expect.toml",
		Given: map[string]kerntest.Setup{"numbers stand": func(f *kerntest.Fake) error { return f.Config("upto", 3) }}}
}

func ledger() kerntest.Module {
	var lines []string
	return kerntest.Module{Component: fixture.Ledger(&lines), File: "ledger.expect.toml",
		Given: map[string]kerntest.Setup{"two lines stand": func(*kerntest.Fake) error {
			lines = []string{"one", "two"}
			return nil
		}}}
}

// meet is a writer's test: it fails on each expectation m does not meet, and
// reports each pending one without failing.
func meet(t *testing.T, m kerntest.Module, pool kerntest.Pool) {
	t.Helper()
	pending, err := m.MeetPending(pool)
	for _, p := range pending {
		t.Log(p)
	}
	must(t, err)
}

func TestCountersTestMeetsThePooledExpectationsOfNumbers(t *testing.T) {
	t.Parallel()
	pool, err := kerntest.ReadPool("tally.expect.toml", "clerk.expect.toml", pythonTally)
	must(t, err)
	if len(pool["numbers"]) != 4 {
		t.Fatalf("the pool holds %d expectations of numbers, want tally's two and the Python tally's two", len(pool["numbers"]))
	}
	meet(t, counter(), pool)
}

func TestLedgersTestMeetsThePooledExpectationsOfLinesAppend(t *testing.T) {
	t.Parallel()
	pool, err := kerntest.ReadPool("tally.expect.toml", "clerk.expect.toml", pythonTally)
	must(t, err)
	if len(pool["lines.append"]) != 5 {
		t.Fatalf("the pool holds %d expectations of lines.append, want the clerk's three and the Python tally's two", len(pool["lines.append"]))
	}
	meet(t, ledger(), pool)
}

func TestTheFixturesWiringsAreComplete(t *testing.T) {
	t.Parallel()
	w, err := index.ParseWiring([]byte(`
[instances.tally]
component = "tally"

[instances.counter]
component = "counter"

[instances.clerk]
component = "clerk"

[instances.ledger]
component = "ledger"

[wires]
"tally.numbers" = "counter.numbers"
"clerk.post" = "ledger.append"
`))
	must(t, err)
	tally := kerntest.Module{Component: fixture.Tally, File: "tally.expect.toml"}
	clerk := kerntest.Module{Component: fixture.Clerk, File: "clerk.expect.toml"}
	must(t, kerntest.Check(w, tally, counter(), clerk, ledger()))
	// Without its counter, tally's expectations of numbers stand unclaimed.
	w.Instances[1].Enabled = false
	if err := kerntest.Check(w, tally, counter(), clerk, ledger()); err == nil || !strings.Contains(err.Error(), `unclaimed: name "numbers": no loaded component claims expect 1 (port numbers) of tally.expect.toml`) {
		t.Fatalf("a wiring without counter was complete: %v", err)
	}
}

func TestTheSamplePythonComponentsWiringIsComplete(t *testing.T) {
	t.Parallel()
	py, err := index.ReadDeclaration(filepath.Join("..", "..", "contract", "fixture", "tally", "component.toml"))
	must(t, err)
	w, err := index.ParseWiring([]byte(`
[instances.tally]
component = "tally"
command = ["python", "-m", "kern", "run", "contract/fixture/tally"]

[instances.counter]
component = "counter"

[instances.ledger]
component = "ledger"
`))
	must(t, err)
	must(t, kerntest.Check(w, kerntest.Module{Component: py, File: pythonTally}, counter(), ledger()))
}

// The watcher's test is answered from the GitHub hot test's recordings
// beside its file (watcher.expect.recorded.json, README.md "Hot tests"):
// the real title and state of the issue read, not the ones its file states.
func TestWatchersTestIsAnsweredFromTheHotTestsRecordings(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("watcher.expect.recorded.json")
	must(t, err)
	var recs struct {
		Recordings []struct {
			Expect int `json:"expect"`
			Answer struct {
				Data fixture.IssueData `json:"data"`
			} `json:"answer"`
		} `json:"recordings"`
	}
	must(t, json.Unmarshal(data, &recs))
	var real fixture.IssueData
	for _, r := range recs.Recordings {
		if r.Expect == 1 {
			real = r.Answer.Data
		}
	}
	if real.Title == "" || real.Title == "a title" {
		t.Fatalf("no real issue is recorded: %+v", recs)
	}
	f, err := kerntest.Expecting(fixture.Watcher, "watcher.expect.toml")
	must(t, err)
	for _, line := range f.Stale() {
		t.Log(line)
	}
	want := "owner/repo issue 1 " + real.State + ": " + real.Title
	if text, err := f.Call("watch", "owner", "repo", "1"); err != nil || text != want {
		t.Fatalf("watch answered %q, %v, want %q", text, err, want)
	}
	must(t, f.Given("issue 1 is read"))
	must(t, f.Derive("titles"))
	if got, _ := f.Out("titles"); fmt.Sprint(got.V) != "[1 "+real.State+": "+real.Title+"]" {
		t.Fatalf("given issue 1 is read, titles is %+v", got)
	}
}
