package main

import (
	"strings"
	"testing"
)

func TestCompareFilter(t *testing.T) {
	round := func(blocked, clean float64) FilterResult {
		return FilterResult{UniqueNames: 2_000_000, BlockedNS: blocked, CleanNS: clean}
	}
	base := []FilterResult{round(100, 90), round(102, 91), round(98, 89)}
	v, err := CompareFilter(base, []FilterResult{round(103, 92), round(104, 93), round(101, 90)}, 0.05)
	if err != nil || !v.Pass {
		t.Fatalf("3%% slower must pass: %+v %v", v, err)
	}
	v, _ = CompareFilter(base, []FilterResult{round(107, 90), round(108, 91), round(104, 89)}, 0.05)
	if v.Pass || len(v.Reasons) != 1 || !strings.Contains(v.Reasons[0], "blocked") {
		t.Fatalf("6%% slower blocked decisions must fail: %+v", v)
	}
	v, _ = CompareFilter(base, []FilterResult{round(100, 99), round(102, 99), round(98, 99)}, 0.05)
	if v.Pass || !strings.Contains(strings.Join(v.Reasons, ";"), "clean") {
		t.Fatalf("10%% slower clean decisions must fail: %+v", v)
	}
	// One noisy round does not decide: the median ratio does.
	v, _ = CompareFilter(base, []FilterResult{round(150, 91), round(101, 91), round(99, 89)}, 0.05)
	if !v.Pass {
		t.Fatalf("a single outlier round failed the gate: %+v", v)
	}
	if _, err := CompareFilter(base, base[:2], 0.05); err == nil {
		t.Fatal("unequal round counts must be an error")
	}
	if _, err := CompareFilter(base, []FilterResult{round(100, 90), {UniqueNames: 1}, round(98, 89)}, 0.05); err == nil {
		t.Fatal("rounds over different corpora must be an error")
	}
	if _, err := CompareFilter(base, []FilterResult{round(100, 90), round(0, 90), round(98, 89)}, 0.05); err == nil {
		t.Fatal("a round without decision times must be an error")
	}
}

// The Zipf (decision cache) time is gated only when both sides measured it in every round: the base
// of the PR that adds the Zipf workload has none.
func TestCompareFilterZipf(t *testing.T) {
	round := func(zipf float64) FilterResult {
		return FilterResult{UniqueNames: 2_000_000, BlockedNS: 100, CleanNS: 90, ZipfNS: zipf}
	}
	base := []FilterResult{round(20), round(21), round(19)}
	v, err := CompareFilter(base, []FilterResult{round(20.5), round(21), round(19.5)}, 0.05)
	if err != nil || !v.Pass || !v.ZipfGated {
		t.Fatalf("2%% slower cached decisions must pass and be gated: %+v %v", v, err)
	}
	v, _ = CompareFilter(base, []FilterResult{round(22), round(23), round(21)}, 0.05)
	if v.Pass || len(v.Reasons) != 1 || !strings.Contains(v.Reasons[0], "Zipf") {
		t.Fatalf("10%% slower cached decisions must fail: %+v", v)
	}
	v, err = CompareFilter([]FilterResult{round(0), round(0), round(0)}, []FilterResult{round(40), round(40), round(40)}, 0.05)
	if err != nil || !v.Pass || v.ZipfGated {
		t.Fatalf("a base without the Zipf workload must skip that gate: %+v %v", v, err)
	}
}
