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
