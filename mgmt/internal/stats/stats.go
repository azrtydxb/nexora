// Package stats stores engine Stats samples and derives the dashboard from them.
package stats

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

const (
	retention = 24 * time.Hour
	// rollupRetention keeps the 7-day dashboard range covered.
	rollupRetention = 8 * 24 * time.Hour
	pruneEvery      = 100
	window          = 5 * time.Minute
	bucketWidth     = 30 * time.Second
)

// inserts counts Record calls per engine (engine id -> *atomic.Uint64) to pace pruning.
var inserts sync.Map

// Record stores one Stats sample received now, replaces the engine's rollup sample of the current
// 5-minute bucket with it and, once per 100 samples of that engine, deletes its samples older than
// 24 h and its rollup rows older than 8 days.
func Record(ctx context.Context, st *store.Store, engineID string, s *controlv1.Stats) error {
	raw, err := proto.Marshal(s)
	if err != nil {
		return err
	}
	if _, err := st.Pool.Exec(ctx, `insert into engine_stats(engine_id, at, stats) values ($1, now(), $2)
		on conflict do nothing`, engineID, raw); err != nil {
		return store.MapError(err)
	}
	if _, err := st.Pool.Exec(ctx, `insert into engine_stats_rollup(engine_id, bucket, stats)
		values ($1, date_bin('5 minutes', now(), timestamptz 'epoch'), $2)
		on conflict (engine_id, bucket) do update set stats = excluded.stats`, engineID, raw); err != nil {
		return store.MapError(err)
	}
	c, _ := inserts.LoadOrStore(engineID, new(atomic.Uint64))
	if c.(*atomic.Uint64).Add(1)%pruneEvery == 0 {
		if _, err := st.Pool.Exec(ctx, "delete from engine_stats where engine_id = $1 and at < now() - $2 * interval '1 second'",
			engineID, int64(retention.Seconds())); err != nil {
			return store.MapError(err)
		}
		_, err = st.Pool.Exec(ctx, "delete from engine_stats_rollup where engine_id = $1 and bucket < now() - $2 * interval '1 second'",
			engineID, int64(rollupRetention.Seconds()))
	}
	return store.MapError(err)
}

// DashboardData is the fleet overview.
type DashboardData struct {
	QueriesTotal, BlockedTotal     uint64
	QPS, CacheHitRatio             float64
	EnginesTotal, EnginesConnected int
	Upstreams                      []UpstreamHealth
	Series                         []Point
}

// UpstreamHealth aggregates one upstream across the engines reporting it.
type UpstreamHealth struct {
	Name                    string
	UpEngines, TotalEngines int
	RTTMs                   float64
}

// Point is the fleet QPS of one 30 s bucket.
type Point struct {
	At  time.Time
	QPS float64
}

type sample struct {
	at    time.Time
	stats *controlv1.Stats
}

// Dashboard summarises the samples of the last 5 minutes of every enrolled engine: totals and
// upstream health from each engine's newest sample, QPS from consecutive samples per engine.
func Dashboard(ctx context.Context, st *store.Store) (DashboardData, error) {
	var d DashboardData
	if err := st.Pool.QueryRow(ctx, `select count(*),
		count(*) filter (where e.connected_instance is not null and i.heartbeat_at > now() - interval '15 seconds')
		from engines e left join instances i on i.id = e.connected_instance where e.deleted_at is null`).
		Scan(&d.EnginesTotal, &d.EnginesConnected); err != nil {
		return d, store.MapError(err)
	}
	rows, err := st.Pool.Query(ctx, `select s.engine_id::text, s.at, s.stats from engine_stats s
		join engines e on e.id = s.engine_id
		where e.deleted_at is null and s.at > now() - $1 * interval '1 second' order by s.engine_id, s.at`,
		int64(window.Seconds()))
	if err != nil {
		return d, store.MapError(err)
	}
	perEngine := map[string][]sample{}
	for rows.Next() {
		var id string
		var at time.Time
		var raw []byte
		if err := rows.Scan(&id, &at, &raw); err != nil {
			rows.Close()
			return d, store.MapError(err)
		}
		s := &controlv1.Stats{}
		if err := proto.Unmarshal(raw, s); err != nil {
			continue // a corrupt sample is skipped rather than failing the dashboard
		}
		perEngine[id] = append(perEngine[id], sample{at: at, stats: s})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return d, store.MapError(err)
	}

	var hits, misses uint64
	upstreams := map[string]*UpstreamHealth{}
	var rttSum = map[string]float64{}
	buckets := map[time.Time]float64{}
	for _, samples := range perEngine {
		latest := samples[len(samples)-1].stats
		d.QueriesTotal += latest.QueriesTotal
		d.BlockedTotal += latest.FilterBlockedTotal
		hits += latest.CacheHitsTotal
		misses += latest.CacheMissesTotal
		for _, u := range latest.Upstreams {
			h := upstreams[u.Name]
			if h == nil {
				h = &UpstreamHealth{Name: u.Name}
				upstreams[u.Name] = h
			}
			h.TotalEngines++
			if u.Up {
				h.UpEngines++
				rttSum[u.Name] += float64(u.RttUs) / 1000
			}
		}
		type acc struct {
			sum float64
			n   int
		}
		engineBuckets := map[time.Time]*acc{}
		lastRate := 0.0
		for i := 1; i < len(samples); i++ {
			prev, cur := samples[i-1], samples[i]
			secs := cur.at.Sub(prev.at).Seconds()
			if secs <= 0 || cur.stats.QueriesTotal < prev.stats.QueriesTotal {
				continue // counter reset (engine restart) or duplicate timestamp
			}
			rate := float64(cur.stats.QueriesTotal-prev.stats.QueriesTotal) / secs
			lastRate = rate
			b := cur.at.Truncate(bucketWidth)
			if engineBuckets[b] == nil {
				engineBuckets[b] = &acc{}
			}
			engineBuckets[b].sum += rate
			engineBuckets[b].n++
		}
		d.QPS += lastRate
		for b, a := range engineBuckets {
			buckets[b] += a.sum / float64(a.n)
		}
	}
	if hits+misses > 0 {
		d.CacheHitRatio = float64(hits) / float64(hits+misses)
	}
	for name, h := range upstreams {
		if h.UpEngines > 0 {
			h.RTTMs = rttSum[name] / float64(h.UpEngines)
		}
		d.Upstreams = append(d.Upstreams, *h)
	}
	sort.Slice(d.Upstreams, func(i, j int) bool { return d.Upstreams[i].Name < d.Upstreams[j].Name })
	for at, qps := range buckets {
		d.Series = append(d.Series, Point{At: at, QPS: qps})
	}
	sort.Slice(d.Series, func(i, j int) bool { return d.Series[i].At.Before(d.Series[j].At) })
	return d, nil
}
