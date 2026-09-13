package harness

import (
	"testing"
	"time"

	"github.com/miekg/dns"
)

// WaitDNSAnswer polls name/qtype over UDP every 100 ms until ok accepts the reply, failing the test
// with the last reply or error after timeout.
func WaitDNSAnswer(t *testing.T, server, name string, qtype uint16, timeout time.Duration, ok func(*dns.Msg) bool) *dns.Msg {
	t.Helper()
	var last *dns.Msg
	var lastErr error
	deadline := time.Now().Add(timeout)
	for {
		r, _, err := Query(t, server, name, qtype, QueryOpts{Timeout: 300 * time.Millisecond})
		if err == nil && ok(r) {
			return r
		}
		last, lastErr = r, err
		if time.Now().After(deadline) {
			t.Fatalf("%s %s @%s not satisfied within %s; last=%v err=%v", name, dns.TypeToString[qtype], server, timeout, last, lastErr)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
