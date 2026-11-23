package host_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"kern/host"
	"kern/waiting"
)

// handed is a clock whose one wait the case fires itself.
type handed struct{ fire chan time.Time }

func (h handed) Now() time.Time                       { return time.Time{} }
func (h handed) After(time.Duration) <-chan time.Time { return h.fire }

// Within ends its context once its clock passes the wait, with a cause that
// names the wait and is a deadline exceeded, and not before; cancel ends it
// as canceled. The case fires the clock and never waits on the wall clock.
func TestWithinEndsWhenItsClockPassesTheWait(t *testing.T) {
	t.Parallel()
	clk := handed{fire: make(chan time.Time, 1)}
	ctx, cancel := host.Within(context.Background(), clk, time.Hour)
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatal("the context ended before its clock passed the wait")
	default:
	}
	clk.fire <- time.Time{}
	select {
	case <-ctx.Done():
	case <-waiting.Fails(): // bounds an end that comes at once
		t.Fatal("the context did not end once its clock passed the wait")
	}
	if cause := context.Cause(ctx); !errors.Is(cause, context.DeadlineExceeded) || cause.Error() != "no answer within 1h0m0s: context deadline exceeded" {
		t.Fatalf("the context ends with %v, and wants the wait named as a deadline exceeded", cause)
	}
	ctx, cancel = host.Within(context.Background(), handed{fire: make(chan time.Time)}, time.Hour)
	cancel()
	<-ctx.Done()
	if !errors.Is(context.Cause(ctx), context.Canceled) {
		t.Fatalf("a cancelled context ends with %v, and wants canceled", context.Cause(ctx))
	}
}
