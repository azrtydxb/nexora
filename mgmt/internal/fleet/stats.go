package fleet

import (
	"context"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Point is one engine's rates between two consecutive engine_stats samples.
type Point struct {
	At                                       time.Time
	QPS, CacheHitRatio, ServfailRatio, P99Ms float64
}

// Series derives an engine's rates over the last window from its engine_stats samples. Pairs whose
// query counter went down (engine restart) are skipped.
func Series(ctx context.Context, q store.PolicyQuerier, engineID uuid.UUID, window time.Duration) ([]Point, error) {
	rows, err := q.Query(ctx, `select at, stats from engine_stats where engine_id = $1
		and at > now() - $2 * interval '1 second' order by at`, engineID, window.Seconds())
	if err != nil {
		return nil, store.MapError(err)
	}
	defer rows.Close()
	points := []Point{}
	var prev *controlv1.Stats
	var prevAt time.Time
	for rows.Next() {
		var at time.Time
		var raw []byte
		if err := rows.Scan(&at, &raw); err != nil {
			return nil, store.MapError(err)
		}
		s := &controlv1.Stats{}
		if proto.Unmarshal(raw, s) != nil {
			continue
		}
		if prev != nil && s.QueriesTotal >= prev.QueriesTotal && at.After(prevAt) {
			points = append(points, point(prev, s, at, at.Sub(prevAt).Seconds()))
		}
		prev, prevAt = s, at
	}
	return points, store.MapError(rows.Err())
}

func point(a, b *controlv1.Stats, at time.Time, seconds float64) Point {
	p := Point{At: at}
	dq := b.QueriesTotal - a.QueriesTotal
	p.QPS = float64(dq) / seconds
	if dh, dm := delta(a.CacheHitsTotal, b.CacheHitsTotal), delta(a.CacheMissesTotal, b.CacheMissesTotal); dh+dm > 0 {
		p.CacheHitRatio = float64(dh) / float64(dh+dm)
	}
	if dq > 0 {
		p.ServfailRatio = float64(delta(a.ServfailTotal, b.ServfailTotal)) / float64(dq)
	}
	bounds, counts, prevCounts := b.DurationBucketBoundsUs, b.DurationBucketCounts, a.DurationBucketCounts
	if len(counts) == len(bounds) && len(prevCounts) == len(counts) && len(counts) > 0 {
		total := delta(prevCounts[len(prevCounts)-1], counts[len(counts)-1])
		if total > 0 {
			for i := range counts {
				if float64(delta(prevCounts[i], counts[i])) >= 0.99*float64(total) {
					p.P99Ms = float64(bounds[i]) / 1000
					break
				}
			}
		}
	}
	return p
}

func delta(a, b uint64) uint64 {
	if b < a {
		return 0
	}
	return b - a
}
