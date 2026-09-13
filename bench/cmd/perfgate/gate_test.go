package main

import (
	"testing"
	"time"

	"github.com/piwi3910/nexora/bench/dnsperf"
)

func qps(v ...float64) []dnsperf.Result {
	out := make([]dnsperf.Result, len(v))
	for i, q := range v {
		out[i] = dnsperf.Result{QPS: q, Completed: 1}
	}
	return out
}

func TestCompareUsesMedianPairedRatioAndFivePercentBoundary(t *testing.T) {
	// The machine slows down across rounds; every round's head is exactly 5% below its base.
	v, err := Compare(qps(200000, 100000, 50000), qps(190000, 95000, 47500), 0.05)
	if err != nil || !v.Pass || v.BaseQPS != 100000 || v.HeadQPS != 95000 {
		t.Fatalf("exactly 5%% drop must pass: %+v %v", v, err)
	}
	// The sides' medians differ by 0.1%; the paired ratios (1.11, 0.81, 0.90) show 10%.
	v, _ = Compare(qps(90000, 100000, 111000), qps(99900, 81000, 99900), 0.05)
	if v.Pass || v.BaseQPS-v.HeadQPS > 100 {
		t.Fatalf("10%% paired drop must fail: %+v", v)
	}
	v, _ = Compare(qps(100000, 100000, 100000), qps(94900, 94900, 94900), 0.05)
	if v.Pass {
		t.Fatalf("5.1%% drop must fail: %+v", v)
	}
	// One disturbed round does not decide the verdict.
	v, _ = Compare(qps(100000, 100000, 100000), qps(100000, 50000, 99000), 0.05)
	if !v.Pass || v.Drop > 0.011 {
		t.Fatalf("a single outlier round must not fail the gate: %+v", v)
	}
	if _, err := Compare(nil, qps(1), 0.05); err == nil {
		t.Fatal("empty base accepted")
	}
	if _, err := Compare(qps(1, 1), qps(1), 0.05); err == nil {
		t.Fatal("unpaired results accepted")
	}
}

func TestAbsoluteThresholds(t *testing.T) {
	ok, _ := Absolute(dnsperf.Result{QPS: 1_000_001, P99Seconds: 0.000499, Completed: 1}, 1_000_000, 500*time.Microsecond)
	if !ok.Pass {
		t.Fatalf("should pass: %+v", ok)
	}
	bad, _ := Absolute(dnsperf.Result{QPS: 999_999, P99Seconds: 0.000501, Completed: 1}, 1_000_000, 500*time.Microsecond)
	if bad.Pass || len(bad.Reasons) != 2 {
		t.Fatalf("should fail twice: %+v", bad)
	}
	if _, err := Absolute(dnsperf.Result{QPS: 2_000_000}, 1_000_000, 500*time.Microsecond); err == nil {
		t.Fatal("result with zero completed queries accepted")
	}
}
