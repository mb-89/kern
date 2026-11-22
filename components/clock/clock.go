// Package clock is kern's clock: the I/O component whose outside thing is
// time. It writes time as two names, a fast tick 10 times a second and a slow
// one once a second, and a component that acts on time reads the one it needs
// (index.Reacts) and keeps its own interval with a Relay, so no component
// keeps a timer of its own. Its loop waits on the host's clock (host.Clock),
// the one other place in kern that touches a real timer. README.md beside
// this file is its contract.
package clock

import (
	"sync"
	"time"

	"kern/host"
	"kern/index"
)

// Name is the component's name, which the wiring's instances name.
const Name = "clock"

// The clock's two granularities: a component never runs more often than the tick.
const (
	Tick   = 100 * time.Millisecond
	Second = time.Second
)

// Options are what the component reaches the outside with; each has a
// default, and a test replaces it.
type Options struct {
	// Clock is the time the loop reads and waits on: the wall clock where none is given.
	Clock host.Clock
}

// Clock is the component.
type Clock struct {
	// Component is what the index, the wiring and the catalog see.
	Component *index.Component
	o         Options
}

// The component's outputs.
var (
	tickOut   = index.Output("tick", time.Time{}, "The time of the last tick, UTC, on the 100 ms: it moves 10 times a second.")
	secondOut = index.Output("second", time.Time{}, "The time of the last second, UTC, on the second: it moves once a second.")
)

// New makes the component with its outside reached through o.
func New(o Options) *Clock {
	if o.Clock == nil {
		o.Clock = host.Wall{}
	}
	c := &Clock{o: o}
	c.Component = index.Declare(Name, "Writes time as names: tick 10 times a second, second once a second; components read the one they need instead of keeping a timer.",
		index.IO(), tickOut, secondOut,
		index.Inlet(c.start),
	)
	return c
}

// Component is the component a binary carries; importing this package
// registers it, and only a wiring's enabled instance runs it.
var Component = New(Options{})

func init() { index.Register(Component.Component) }

// start runs the instance's loop, which commits until a commit fails.
func (c *Clock) start(_ string, _ index.Reader, commit index.Commit) error {
	go c.loop(commit)
	return nil
}

// loop waits on the host's clock for the next 100 ms and commits the tick,
// and the second where it moved. A wait that overran skips the ticks it
// missed: the next tick is the one after the time it woke at.
func (c *Clock) loop(commit index.Commit) {
	clk := c.o.Clock
	var second time.Time
	next := clk.Now().UTC().Truncate(Tick).Add(Tick)
	for {
		<-clk.After(next.Sub(clk.Now()))
		now := clk.Now().UTC()
		if now.Before(next) {
			continue // woke early: wait out the rest
		}
		tick := now.Truncate(Tick)
		writes := []index.Write{tickOut.Set(tick)}
		if s := now.Truncate(Second); !s.Equal(second) {
			second = s
			writes = append(writes, secondOut.Set(s))
		}
		if err := commit(writes...); err != nil {
			return // its core is gone
		}
		next = tick.Add(Tick)
	}
}

// Relay is the small function at the start of a component's reaction to the
// clock: Due answers at once whether every has passed since the last time it
// answered true, so the reaction returns while it has not, and runs whole
// when it has. The first time it is asked starts the interval. A Relay is
// safe to use from many goroutines.
type Relay struct {
	mu   sync.Mutex
	last time.Time
}

// Due says whether every has passed between the last real run and now, and
// where it has, marks now as the last run. An every of 0 or less is never due.
func (r *Relay) Due(now time.Time, every time.Duration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case every <= 0:
		return false
	case r.last.IsZero():
		r.last = now
		return false
	case now.Sub(r.last) < every:
		return false
	}
	r.last = now
	return true
}
