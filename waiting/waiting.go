// Package waiting is how a kern test waits without the wall clock (README.md
// "Tests"): on an event the awaited code gives, with one deadline that fires
// only where the event never comes. It is the one home of a test's wall-clock
// deadline: kerntest/waits_test.go refuses time.Sleep, time.After,
// time.NewTimer and a deadline from time.Now in every _test.go of kern.
package waiting

import (
	"testing"
	"time"
)

// Bound is the deadline of every wait here: a failure bound, past any run
// that passes, so a loaded machine slows a passing test and never fails it.
const Bound = 60 * time.Second

// look is the pause between two looks of Until: it decides nothing, the
// condition does.
const look = time.Millisecond

// Fails is the failure case of a select that waits on an event: it fires
// once Bound passed, and never where the test passes.
func Fails() <-chan time.Time { return time.After(Bound) }

// Until looks at done until it holds, pausing between looks, and fails t
// past Bound, saying what never came. It is for state that gives no event of
// its own, such as a process's end or a file another process writes.
func Until(t testing.TB, what string, done func() bool) {
	t.Helper()
	end := time.Now().Add(Bound)
	for !done() {
		if time.Now().After(end) {
			t.Fatalf("%s did not come within %v", what, Bound)
		}
		time.Sleep(look)
	}
}
