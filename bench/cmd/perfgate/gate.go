package main

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/piwi3910/nexora/bench/dnsperf"
)

// Verdict is the relative regression check. Drop is 1 - the median per-round head/base QPS ratio;
// BaseQPS and HeadQPS are each side's median, for the report.
type Verdict struct {
	BaseQPS, HeadQPS, Drop float64
	Ratios                 []float64
	Pass                   bool
}

// Verdict2 is the absolute check against the reference-box targets.
type Verdict2 struct {
	QPS     float64
	P99     time.Duration
	Pass    bool
	Reasons []string
}

// Compare pairs base[i] with head[i], two runs made back to back in one round, and passes when
// the median of the per-round head/base QPS ratios is at least 1-maxDrop. Pairing cancels drift
// in the machine's speed across rounds, and the median ignores rounds disturbed by bursts of
// outside load.
func Compare(base, head []dnsperf.Result, maxDrop float64) (Verdict, error) {
	if len(base) != len(head) {
		return Verdict{}, fmt.Errorf("%d base results but %d head results: each round pairs one of each", len(base), len(head))
	}
	b, err := medianQPS("base", base)
	if err != nil {
		return Verdict{}, err
	}
	h, err := medianQPS("head", head)
	if err != nil {
		return Verdict{}, err
	}
	ratios := make([]float64, len(base))
	for i := range base {
		ratios[i] = head[i].QPS / base[i].QPS
	}
	drop := 1 - median(ratios)
	// The epsilon keeps a drop of exactly maxDrop passing despite float rounding.
	return Verdict{BaseQPS: b, HeadQPS: h, Drop: drop, Ratios: ratios, Pass: drop <= maxDrop+1e-9}, nil
}

// Absolute passes when r reaches minQPS and its p99 stays below maxP99.
func Absolute(r dnsperf.Result, minQPS float64, maxP99 time.Duration) (Verdict2, error) {
	if r.Completed == 0 {
		return Verdict2{}, errors.New("result has no completed queries")
	}
	v := Verdict2{QPS: r.QPS, P99: time.Duration(r.P99Seconds * float64(time.Second))}
	if r.QPS < minQPS {
		v.Reasons = append(v.Reasons, fmt.Sprintf("QPS %.0f below %.0f", r.QPS, minQPS))
	}
	if v.P99 >= maxP99 {
		v.Reasons = append(v.Reasons, fmt.Sprintf("p99 %s not below %s", v.P99, maxP99))
	}
	v.Pass = len(v.Reasons) == 0
	return v, nil
}

func medianQPS(side string, rs []dnsperf.Result) (float64, error) {
	if len(rs) == 0 {
		return 0, fmt.Errorf("no %s results", side)
	}
	q := make([]float64, len(rs))
	for i, r := range rs {
		if r.Completed == 0 {
			return 0, fmt.Errorf("%s result %d has no completed queries", side, i+1)
		}
		q[i] = r.QPS
	}
	return median(q), nil
}

// median of a non-empty slice; it sorts a copy.
func median(v []float64) float64 {
	q := slices.Clone(v)
	slices.Sort(q)
	n := len(q)
	if n%2 == 0 {
		return (q[n/2-1] + q[n/2]) / 2
	}
	return q[n/2]
}
