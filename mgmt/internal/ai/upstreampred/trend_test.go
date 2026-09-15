package upstreampred_test

import (
	"math"
	"testing"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/ai/upstreampred"
)

func TestUpstreamTrendFeatures(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	series := func(f func(i int) float64, n int) []upstreampred.HourPoint {
		var ps []upstreampred.HourPoint
		for i := 0; i < n; i++ {
			ps = append(ps, upstreampred.HourPoint{At: now.Add(time.Duration(i-n+1) * time.Hour), P99Ms: f(i)})
		}
		return ps
	}
	climb := upstreampred.Compute("u1", "fx", 250, series(func(i int) float64 { return 10 + 50*float64(i) }, 24), now)
	if math.Abs(climb.SlopeMsPerHour-50) > 0.5 || climb.CurrentP99Ms != 1160 {
		t.Fatalf("climb: %+v", climb)
	}
	rising := upstreampred.Compute("u1", "fx", 2000, series(func(i int) float64 { return 100 + 10*float64(i) }, 24), now)
	if want := now.Add(time.Duration((2000.0 - 330.0) / 10.0 * float64(time.Hour))); rising.ProjectedTimeToThreshold == nil ||
		rising.ProjectedTimeToThreshold.Sub(want).Abs() > time.Minute {
		t.Fatalf("threshold crossing: %v, want %v", rising.ProjectedTimeToThreshold, want)
	}
	step := upstreampred.Compute("u1", "fx", 2000, series(func(i int) float64 {
		if i >= 21 {
			return 200
		}
		return 10
	}, 24), now)
	if !step.StepChange {
		t.Fatal("step change not detected")
	}
	// 168 hourly points ending at 12:00 UTC: index i is hour (i+13)%24, so index%24 == 14 is 03:00 UTC.
	periodic := upstreampred.Compute("u1", "fx", 2000, series(func(i int) float64 {
		if (i % 24) == 14 {
			return 600
		}
		return 20
	}, 24*7), now)
	if len(periodic.PeriodicHours) != 1 || periodic.PeriodicHours[0] != 3 {
		t.Fatalf("periodic hours %v", periodic.PeriodicHours)
	}
	if few := upstreampred.Compute("u1", "fx", 250, series(func(int) float64 { return 10 }, 11), now); few.Points != 11 {
		t.Fatalf("points %d", few.Points)
	}
}
