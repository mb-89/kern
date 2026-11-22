package clock_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"kern/components/clock"
	"kern/index"
)

// fake is a host clock the test moves: each After is handed to the test,
// which moves the time and fires it.
type fake struct {
	mu    sync.Mutex
	now   time.Time
	waits chan chan time.Time
}

func (f *fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fake) After(time.Duration) <-chan time.Time {
	c := make(chan time.Time, 1)
	f.waits <- c
	return c
}

// fire moves the clock to at and wakes the loop's wait.
func (f *fake) fire(t *testing.T, at time.Time) {
	t.Helper()
	var c chan time.Time
	select {
	case c = <-f.waits:
	case <-time.After(5 * time.Second): // bounds a wait the loop asks at once
		t.Fatal("the clock's loop asked no wait")
	}
	f.mu.Lock()
	f.now = at
	f.mu.Unlock()
	c <- at
}

func started(t *testing.T, f *fake, config string) map[string]chan any {
	t.Helper()
	c := clock.New(clock.Options{Clock: f})
	w, err := index.ParseWiring([]byte(`
[instances.clock]
component = "clock"
config = { ` + config + ` }

[wires]
"clock.time" = "clock/time"
"clock.second" = "clock/second"
"clock.minute" = "clock/minute"
"clock.hour" = "clock/hour"
`))
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.Start(w, c.Component)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]chan any{}
	for _, name := range []string{"time", "second", "minute", "hour"} {
		ch := make(chan any, 100)
		out[name] = ch
		if _, err := ix.Subscribe("clock/"+name, func(v index.Value) { ch <- v.V }); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func next(t *testing.T, ch chan any, what string) any {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second): // bounds a commit the fired wait makes at once
		t.Fatalf("no %s was committed", what)
	}
	return nil
}

func still(t *testing.T, out map[string]chan any, names ...string) {
	t.Helper()
	for _, name := range names {
		select {
		case v := <-out[name]:
			t.Fatalf("%s moved to %v, and wants it still", name, v)
		default:
		}
	}
}

// The clock writes the time since its start on its granularity's step, and
// the seconds, minutes and hours since its start as counters that count up,
// each only when it moves, from the host's clock alone: a test moves it and
// never waits on the wall clock. A wait that overran skips the steps it
// missed.
func TestTheClockWritesTheTimeAndCountsUp(t *testing.T) {
	t.Parallel()
	nine := time.Date(2026, 11, 22, 9, 0, 0, 30_000_000, time.UTC)
	f := &fake{now: nine, waits: make(chan chan time.Time)}
	out := started(t, f, ``)

	f.fire(t, nine.Add(120*time.Millisecond))
	if got := next(t, out["time"], "time"); got != 100*time.Millisecond {
		t.Fatalf("the first time is %v, and wants 100ms", got)
	}
	still(t, out, "second", "minute", "hour")

	f.fire(t, nine.Add(time.Second+50*time.Millisecond)) // the steps between are skipped
	if got := next(t, out["time"], "time"); got != time.Second {
		t.Fatalf("the time after an overrun is %v, and wants 1s", got)
	}
	if got := next(t, out["second"], "second"); got != 1 {
		t.Fatalf("the second counts %v, and wants 1", got)
	}
	still(t, out, "minute", "hour")

	f.fire(t, nine.Add(time.Hour+time.Minute+2*time.Second))
	if got := next(t, out["time"], "time"); got != time.Hour+time.Minute+2*time.Second {
		t.Fatalf("the time is %v, and wants 1h1m2s", got)
	}
	for name, want := range map[string]int{"second": 3662, "minute": 61, "hour": 1} {
		if got := next(t, out[name], name); got != want {
			t.Fatalf("the %s counts %v, and wants %d", name, got, want)
		}
	}
}

// The granularity is configurable: at 2 a second the time moves on the
// 500 ms; one out of range refuses the start.
func TestTheClocksGranularityIsConfigurable(t *testing.T) {
	t.Parallel()
	nine := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	f := &fake{now: nine, waits: make(chan chan time.Time)}
	out := started(t, f, `per-second = 2`)
	f.fire(t, nine.Add(700*time.Millisecond))
	if got := next(t, out["time"], "time"); got != 500*time.Millisecond {
		t.Fatalf("at 2 a second the first time is %v, and wants 500ms", got)
	}
	for _, n := range []int{0, 1001} {
		w, err := index.ParseWiring([]byte(fmt.Sprintf("[instances.clock]\ncomponent = \"clock\"\nconfig = { per-second = %d }\n", n)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := index.Start(w, clock.New(clock.Options{Clock: f}).Component); err == nil || !strings.Contains(err.Error(), "config per-second") {
			t.Fatalf("per-second %d starts with %v, and wants a refusal naming it", n, err)
		}
	}
}

// The relay answers at once: not due on the first count it is asked, which
// starts the interval, nor while fewer than every counts have passed, and
// due once they have, which starts the next: every 5 seconds is every fifth
// count. An interval of 0 is never due.
func TestTheRelayIsDueOncePerInterval(t *testing.T) {
	t.Parallel()
	var r clock.Relay
	var due []int
	for count := 3; count <= 20; count++ {
		if r.Due(count, 5) {
			due = append(due, count)
		}
	}
	if fmt.Sprint(due) != "[8 13 18]" {
		t.Fatalf("over the counts 3 to 20 the relay is due at %v, and wants every fifth after the first: [8 13 18]", due)
	}
	var skipped clock.Relay
	skipped.Due(0, 5)
	if !skipped.Due(9, 5) || skipped.Due(13, 5) || !skipped.Due(14, 5) {
		t.Fatal("a count that jumps past the interval is due once, and the next interval counts from it")
	}
	var never clock.Relay
	for _, count := range []int{0, 100, 200} {
		if never.Due(count, 0) {
			t.Fatalf("an interval of 0 is due at %d", count)
		}
	}
}
