package harness

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/piwi3910/nexora/bench/dnsperf"
)

// RunDnsperf runs dnsperf for seconds against server (host:port) over names and returns its result.
func RunDnsperf(t *testing.T, server string, names []string, seconds int) dnsperf.Result {
	t.Helper()
	host, portStr, err := net.SplitHostPort(server)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(seconds)*time.Second+30*time.Second)
	defer cancel()
	r, err := dnsperf.Run(ctx, dnsperf.Options{Server: host, Port: port, Names: names, Seconds: seconds, Clients: 16, Threads: 4})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
