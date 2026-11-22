package hosts

import "time"

// An entry of a list no owner of it owns: time.Since is no one's.
var _ = time.Since
