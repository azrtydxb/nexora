package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

// FilterResult is the part of `filter_bench --json` the filter gate reads.
type FilterResult struct {
	UniqueNames  uint64  `json:"unique_names"`
	IndexBytes   uint64  `json:"index_bytes"`
	BytesPerName float64 `json:"bytes_per_name"`
	BuildSeconds float64 `json:"build_seconds"`
	BlockedNS    float64 `json:"blocked_ns"`
	CleanNS      float64 `json:"clean_ns"`
	// ZipfNS is the per-decision time through the decision cache on the Zipf workload; zero when
	// the benchmark predates that workload or skipped it.
	ZipfNS float64 `json:"zipf_ns"`
}

// FilterVerdict holds the median per-round head/base decision-time ratios. ZipfRatio is set only
// when ZipfGated: both sides measured the Zipf workload in every round.
type FilterVerdict struct {
	BlockedRatio, CleanRatio, ZipfRatio float64
	ZipfGated                           bool
	Pass                                bool
	Reasons                             []string
}

// CompareFilter pairs base[i] with head[i], two filter_bench runs made back to back in one round
// over the same corpus, and fails when the median head/base ratio of the cold blocked, cold clean
// or (when both sides have it) Zipf decision time exceeds 1+maxRegress.
func CompareFilter(base, head []FilterResult, maxRegress float64) (FilterVerdict, error) {
	if len(base) == 0 {
		return FilterVerdict{}, errors.New("no base rounds")
	}
	if len(base) != len(head) {
		return FilterVerdict{}, fmt.Errorf("%d base rounds but %d head rounds", len(base), len(head))
	}
	n := len(base)
	blocked, clean, zipf := make([]float64, n), make([]float64, n), make([]float64, n)
	zipfGated := true
	for i := range base {
		b, h := base[i], head[i]
		if b.UniqueNames != h.UniqueNames {
			return FilterVerdict{}, fmt.Errorf("round %d compares %d and %d names", i+1, b.UniqueNames, h.UniqueNames)
		}
		if b.BlockedNS <= 0 || b.CleanNS <= 0 || h.BlockedNS <= 0 || h.CleanNS <= 0 {
			return FilterVerdict{}, fmt.Errorf("round %d has no blocked or clean decision time", i+1)
		}
		blocked[i], clean[i] = h.BlockedNS/b.BlockedNS, h.CleanNS/b.CleanNS
		if b.ZipfNS > 0 && h.ZipfNS > 0 {
			zipf[i] = h.ZipfNS / b.ZipfNS
		} else {
			zipfGated = false
		}
	}
	v := FilterVerdict{BlockedRatio: median(blocked), CleanRatio: median(clean), ZipfGated: zipfGated}
	// The epsilon keeps a regression of exactly maxRegress passing despite float rounding.
	limit := 1 + maxRegress + 1e-9
	check := func(ratio float64, what string) {
		if ratio > limit {
			v.Reasons = append(v.Reasons, fmt.Sprintf("%s decisions %.2f%% slower (median of %d rounds)", what, (ratio-1)*100, n))
		}
	}
	check(v.BlockedRatio, "blocked")
	check(v.CleanRatio, "clean")
	if zipfGated {
		v.ZipfRatio = median(zipf)
		check(v.ZipfRatio, "Zipf (decision cache)")
	}
	v.Pass = len(v.Reasons) == 0
	return v, nil
}

func cmdFilterCompare(args []string) error {
	fs := flag.NewFlagSet("filter-compare", flag.ContinueOnError)
	base := fs.String("base", "", "comma-separated base filter_bench --json files")
	head := fs.String("head", "", "comma-separated head filter_bench --json files")
	maxRegress := fs.Float64("max-regress", 0.05, "largest tolerated decision-time increase (fraction)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *maxRegress < 0 || *maxRegress >= 1 {
		return errors.New("--max-regress must be in [0, 1)")
	}
	b, err := readFilterResults(*base)
	if err != nil {
		return err
	}
	h, err := readFilterResults(*head)
	if err != nil {
		return err
	}
	v, err := CompareFilter(b, h, *maxRegress)
	if err != nil {
		return err
	}
	for i := range b {
		fmt.Printf("round %d: blocked %.1f -> %.1f ns, clean %.1f -> %.1f ns, Zipf %.1f -> %.1f ns\n",
			i+1, b[i].BlockedNS, h[i].BlockedNS, b[i].CleanNS, h[i].CleanNS, b[i].ZipfNS, h[i].ZipfNS)
	}
	bpn := func(rs []FilterResult) float64 {
		q := make([]float64, len(rs))
		for i, r := range rs {
			q[i] = r.BytesPerName
		}
		return median(q)
	}
	fmt.Printf("median head/base: blocked %.4f, clean %.4f", v.BlockedRatio, v.CleanRatio)
	if v.ZipfGated {
		fmt.Printf(", Zipf %.4f", v.ZipfRatio)
	} else {
		fmt.Print(", Zipf not gated (a side has no Zipf workload)")
	}
	fmt.Printf("; bytes per name base %.2f, head %.2f\n", bpn(b), bpn(h))
	fmt.Printf("filter decision-time gate (limit +%.2f%%): %s\n", *maxRegress*100, passFail(v.Pass))
	for _, r := range v.Reasons {
		fmt.Println("  " + r)
	}
	if !v.Pass {
		return errGateFailed
	}
	return nil
}

func readFilterResults(list string) ([]FilterResult, error) {
	if list == "" {
		return nil, errors.New("no result files given")
	}
	var out []FilterResult
	for _, p := range strings.Split(list, ",") {
		p = strings.TrimSpace(p)
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var r FilterResult
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, r)
	}
	return out, nil
}
