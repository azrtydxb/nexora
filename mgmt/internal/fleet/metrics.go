package fleet

import (
	"context"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// MetricsPoint is one engine's rates and gauges between two consecutive engine_stats samples.
type MetricsPoint struct {
	At                                                                                                 time.Time
	QPS, P50Ms, P99Ms, CacheHitRatio, ServfailRatio, NXDomainRatio, RefusedRatio, BlockedQPS, CPUCores float64
	ResidentBytes, MemoryLimitBytes                                                                    uint64
	Connections                                                                                        map[string]uint64
}

// UpstreamPoint is one upstream's round-trip gauge and rates between two consecutive samples.
type UpstreamPoint struct {
	At                                          time.Time
	RTTMs, FailuresPerSecond, RaceWinsPerSecond float64
}

// EngineMetricsData is the engine modal's series over a window.
type EngineMetricsData struct {
	Points    []MetricsPoint
	Upstreams map[string][]UpstreamPoint
	StartedAt *time.Time
	Restarts  int
}

// EngineMetrics derives an engine's series over the last window from its engine_stats samples. A
// pair whose query counter went down or whose start time changed counts as a restart and gives no
// point.
func EngineMetrics(ctx context.Context, q store.PolicyQuerier, engineID uuid.UUID, window time.Duration) (EngineMetricsData, error) {
	d := EngineMetricsData{Points: []MetricsPoint{}, Upstreams: map[string][]UpstreamPoint{}}
	rows, err := q.Query(ctx, `select at, stats from engine_stats where engine_id = $1
		and at > now() - $2 * interval '1 second' order by at`, engineID, window.Seconds())
	if err != nil {
		return d, store.MapError(err)
	}
	defer rows.Close()
	var prev *controlv1.Stats
	var prevAt time.Time
	for rows.Next() {
		var at time.Time
		var raw []byte
		if err := rows.Scan(&at, &raw); err != nil {
			return d, store.MapError(err)
		}
		s := &controlv1.Stats{}
		if proto.Unmarshal(raw, s) != nil {
			continue
		}
		if prev != nil {
			switch {
			case s.QueriesTotal < prev.QueriesTotal || s.StartedUnixMs != prev.StartedUnixMs:
				d.Restarts++
			case at.After(prevAt):
				seconds := at.Sub(prevAt).Seconds()
				d.Points = append(d.Points, metricsPoint(prev, s, at, seconds))
				appendUpstreams(d.Upstreams, prev, s, at, seconds)
			}
		}
		prev, prevAt = s, at
	}
	if err := rows.Err(); err != nil {
		return d, store.MapError(err)
	}
	if prev != nil && prev.StartedUnixMs > 0 {
		started := time.UnixMilli(prev.StartedUnixMs)
		d.StartedAt = &started
	}
	return d, nil
}

func metricsPoint(a, b *controlv1.Stats, at time.Time, seconds float64) MetricsPoint {
	p := MetricsPoint{At: at, ResidentBytes: b.ProcessResidentBytes, MemoryLimitBytes: b.MemoryLimitBytes, Connections: b.OpenConnections}
	if p.Connections == nil {
		p.Connections = map[string]uint64{}
	}
	dq := b.QueriesTotal - a.QueriesTotal
	p.QPS = float64(dq) / seconds
	p.BlockedQPS = float64(delta(a.FilterBlockedTotal, b.FilterBlockedTotal)) / seconds
	if cpu := b.ProcessCpuSecondsTotal - a.ProcessCpuSecondsTotal; cpu > 0 {
		p.CPUCores = cpu / seconds
	}
	if dh, dm := delta(a.CacheHitsTotal, b.CacheHitsTotal), delta(a.CacheMissesTotal, b.CacheMissesTotal); dh+dm > 0 {
		p.CacheHitRatio = float64(dh) / float64(dh+dm)
	}
	if dq > 0 {
		rcode := func(name string) float64 {
			return float64(delta(a.QueriesByRcode[name], b.QueriesByRcode[name])) / float64(dq)
		}
		if len(b.QueriesByRcode) > 0 {
			p.ServfailRatio, p.NXDomainRatio, p.RefusedRatio = rcode("SERVFAIL"), rcode("NXDOMAIN"), rcode("REFUSED")
		} else {
			p.ServfailRatio = float64(delta(a.ServfailTotal, b.ServfailTotal)) / float64(dq)
		}
	}
	p.P50Ms = Percentile(b.DurationBucketBoundsUs, a.DurationBucketCounts, b.DurationBucketCounts, 0.5)
	p.P99Ms = Percentile(b.DurationBucketBoundsUs, a.DurationBucketCounts, b.DurationBucketCounts, 0.99)
	return p
}

func appendUpstreams(out map[string][]UpstreamPoint, a, b *controlv1.Stats, at time.Time, seconds float64) {
	for _, u := range b.Upstreams {
		var before *controlv1.UpstreamStatus
		for _, v := range a.Upstreams {
			if v.Name == u.Name {
				before = v
				break
			}
		}
		if before == nil {
			continue
		}
		out[u.Name] = append(out[u.Name], UpstreamPoint{
			At:                at,
			RTTMs:             float64(u.RttUs) / 1000,
			FailuresPerSecond: float64(delta(before.FailuresTotal, u.FailuresTotal)) / seconds,
			RaceWinsPerSecond: float64(delta(before.RaceWinsTotal, u.RaceWinsTotal)) / seconds,
		})
	}
}

// Percentile returns, in ms, the first bucket bound whose count delta between two cumulative
// histograms reaches p of the total delta; 0 when the histograms do not line up or saw no queries.
func Percentile(bounds, cumulativeA, cumulativeB []uint64, p float64) float64 {
	n := len(bounds)
	if n == 0 || len(cumulativeA) != n || len(cumulativeB) != n {
		return 0
	}
	total := delta(cumulativeA[n-1], cumulativeB[n-1])
	if total == 0 {
		return 0
	}
	for i := range bounds {
		if float64(delta(cumulativeA[i], cumulativeB[i])) >= p*float64(total) {
			return float64(bounds[i]) / 1000
		}
	}
	return float64(bounds[n-1]) / 1000
}
