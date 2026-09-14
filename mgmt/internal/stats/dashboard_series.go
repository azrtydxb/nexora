package stats

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/fleet"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Range is a dashboard time range: "15m", "1h", "6h", "24h" or "7d".
type Range string

type rangeSpec struct {
	span, step time.Duration
	rollup     bool // read engine_stats_rollup instead of the raw samples
}

var ranges = map[Range]rangeSpec{
	"15m": {15 * time.Minute, 10 * time.Second, false},
	"1h":  {time.Hour, 30 * time.Second, false},
	"6h":  {6 * time.Hour, 3 * time.Minute, false},
	"24h": {24 * time.Hour, 10 * time.Minute, false},
	"7d":  {7 * 24 * time.Hour, time.Hour, true},
}

// ErrInvalidRange is returned for a range outside 15m, 1h, 6h, 24h and 7d.
var ErrInvalidRange = errors.New("range must be 15m, 1h, 6h, 24h or 7d")

// Span returns the duration a range covers; false for an unknown range.
func (r Range) Span() (time.Duration, bool) {
	s, ok := ranges[r]
	return s.span, ok
}

// SeriesPoint is the fleet's figures over one step: rates per second summed across engines,
// percentiles over the summed histogram deltas, ratios of summed counts, and LameMarked as the
// number of servers marked lame during the step.
type SeriesPoint struct {
	At                                                                        time.Time
	QPS, BlockedQPS, RewrittenQPS                                             float64
	QPSByTransport, QPSByRcode, BlockedByCategory, AnswersByRoute             map[string]float64
	P50Ms, P95Ms, P99Ms, MissP50Ms, MissP95Ms, MissP99Ms                      float64
	CacheHitRatio, CacheMissRatio, CacheStaleRatio                            float64
	RecursionUpstreamQPS, RecursionTimeoutsQPS, LameMarked                    float64
	ResolutionFailuresQPS, DNSSECSecureQPS, DNSSECInsecureQPS, DNSSECBogusQPS float64
}

// EngineFigures are one engine's cache and filter index sizes from its newest sample in the range.
type EngineFigures struct {
	EngineID                                   uuid.UUID
	NodeName                                   string
	CacheEntries, CacheBytes, FilterIndexBytes uint64
}

// SeriesData is the dashboard's time series over a range.
type SeriesData struct {
	Range       Range
	StepSeconds int
	Points      []SeriesPoint
	Engines     []EngineFigures
}

// counts are the counter deltas of one engine over one step (or their sum across engines).
type counts struct {
	seconds                                                              float64
	queries, blocked, rewritten, hits, misses, stale                     uint64
	recUpstream, recTimeouts, lame, resFailures, secure, insecure, bogus uint64
	byTransport, byRcode, byCategory, byRoute                            map[string]uint64
	bounds, duration, missDuration                                       []uint64
}

// DashboardSeries derives the fleet series over the range ending at now. Ranges up to 24 h read the
// raw engine_stats samples, 7d reads engine_stats_rollup. Per engine, the counter deltas between
// consecutive samples are added to the step of the later sample; a pair whose query counter went
// down or whose start time changed (a restart) is skipped.
func DashboardSeries(ctx context.Context, q store.PolicyQuerier, r Range, now time.Time) (SeriesData, error) {
	spec, ok := ranges[r]
	if !ok {
		return SeriesData{}, ErrInvalidRange
	}
	out := SeriesData{Range: r, StepSeconds: int(spec.step.Seconds()), Points: []SeriesPoint{}, Engines: []EngineFigures{}}
	table, column := "engine_stats", "at"
	if spec.rollup {
		table, column = "engine_stats_rollup", "bucket"
	}
	// debt: the 24h range decodes every 10 s sample of every engine (8,640 per engine); revisit with a
	// 1-minute rollup when a fleet above 50 engines makes the dashboard request exceed 1 s.
	rows, err := q.Query(ctx, `select s.engine_id, e.node_name, s.`+column+`, s.stats from `+table+` s
		join engines e on e.id = s.engine_id
		where e.deleted_at is null and s.`+column+` > $1 order by s.engine_id, s.`+column, now.Add(-spec.span))
	if err != nil {
		return out, store.MapError(err)
	}
	defer rows.Close()

	fleetSteps := map[time.Time]*SeriesPoint{}
	fleetCounts := map[time.Time]*counts{}
	var engine uuid.UUID
	var nodeName string
	var prev, last *controlv1.Stats
	var prevAt time.Time
	engineSteps := map[time.Time]*counts{}
	flush := func() {
		if last != nil {
			f := EngineFigures{EngineID: engine, NodeName: nodeName, CacheEntries: last.CacheEntries, CacheBytes: last.CacheBytes}
			if last.FilterIndex != nil {
				f.FilterIndexBytes = last.FilterIndex.Bytes
			}
			out.Engines = append(out.Engines, f)
		}
		for at, c := range engineSteps {
			if fleetSteps[at] == nil {
				fleetSteps[at], fleetCounts[at] = newPoint(at), &counts{}
			}
			addRates(fleetSteps[at], c)
			fleetCounts[at].add(c)
		}
		engineSteps = map[time.Time]*counts{}
		prev, last = nil, nil
	}
	for rows.Next() {
		var id uuid.UUID
		var name string
		var at time.Time
		var raw []byte
		if err := rows.Scan(&id, &name, &at, &raw); err != nil {
			return out, store.MapError(err)
		}
		s := &controlv1.Stats{}
		if proto.Unmarshal(raw, s) != nil {
			continue // a corrupt sample is skipped rather than failing the dashboard
		}
		if id != engine {
			flush()
			engine, nodeName = id, name
		}
		if prev != nil && at.After(prevAt) && s.QueriesTotal >= prev.QueriesTotal && s.StartedUnixMs == prev.StartedUnixMs {
			step := at.Truncate(spec.step)
			if engineSteps[step] == nil {
				engineSteps[step] = &counts{}
			}
			engineSteps[step].add(pairDelta(prev, s, at.Sub(prevAt).Seconds()))
		}
		prev, prevAt, last = s, at, s
	}
	if err := rows.Err(); err != nil {
		return out, store.MapError(err)
	}
	flush()

	for at, p := range fleetSteps {
		finishPoint(p, fleetCounts[at])
		out.Points = append(out.Points, *p)
	}
	slices.SortFunc(out.Points, func(a, b SeriesPoint) int { return a.At.Compare(b.At) })
	slices.SortFunc(out.Engines, func(a, b EngineFigures) int { return strings.Compare(a.NodeName, b.NodeName) })
	return out, nil
}

func grow(a, b uint64) uint64 {
	if b < a {
		return 0
	}
	return b - a
}

func growMap(a, b map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(b))
	for k, v := range b {
		out[k] = grow(a[k], v)
	}
	return out
}

func growHistogram(a, b []uint64) []uint64 {
	if len(a) != len(b) || len(b) == 0 {
		return nil
	}
	out := make([]uint64, len(b))
	for i := range b {
		out[i] = grow(a[i], b[i])
	}
	return out
}

// pairDelta is the counter growth from sample a to sample b, seconds apart.
func pairDelta(a, b *controlv1.Stats, seconds float64) *counts {
	c := &counts{
		seconds: seconds, queries: grow(a.QueriesTotal, b.QueriesTotal), blocked: grow(a.FilterBlockedTotal, b.FilterBlockedTotal),
		rewritten: grow(a.FilterRewrittenTotal, b.FilterRewrittenTotal), hits: grow(a.CacheHitsTotal, b.CacheHitsTotal),
		misses: grow(a.CacheMissesTotal, b.CacheMissesTotal), stale: grow(a.CacheStaleServedTotal, b.CacheStaleServedTotal),
		resFailures: grow(a.ResolutionFailuresTotal, b.ResolutionFailuresTotal),
		byTransport: growMap(a.QueriesByTransport, b.QueriesByTransport), byRcode: growMap(a.QueriesByRcode, b.QueriesByRcode),
		byRoute:     growMap(a.AnswersByRoute, b.AnswersByRoute),
		byCategory:  growMap(a.GetFilterIndex().GetBlockedByCategory(), b.GetFilterIndex().GetBlockedByCategory()),
		recUpstream: grow(a.GetRecursion().GetUpstreamQueries(), b.GetRecursion().GetUpstreamQueries()),
		recTimeouts: grow(a.GetRecursion().GetUpstreamTimeouts(), b.GetRecursion().GetUpstreamTimeouts()),
		lame:        grow(a.GetRecursion().GetLameMarked(), b.GetRecursion().GetLameMarked()),
		secure:      grow(a.GetDnssec().GetSecure(), b.GetDnssec().GetSecure()),
		insecure:    grow(a.GetDnssec().GetInsecure(), b.GetDnssec().GetInsecure()),
		bogus:       grow(a.GetDnssec().GetBogus(), b.GetDnssec().GetBogus()),
	}
	if len(b.DurationBucketBoundsUs) == len(b.DurationBucketCounts) {
		c.bounds = b.DurationBucketBoundsUs
		c.duration = growHistogram(a.DurationBucketCounts, b.DurationBucketCounts)
		if len(b.MissDurationBucketCounts) == len(c.bounds) {
			c.missDuration = growHistogram(a.MissDurationBucketCounts, b.MissDurationBucketCounts)
		}
	}
	return c
}

// add sums o into c; histograms only add when their bounds match c's (the first set wins).
func (c *counts) add(o *counts) {
	c.seconds += o.seconds
	c.queries += o.queries
	c.blocked += o.blocked
	c.rewritten += o.rewritten
	c.hits += o.hits
	c.misses += o.misses
	c.stale += o.stale
	c.recUpstream += o.recUpstream
	c.recTimeouts += o.recTimeouts
	c.lame += o.lame
	c.resFailures += o.resFailures
	c.secure += o.secure
	c.insecure += o.insecure
	c.bogus += o.bogus
	for _, m := range []struct {
		dst *map[string]uint64
		src map[string]uint64
	}{
		{&c.byTransport, o.byTransport}, {&c.byRcode, o.byRcode}, {&c.byCategory, o.byCategory}, {&c.byRoute, o.byRoute},
	} {
		if *m.dst == nil {
			*m.dst = map[string]uint64{}
		}
		for k, v := range m.src {
			(*m.dst)[k] += v
		}
	}
	if o.bounds == nil {
		return
	}
	if c.bounds == nil {
		c.bounds = o.bounds
	}
	if !slices.Equal(c.bounds, o.bounds) {
		return
	}
	c.duration = addHistogram(c.duration, o.duration)
	c.missDuration = addHistogram(c.missDuration, o.missDuration)
}

func addHistogram(dst, src []uint64) []uint64 {
	if src == nil {
		return dst
	}
	if dst == nil {
		dst = make([]uint64, len(src))
	}
	for i := range src {
		dst[i] += src[i]
	}
	return dst
}

func newPoint(at time.Time) *SeriesPoint {
	return &SeriesPoint{At: at, QPSByTransport: map[string]float64{}, QPSByRcode: map[string]float64{},
		BlockedByCategory: map[string]float64{}, AnswersByRoute: map[string]float64{}}
}

// addRates adds one engine's per-second rates over a step to the fleet point.
func addRates(p *SeriesPoint, c *counts) {
	if c.seconds <= 0 {
		return
	}
	rate := func(n uint64) float64 { return float64(n) / c.seconds }
	p.QPS += rate(c.queries)
	p.BlockedQPS += rate(c.blocked)
	p.RewrittenQPS += rate(c.rewritten)
	p.RecursionUpstreamQPS += rate(c.recUpstream)
	p.RecursionTimeoutsQPS += rate(c.recTimeouts)
	p.ResolutionFailuresQPS += rate(c.resFailures)
	p.DNSSECSecureQPS += rate(c.secure)
	p.DNSSECInsecureQPS += rate(c.insecure)
	p.DNSSECBogusQPS += rate(c.bogus)
	for _, m := range []struct {
		dst map[string]float64
		src map[string]uint64
	}{{p.QPSByTransport, c.byTransport}, {p.QPSByRcode, c.byRcode}, {p.BlockedByCategory, c.byCategory}, {p.AnswersByRoute, c.byRoute}} {
		for k, v := range m.src {
			m.dst[k] += rate(v)
		}
	}
}

// finishPoint sets the figures of a fleet point that come from the summed counts: ratios,
// percentiles and lame marks.
func finishPoint(p *SeriesPoint, c *counts) {
	p.LameMarked = float64(c.lame)
	if lookups := c.hits + c.misses; lookups > 0 {
		p.CacheHitRatio = float64(c.hits) / float64(lookups)
		p.CacheMissRatio = float64(c.misses) / float64(lookups)
		p.CacheStaleRatio = float64(c.stale) / float64(lookups)
	}
	zero := make([]uint64, len(c.bounds))
	if c.duration != nil {
		p.P50Ms = fleet.Percentile(c.bounds, zero, c.duration, 0.5)
		p.P95Ms = fleet.Percentile(c.bounds, zero, c.duration, 0.95)
		p.P99Ms = fleet.Percentile(c.bounds, zero, c.duration, 0.99)
	}
	if c.missDuration != nil {
		p.MissP50Ms = fleet.Percentile(c.bounds, zero, c.missDuration, 0.5)
		p.MissP95Ms = fleet.Percentile(c.bounds, zero, c.missDuration, 0.95)
		p.MissP99Ms = fleet.Percentile(c.bounds, zero, c.missDuration, 0.99)
	}
}
