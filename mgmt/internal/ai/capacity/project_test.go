package capacity_test

import (
	"testing"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/ai/capacity"
)

// TestCapacityProjection catches a wrong least-squares slope or exhaustion date, a date without a limit
// or with negative growth, a projection from fewer than 3 points, and a missing confidence cap below 7
// points.
func TestCapacityProjection(t *testing.T) {
	now := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	pts := func(start, perDay float64, n int) []capacity.Point {
		var out []capacity.Point
		for i := 0; i < n; i++ {
			out = append(out, capacity.Point{Day: now.AddDate(0, 0, i-n+1), Value: start + perDay*float64(i)})
		}
		return out
	}
	max := 22.7e6
	p := capacity.Project("filter_index", pts(18.5e6-1200*9, 1200, 10), &max, now)
	want := now.Add(time.Duration((22.7e6 - 18.5e6) / 1200 * 24 * float64(time.Hour)))
	if p.ExhaustionDate == nil || p.ExhaustionDate.Sub(want).Abs() > 24*time.Hour || p.Trend != "stable" {
		t.Fatalf("projection %+v, want exhaustion near %v", p, want)
	}
	if p.DaysRemaining == nil || *p.DaysRemaining < 3499 || *p.DaysRemaining > 3501 {
		t.Fatalf("days remaining %v", p.DaysRemaining)
	}
	if s := capacity.Project("cache", pts(100, -5, 10), &max, now); s.ExhaustionDate != nil || s.Trend != "shrinking" {
		t.Fatalf("shrinking: %+v", s)
	}
	if f := capacity.Project("cache", pts(100, 5, 2), &max, now); f.Trend != "insufficient_data" {
		t.Fatalf("two points: %+v", f)
	}
	if c := capacity.Project("query_volume", pts(100, 50, 5), nil, now); c.ExhaustionDate != nil || c.MaxConfidence != 0.5 {
		t.Fatalf("no limit or confidence cap: %+v", c)
	}
}
