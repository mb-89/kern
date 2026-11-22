// Package clock is the owner of the system time: its own code reads it.
package clock

import "time"

func now() time.Time { return time.Now() }
