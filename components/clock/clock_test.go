package clock_test

import (
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

func started(t *testing.T, f *fake) (tick, second chan time.Time) {
	t.Helper()
	c := clock.New(clock.Options{Clock: f})
	w, err := index.ParseWiring([]byte(`
[instances.clock]
component = "clock"

[wires]
"clock.tick" = "clock/tick"
"clock.second" = "clock/second"
`))
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.Start(w, c.Component)
	if err != nil {
		t.Fatal(err)
	}
	tick, second = make(chan time.Time, 100), make(chan time.Time, 100)
	for name, ch := range map[string]chan time.Time{"clock/tick": tick, "clock/second": second} {
		ch := ch
		if _, err := ix.Subscribe(name, func(v index.Value) { ch <- v.V.(time.Time) }); err != nil {
			t.Fatal(err)
		}
	}
	return tick, second
}

func next(t *testing.T, ch chan time.Time, what string) time.Time {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second): // bounds a commit the fired wait makes at once
		t.Fatalf("no %s was committed", what)
	}
	return time.Time{}
}

// The clock writes the tick on the 100 ms and the second on the second, from
// the host's clock alone: a test moves it and never waits on the wall clock.
// A wait that overran skips the ticks it missed.
func TestTheClockWritesTheTickAndTheSecond(t *testing.T) {
	t.Parallel()
	nine := time.Date(2026, 11, 22, 9, 0, 0, 950_000_000, time.UTC)
	f := &fake{now: nine, waits: make(chan chan time.Time)}
	tick, second := started(t, f)

	f.fire(t, nine.Add(50*time.Millisecond)) // 9:00:01.000
	if got := next(t, tick, "tick"); !got.Equal(time.Date(2026, 11, 22, 9, 0, 1, 0, time.UTC)) {
		t.Fatalf("the first tick is %v, and wants 09:00:01.000", got)
	}
	if got := next(t, second, "second"); !got.Equal(time.Date(2026, 11, 22, 9, 0, 1, 0, time.UTC)) {
		t.Fatalf("the first second is %v, and wants 09:00:01", got)
	}

	f.fire(t, nine.Add(170*time.Millisecond)) // 9:00:01.120, inside the second
	if got := next(t, tick, "tick"); !got.Equal(time.Date(2026, 11, 22, 9, 0, 1, 100_000_000, time.UTC)) {
		t.Fatalf("the second tick is %v, and wants 09:00:01.100", got)
	}
	select {
	case s := <-second:
		t.Fatalf("a tick inside the second moved it to %v", s)
	default:
	}

	f.fire(t, nine.Add(1350*time.Millisecond)) // 9:00:02.300: the ticks between are skipped
	if got := next(t, tick, "tick"); !got.Equal(time.Date(2026, 11, 22, 9, 0, 2, 300_000_000, time.UTC)) {
		t.Fatalf("the tick after an overrun is %v, and wants 09:00:02.300", got)
	}
	if got := next(t, second, "second"); !got.Equal(time.Date(2026, 11, 22, 9, 0, 2, 0, time.UTC)) {
		t.Fatalf("the second after an overrun is %v, and wants 09:00:02", got)
	}
}

// The relay answers at once: not due on the first ask, which starts the
// interval, nor while the interval is short, and due once it has passed,
// which starts the next; an interval of 0 is never due.
func TestTheRelayIsDueOncePerInterval(t *testing.T) {
	t.Parallel()
	nine := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	var r clock.Relay
	for _, c := range []struct {
		at   time.Duration
		want bool
	}{{0, false}, {time.Second, false}, {2 * time.Second, true}, {3 * time.Second, false}, {4 * time.Second, true}} {
		if got := r.Due(nine.Add(c.at), 2*time.Second); got != c.want {
			t.Fatalf("at +%v the relay answers %v, and wants %v", c.at, got, c.want)
		}
	}
	var never clock.Relay
	for _, at := range []time.Duration{0, time.Hour, 2 * time.Hour} {
		if never.Due(nine.Add(at), 0) {
			t.Fatalf("an interval of 0 is due at +%v", at)
		}
	}
}
