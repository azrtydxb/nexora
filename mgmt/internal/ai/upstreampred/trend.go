// Package upstreampred is the upstream health prediction agent (M11 S-8): code builds hourly RTT and
// failure points per upstream from the engine samples and computes the trend features; the model only
// classifies the trend and may recommend one change, which code turns into proposal actions.
package upstreampred

import (
	"context"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// MinPoints is the fewest hourly points a prediction needs; fewer give trend insufficient_data.
const MinPoints = 12

const (
	trendWindow      = 24 // points of the slope and step-change window
	stepRecent       = 3  // points compared against the rest of the trend window
	stepFactor       = 2.0
	stepMinMs        = 20.0
	periodicFactor   = 2.0
	periodicMinDays  = 3
	periodicDays     = 7
	projectionWindow = 7 * 24 * time.Hour
	rawWindow        = 24 * time.Hour     // engine_stats keeps the raw samples
	rollupWindow     = 8 * 24 * time.Hour // engine_stats_rollup keeps 8 days
)

// HourPoint is one hour of an upstream: RTT percentiles over the engines' samples and the failure ratio
// of the queries sent to it.
type HourPoint struct {
	At                         time.Time
	P50Ms, P99Ms, FailureRatio float64
}

// Features are the code-computed trend features of one upstream.
type Features struct {
	UpstreamID, Name           string
	Points                     int
	CurrentP50Ms, CurrentP99Ms float64
	SlopeMsPerHour             float64
	StepChange                 bool
	PeriodicHours              []int
	FailureSlopePerHour        float64
	ProjectedTimeToThreshold   *time.Time
	TimeoutMs                  int
}

// Compute derives the features from hourly points (any order). Points < MinPoints means the caller
// reports insufficient_data.
func Compute(upstreamID, name string, timeoutMs int, points []HourPoint, now time.Time) Features {
	f := Features{UpstreamID: upstreamID, Name: name, Points: len(points), TimeoutMs: timeoutMs, PeriodicHours: []int{}}
	if len(points) == 0 {
		return f
	}
	ps := slices.Clone(points)
	slices.SortFunc(ps, func(a, b HourPoint) int { return a.At.Compare(b.At) })
	last := ps[len(ps)-1]
	f.CurrentP50Ms, f.CurrentP99Ms = last.P50Ms, last.P99Ms

	window := ps[max(0, len(ps)-trendWindow):]
	f.SlopeMsPerHour = slope(window, func(p HourPoint) float64 { return p.P99Ms })
	f.FailureSlopePerHour = slope(window, func(p HourPoint) float64 { return p.FailureRatio })
	if len(window) > stepRecent {
		recent := mean(window[len(window)-stepRecent:])
		prior := mean(window[:len(window)-stepRecent])
		f.StepChange = recent >= stepFactor*prior && recent-prior >= stepMinMs
	}
	if f.SlopeMsPerHour > 0 {
		hours := max(0, (float64(timeoutMs)-f.CurrentP99Ms)/f.SlopeMsPerHour)
		if at := now.Add(time.Duration(hours * float64(time.Hour))); at.Sub(now) <= projectionWindow {
			f.ProjectedTimeToThreshold = &at
		}
	}
	f.PeriodicHours = periodicHours(ps, now)
	return f
}

// slope is the least-squares slope of value against hours.
func slope(ps []HourPoint, value func(HourPoint) float64) float64 {
	if len(ps) < 2 {
		return 0
	}
	n := float64(len(ps))
	var sx, sy, sxx, sxy float64
	for _, p := range ps {
		x := p.At.Sub(ps[0].At).Hours()
		y := value(p)
		sx, sy, sxx, sxy = sx+x, sy+y, sxx+x*x, sxy+x*y
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0
	}
	return (n*sxy - sx*sy) / den
}

func mean(ps []HourPoint) float64 {
	var sum float64
	for _, p := range ps {
		sum += p.P99Ms
	}
	return sum / float64(len(ps))
}

// periodicHours returns the UTC hours of day whose P99 is at least twice that day's median P99 on at
// least 3 of the last 7 days.
func periodicHours(ps []HourPoint, now time.Time) []int {
	days := map[time.Time][]HourPoint{}
	for _, p := range ps {
		if p.At.After(now.Add(-periodicDays * 24 * time.Hour)) {
			day := p.At.UTC().Truncate(24 * time.Hour)
			days[day] = append(days[day], p)
		}
	}
	var counts [24]int
	for _, day := range days {
		values := make([]float64, len(day))
		for i, p := range day {
			values[i] = p.P99Ms
		}
		median := percentile(values, 0.5)
		hit := map[int]bool{}
		for _, p := range day {
			if p.P99Ms > 0 && p.P99Ms >= periodicFactor*median {
				hit[p.At.UTC().Hour()] = true
			}
		}
		for h := range hit {
			counts[h]++
		}
	}
	out := []int{}
	for h, c := range counts {
		if c >= periodicMinDays {
			out = append(out, h)
		}
	}
	return out
}

// percentile is the nearest-rank percentile of values (sorted in place); 0 for none.
func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	slices.Sort(values)
	i := int(math.Ceil(p*float64(len(values)))) - 1
	return values[min(max(i, 0), len(values)-1)]
}

type hourAcc struct {
	rtts              []float64
	failures, queries uint64
}

// HourlyPoints builds the hourly points of the upstream named upstreamName: the raw engine_stats samples
// of the last 24 h and the engine_stats_rollup samples of the 7 days before. RTT percentiles are over
// every engine's reported RTT in the hour; the failure ratio is the failures over the queries between
// consecutive samples of each engine. Hours without a sample naming the upstream have no point.
func HourlyPoints(ctx context.Context, q store.PolicyQuerier, upstreamName string, now time.Time) ([]HourPoint, error) {
	rawFrom := now.Add(-rawWindow)
	rows, err := q.Query(ctx, `select engine_id, at, stats from engine_stats where at > $1 and at <= $2
		union all
		select engine_id, bucket, stats from engine_stats_rollup where bucket > $3 and bucket <= $1
		order by 1, 2`, rawFrom, now, now.Add(-rollupWindow))
	if err != nil {
		return nil, store.MapError(err)
	}
	defer rows.Close()
	hours := map[time.Time]*hourAcc{}
	var engine uuid.UUID
	var prev *controlv1.UpstreamStatus
	for rows.Next() {
		var id uuid.UUID
		var at time.Time
		var raw []byte
		if err := rows.Scan(&id, &at, &raw); err != nil {
			return nil, store.MapError(err)
		}
		if id != engine {
			engine, prev = id, nil
		}
		s := &controlv1.Stats{}
		if proto.Unmarshal(raw, s) != nil {
			continue
		}
		var u *controlv1.UpstreamStatus
		for _, candidate := range s.Upstreams {
			if candidate.Name == upstreamName {
				u = candidate
				break
			}
		}
		if u == nil {
			prev = nil
			continue
		}
		key := at.UTC().Truncate(time.Hour)
		acc := hours[key]
		if acc == nil {
			acc = &hourAcc{}
			hours[key] = acc
		}
		if u.RttUs > 0 {
			acc.rtts = append(acc.rtts, float64(u.RttUs)/1000)
		}
		// A counter that went backwards is an engine restart: that interval has no delta.
		if prev != nil && u.QueriesTotal >= prev.QueriesTotal && u.FailuresTotal >= prev.FailuresTotal {
			acc.queries += u.QueriesTotal - prev.QueriesTotal
			acc.failures += u.FailuresTotal - prev.FailuresTotal
		}
		prev = u
	}
	if err := rows.Err(); err != nil {
		return nil, store.MapError(err)
	}
	out := make([]HourPoint, 0, len(hours))
	for at, acc := range hours {
		p := HourPoint{At: at, P99Ms: percentile(acc.rtts, 0.99), P50Ms: percentile(acc.rtts, 0.5)}
		if acc.queries > 0 {
			p.FailureRatio = min(1, float64(acc.failures)/float64(acc.queries))
		}
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b HourPoint) int { return a.At.Compare(b.At) })
	return out, nil
}
