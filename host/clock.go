package host

import (
	"context"
	"fmt"
	"time"
)

// Clock is the time the core reads and waits on. The core's own process
// hands in the wall clock; a test hands in a clock it moves itself. Every
// wait for an answer in kern measures its limit against one, and with the
// clock component (components/clock) it is the only place that touches a
// real timer.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// Wall is the system's clock.
type Wall struct{}

func (Wall) Now() time.Time                         { return time.Now() }
func (Wall) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Still is a clock no one moves: an I/O module's clock where none is given,
// so a module built without one reads no system time and starts no timer.
// Its time stands at At; a wait of no time is over at once, and a longer
// one never ends. The core's process hands in Wall.
type Still struct{ At time.Time }

func (s Still) Now() time.Time { return s.At }

func (s Still) After(d time.Duration) <-chan time.Time {
	if d > 0 {
		return nil // time stands: the wait never ends
	}
	c := make(chan time.Time, 1)
	c <- s.At
	return c
}

// Within is context.WithTimeout on clock: the context ends once clock passes
// d, its cause naming the wait and wrapping context.DeadlineExceeded, or when
// the parent ends or cancel is called.
func Within(ctx context.Context, clock Clock, d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(ctx)
	go func() {
		select {
		case <-clock.After(d):
			cancel(fmt.Errorf("no answer within %v: %w", d, context.DeadlineExceeded))
		case <-ctx.Done():
		}
	}()
	return ctx, func() { cancel(context.Canceled) }
}
