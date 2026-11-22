// Package poller reads the system time itself, past the clock.
package poller

import (
	"fmt"
	wall "time"
)

// A comment that names time.Now is no use of it.
const doc = "time.Now"

func stamp() string {
	return fmt.Sprint(wall.Now())
}
