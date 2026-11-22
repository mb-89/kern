package kerntest_test

import (
	"context"
	"strings"
	"testing"

	"kern/index"
	"kern/kerntest"
)

func wiring(t *testing.T, components ...string) index.Wiring {
	t.Helper()
	var w index.Wiring
	for i, c := range components {
		on := true
		if name, off := strings.CutSuffix(c, " off"); off {
			c, on = name, false
		}
		w.Instances = append(w.Instances, index.Instance{Name: c, Component: c, Enabled: on, Line: i + 1})
	}
	return w
}

func TestTheCompletenessCheckPassesWhereEachExpectationIsClaimedAndMetOnce(t *testing.T) {
	t.Parallel()
	reader, count, ledger := modules(t)
	if err := kerntest.Check(wiring(t, "reader", "counter", "lines"), reader, count, ledger); err != nil {
		t.Fatal(err)
	}
	// Two writers of one name pass where no wiring loads both.
	other := count
	other.Component = counter("other-counter")
	if err := kerntest.Check(wiring(t, "reader", "other-counter", "lines"), reader, count, other, ledger); err != nil {
		t.Errorf("the other counter alone: %v", err)
	}
	if err := kerntest.Check(wiring(t, "reader", "counter", "other-counter off", "lines"), reader, count, other, ledger); err != nil {
		t.Errorf("the other counter switched off: %v", err)
	}
}

func TestTheCompletenessCheckRefusesAnUnclaimedExpectation(t *testing.T) {
	t.Parallel()
	reader, count, ledger := modules(t)
	err := kerntest.Check(wiring(t, "reader", "counter", "lines off"), reader, count, ledger)
	for _, want := range []string{
		`unclaimed: name "lines.append": no loaded component claims expect 3 (request post, ask "") of ` + reader.File,
		`unclaimed: name "lines.append": no loaded component claims expect 4 (request post, ask "a word") of ` + reader.File,
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("the check answered %v, want %q", err, want)
		}
	}
	if err != nil && strings.Contains(err.Error(), `name "numbers"`) {
		t.Errorf("the check refused numbers, which counter claims: %v", err)
	}
}

func TestTheCompletenessCheckRefusesAnExpectationClaimedTwice(t *testing.T) {
	t.Parallel()
	reader, count, ledger := modules(t)
	other := count
	other.Component = counter("other-counter")
	err := kerntest.Check(wiring(t, "reader", "counter", "other-counter", "lines"), reader, count, other, ledger)
	want := `claimed twice: name "numbers": counter and other-counter, all loaded, claim expect 1 (port numbers) of ` + reader.File + `: exactly one may`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("the check answered %v, want %q", err, want)
	}
}

func TestTheCompletenessCheckRefusesAnUnmetExpectation(t *testing.T) {
	t.Parallel()
	reader, count, ledger := modules(t)
	var kept []string
	// A ledger that takes the empty line, which the reader relies on being refused.
	ledger.Component = index.Declare("lines", "Keeps lines.", index.IO(),
		index.Verb("append", "Appends a line.", func(_ context.Context, line string) (int, error) {
			kept = append(kept, line)
			return len(kept), nil
		}))
	err := kerntest.Check(wiring(t, "reader", "counter", "lines"), reader, count, ledger)
	want := `unmet: name "lines.append": lines does not meet expect 3 (request post, ask "") of ` + reader.File + `: its verb append answers 1, and the expectation is a refusal`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("the check answered %v, want %q", err, want)
	}
}
