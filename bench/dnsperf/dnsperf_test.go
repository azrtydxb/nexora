package dnsperf_test

import (
	"os"
	"testing"

	"github.com/piwi3910/nexora/bench/dnsperf"
)

func TestParseRealOutput(t *testing.T) {
	raw, err := os.ReadFile("testdata/output.txt")
	if err != nil {
		t.Fatal(err)
	}
	r, err := dnsperf.Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if r.QPS <= 0 || r.Completed == 0 || r.Sent < r.Completed {
		t.Fatalf("parsed %+v", r)
	}
	if r.P99Seconds <= 0 || r.P99Seconds < r.LatencyAvgSeconds/10 {
		t.Fatalf("p99 not parsed from the latency histogram: %+v", r)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := dnsperf.Parse("nothing useful"); err == nil {
		t.Fatal("garbage accepted")
	}
}
