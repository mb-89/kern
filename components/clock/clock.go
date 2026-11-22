// Package clock is kern's clock: the I/O component whose outside thing is
// time. It writes time as names: the fine-grained time since its start, at
// its granularity, 10 times a second by default, and three counters that
// count up, the seconds, the minutes and the hours since its start. A
// component that acts on time reads the counter it needs (index.Reacts) and
// keeps its own interval with a Relay over that count, so no component keeps
// a timer of its own. Its loop waits on the host's clock (host.Clock), the
// one other place in kern that touches a real timer. README.md beside this
// file is its contract.
package clock

import (
	"fmt"
	"sync"
	"time"

	"kern/host"
	"kern/index"
)

// Name is the component's name, which the wiring's instances name.
const Name = "clock"

// PerSecond is the built-in granularity: how often a second the time moves.
const PerSecond = 10

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
	timeOut   = index.Output("time", time.Duration(0), "The time since the clock's start, at its granularity: it moves per-second times a second.")
	secondOut = index.Output("second", 0, "The seconds since the clock's start, counting up: it moves once a second.")
	minuteOut = index.Output("minute", 0, "The minutes since the clock's start, counting up: it moves once a minute.")
	hourOut   = index.Output("hour", 0, "The hours since the clock's start, counting up: it moves once an hour.")
)

// Forbidden is the clock's forbid list (index.Forbids): reading and waiting on
// the system time, which only the clock's own code and the host's clock may
// do. Any other component reads a counter of the clock instead, and a wait for
// an answer waits on host.Clock (README.md "Forbid lists").
var Forbidden = index.Forbidden{Calls: []string{
	// Go
	"time.Now", "time.Since", "time.Until", "time.Sleep", "time.After", "time.Tick",
	"time.NewTicker", "time.NewTimer", "time.AfterFunc",
	// Python
	"time.time", "time.time_ns", "time.sleep", "time.monotonic", "time.monotonic_ns",
	"time.perf_counter", "datetime.now", "datetime.utcnow", "datetime.today", "date.today",
}}

// New makes the component with its outside reached through o.
func New(o Options) *Clock {
	if o.Clock == nil {
		o.Clock = host.Wall{}
	}
	c := &Clock{o: o}
	c.Component = index.Declare(Name, "Writes time as names: the time since its start, and the seconds, minutes and hours counting up; components read the one they need instead of keeping a timer.",
		index.IO(), index.Forbids(Forbidden), timeOut, secondOut, minuteOut, hourOut,
		index.Config("per-second", PerSecond, "The granularity: how often a second time moves, from 1 to 1000."),
		index.Inlet(c.start),
	)
	return c
}

// Component is the component a binary carries; importing this package
// registers it, and only a wiring's enabled instance runs it.
var Component = New(Options{})

func init() { index.Register(Component.Component) }

// start reads the granularity and runs the instance's loop, which commits
// until a commit fails.
func (c *Clock) start(_ string, read index.Reader, commit index.Commit) error {
	n, ok := read(index.ConfigPrefix + "per-second").(int)
	if !ok || n < 1 || n > 1000 {
		return fmt.Errorf("config per-second: %v is not from 1 to 1000", read(index.ConfigPrefix+"per-second"))
	}
	go c.loop(time.Second/time.Duration(n), commit)
	return nil
}

// loop waits on the host's clock for the next step and commits the time
// since its start, and each counter that moved. A wait that overran skips
// the steps it missed: the next step is the one after the time it woke at.
func (c *Clock) loop(step time.Duration, commit index.Commit) {
	clk := c.o.Clock
	start := clk.Now()
	var second, minute, hour int
	next := step
	for {
		<-clk.After(next - clk.Now().Sub(start))
		since := clk.Now().Sub(start)
		if since < next {
			continue // woke early: wait out the rest
		}
		at := since.Truncate(step)
		writes := []index.Write{timeOut.Set(at)}
		for _, n := range []struct {
			count *int
			unit  time.Duration
			out   *index.OutPort[int]
		}{{&second, time.Second, secondOut}, {&minute, time.Minute, minuteOut}, {&hour, time.Hour, hourOut}} {
			if v := int(since / n.unit); v != *n.count {
				*n.count = v
				writes = append(writes, n.out.Set(v))
			}
		}
		if err := commit(writes...); err != nil {
			return // its core is gone
		}
		next = at + step
	}
}

// Relay is the small function at the start of a component's reaction to a
// counter of the clock: Due answers at once whether every counts have passed
// since the last count it answered true at, so the reaction returns while
// they have not, and runs whole when they have. A component that acts every
// 5 seconds reads clock.second and is due on every fifth count. The first
// count it is asked starts the interval. A Relay is safe to use from many
// goroutines.
type Relay struct {
	mu      sync.Mutex
	started bool
	last    int
}

// Due says whether every counts have passed between the last real run and
// count, and where they have, marks count as the last run. An every of 0 or
// less is never due.
func (r *Relay) Due(count, every int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case every <= 0:
		return false
	case !r.started:
		r.started, r.last = true, count
		return false
	case count-r.last < every:
		return false
	}
	r.last = count
	return true
}
