package stats

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

const (
	freshness      = 60 * time.Second
	collectTimeout = 5 * time.Second
)

var (
	descQPS           = prometheus.NewDesc("nexora_fleet_qps", "Queries per second of the engine between its two newest stats samples", []string{"engine"}, nil)
	descQueries       = prometheus.NewDesc("nexora_fleet_queries_total", "DNS replies sent by the engine", []string{"engine"}, nil)
	descDuration      = prometheus.NewDesc("nexora_fleet_query_duration_seconds", "Engine time from query arrival to reply", []string{"engine"}, nil)
	descCacheRatio    = prometheus.NewDesc("nexora_fleet_cache_hit_ratio", "Cache hits over cache lookups of the engine", []string{"engine"}, nil)
	descUpstreamUp    = prometheus.NewDesc("nexora_fleet_upstream_up", "Whether the engine considers the upstream healthy", []string{"engine", "upstream"}, nil)
	descConnected     = prometheus.NewDesc("nexora_fleet_engines_connected", "Enrolled engines with a live control stream", nil, nil)
	descVersion       = prometheus.NewDesc("nexora_mgmt_config_version", "Newest published config version", nil, nil)
	descListStale     = prometheus.NewDesc("nexora_mgmt_filter_list_stale", "Whether the filter list failed its last refresh or is older than two intervals", []string{"list"}, nil)
	descListSuccess   = prometheus.NewDesc("nexora_mgmt_filter_list_last_success_timestamp_seconds", "Time of the filter list's last successful refresh", []string{"list"}, nil)
	descCategoryStale = prometheus.NewDesc("nexora_mgmt_filter_category_stale", "Whether an enabled filter category has an enabled source that failed its last refresh or is older than two intervals", []string{"category"}, nil)
)

type collector struct{ st *store.Store }

// NewCollector exports fleet metrics from the stored engine stats (each non-deleted engine's
// samples of the last 60 s, labelled by node name) and filter-list freshness, read at scrape time.
func NewCollector(st *store.Store) prometheus.Collector { return collector{st: st} }

func (collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{descQPS, descQueries, descDuration, descCacheRatio, descUpstreamUp,
		descConnected, descVersion, descListStale, descListSuccess, descCategoryStale} {
		ch <- d
	}
}

func (c collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
	defer cancel()
	// A database failure leaves the affected families out of this scrape; Prometheus' own `up`
	// stays healthy because the process metrics still answer.
	if err := c.collectEngines(ctx, ch); err != nil {
		slog.Warn("fleet metrics", "err", err)
	}
	if err := c.collectMgmt(ctx, ch); err != nil {
		slog.Warn("management metrics", "err", err)
	}
	if err := c.collectCategories(ctx, ch); err != nil {
		slog.Warn("filter category metrics", "err", err)
	}
}

func (c collector) collectEngines(ctx context.Context, ch chan<- prometheus.Metric) error {
	rows, err := c.st.Pool.Query(ctx, `select e.id::text, e.node_name, s.stats from engines e
		cross join lateral (select at, stats from engine_stats where engine_id = e.id and at > now() - $1 * interval '1 second'
			order by at desc limit 2) s
		where e.deleted_at is null order by e.id, s.at desc`, int64(freshness.Seconds()))
	if err != nil {
		return store.MapError(err)
	}
	type pair struct{ newest, previous *controlv1.Stats }
	engines := map[string]*pair{} // by node name
	owner := map[string]string{}  // node name -> engine id whose samples it shows
	var order []string
	for rows.Next() {
		var id, node string
		var raw []byte
		if err := rows.Scan(&id, &node, &raw); err != nil {
			rows.Close()
			return store.MapError(err)
		}
		s := &controlv1.Stats{}
		if err := proto.Unmarshal(raw, s); err != nil {
			continue
		}
		if o, ok := owner[node]; ok && o != id {
			continue // two engines share the node name: duplicate label sets would fail the scrape
		}
		p := engines[node]
		if p == nil {
			p = &pair{}
			engines[node] = p
			owner[node] = id
			order = append(order, node)
		}
		if p.newest == nil {
			p.newest = s
		} else {
			p.previous = s
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return store.MapError(err)
	}
	for _, node := range order {
		p := engines[node]
		s := p.newest
		qps := 0.0
		if prev := p.previous; prev != nil && s.QueriesTotal >= prev.QueriesTotal && s.UnixMs > prev.UnixMs {
			qps = float64(s.QueriesTotal-prev.QueriesTotal) / (float64(s.UnixMs-prev.UnixMs) / 1000)
		}
		ch <- prometheus.MustNewConstMetric(descQPS, prometheus.GaugeValue, qps, node)
		ch <- prometheus.MustNewConstMetric(descQueries, prometheus.CounterValue, float64(s.QueriesTotal), node)
		ratio := 0.0
		if lookups := s.CacheHitsTotal + s.CacheMissesTotal; lookups > 0 {
			ratio = float64(s.CacheHitsTotal) / float64(lookups)
		}
		ch <- prometheus.MustNewConstMetric(descCacheRatio, prometheus.GaugeValue, ratio, node)
		if len(s.DurationBucketBoundsUs) == len(s.DurationBucketCounts) {
			buckets := make(map[float64]uint64, len(s.DurationBucketBoundsUs))
			count := s.QueriesTotal
			for i, b := range s.DurationBucketBoundsUs {
				buckets[float64(b)/1e6] = s.DurationBucketCounts[i]
				count = max(count, s.DurationBucketCounts[i])
			}
			ch <- prometheus.MustNewConstHistogram(descDuration, count, float64(s.DurationSumUs)/1e6, buckets, node)
		}
		seen := map[string]bool{}
		for _, u := range s.Upstreams {
			if seen[u.Name] {
				continue // duplicate label sets would fail the whole scrape
			}
			seen[u.Name] = true
			up := 0.0
			if u.Up {
				up = 1
			}
			ch <- prometheus.MustNewConstMetric(descUpstreamUp, prometheus.GaugeValue, up, node, u.Name)
		}
	}
	return nil
}

func (c collector) collectMgmt(ctx context.Context, ch chan<- prometheus.Metric) error {
	var connected int
	var version int64
	if err := c.st.Pool.QueryRow(ctx, `select
		(select count(*) from engines e join instances i on i.id = e.connected_instance
			where e.deleted_at is null and i.heartbeat_at > now() - interval '15 seconds'),
		(select coalesce(max(version), 0) from config_versions)`).Scan(&connected, &version); err != nil {
		return store.MapError(err)
	}
	ch <- prometheus.MustNewConstMetric(descConnected, prometheus.GaugeValue, float64(connected))
	ch <- prometheus.MustNewConstMetric(descVersion, prometheus.GaugeValue, float64(version))
	rows, err := c.st.Pool.Query(ctx, `select name,
		(last_error <> '' or (last_success_at is not null and
			last_success_at < now() - 2 * refresh_interval_seconds * interval '1 second')),
		last_success_at from filter_lists`)
	if err != nil {
		return store.MapError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var stale bool
		var success *time.Time
		if err := rows.Scan(&name, &stale, &success); err != nil {
			return store.MapError(err)
		}
		v := 0.0
		if stale {
			v = 1
		}
		ch <- prometheus.MustNewConstMetric(descListStale, prometheus.GaugeValue, v, name)
		if success != nil {
			ch <- prometheus.MustNewConstMetric(descListSuccess, prometheus.GaugeValue, float64(success.Unix()), name)
		}
	}
	return store.MapError(rows.Err())
}

// collectCategories exports one staleness sample per catalog category; a disabled category is never
// stale, and an enabled source that never refreshed counts as stale.
func (c collector) collectCategories(ctx context.Context, ch chan<- prometheus.Metric) error {
	rows, err := c.st.Pool.Query(ctx, `select c.key, c.enabled and coalesce(bool_or(f.enabled and (f.last_error <> ''
			or f.last_success_at is null or f.last_success_at < now() - 2 * f.refresh_interval_seconds * interval '1 second')), false)
		from filter_categories c left join filter_lists f on f.category_key = c.key group by c.key, c.enabled`)
	if err != nil {
		return store.MapError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var stale bool
		if err := rows.Scan(&key, &stale); err != nil {
			return store.MapError(err)
		}
		v := 0.0
		if stale {
			v = 1
		}
		ch <- prometheus.MustNewConstMetric(descCategoryStale, prometheus.GaugeValue, v, key)
	}
	return store.MapError(rows.Err())
}
