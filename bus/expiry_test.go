package bus

import (
	"testing"
	"time"
)

// The fast guard of the long step (contract/longstep_test.go): a reply
// permission the bus grants an instance never expires where the core sets no
// reply wait, as kern serves, so the server's own default, NATS's two minutes
// or the shortened one a test sets, never refuses the answer of a long step;
// a set wait bounds it.
func TestAnInstancesReplyPermissionNeverExpiresWithoutAReplyWait(t *testing.T) {
	for _, replyWait := range []time.Duration{0, -time.Second} {
		for _, serverDefault := range []time.Duration{0, time.Millisecond} {
			r := instancePermissions(Instance{Name: "gap"}, replyWait, serverDefault).Response
			if r == nil || r.MaxMsgs != 1 || r.Expires >= 0 {
				t.Fatalf("with a reply wait of %v and a server default of %v the reply permission is %+v, and wants one answer that never expires (Expires < 0)", replyWait, serverDefault, r)
			}
		}
	}
	if r := instancePermissions(Instance{Name: "gap"}, time.Second, time.Millisecond).Response; r == nil || r.Expires != time.Second {
		t.Fatalf("with a reply wait of 1s the reply permission is %+v, and wants it to expire after 1s", r)
	}
}
