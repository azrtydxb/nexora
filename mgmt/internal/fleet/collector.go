package fleet

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/piwi3910/nexora/mgmt/internal/store"
)

const collectTimeout = 5 * time.Second

var (
	descEngines = prometheus.NewDesc("nexora_mgmt_engines", "Engines by engine group and status",
		[]string{"engine_group", "status"}, nil)
	descDisconnected = prometheus.NewDesc("nexora_mgmt_engines_disconnected",
		"Non-revoked engines without a live control stream for more than 60 seconds", nil, nil)
	descRollouts = prometheus.NewDesc("nexora_mgmt_rollouts", "Open and halted rollouts by engine group and state",
		[]string{"engine_group", "state"}, nil)
)

type collector struct{ st *store.Store }

// NewCollector exports the fleet gauges, read from PostgreSQL at scrape time; a database error
// leaves the affected families out of the scrape.
func NewCollector(st *store.Store) prometheus.Collector { return collector{st: st} }

func (collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- descEngines
	ch <- descDisconnected
	ch <- descRollouts
}

func (c collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
	defer cancel()
	for name, collect := range map[string]func(context.Context, chan<- prometheus.Metric) error{
		"engines": c.collectEngines, "disconnected": c.collectDisconnected, "rollouts": c.collectRollouts,
	} {
		if err := collect(ctx, ch); err != nil {
			slog.Warn("fleet gauges", "family", name, "err", err)
		}
	}
}

func (c collector) collectEngines(ctx context.Context, ch chan<- prometheus.Metric) error {
	views, err := ListEngines(ctx, c.st.Pool, EngineFilter{})
	if err != nil {
		return err
	}
	type key struct{ group, status string }
	counts := map[key]int{}
	for _, v := range views {
		counts[key{v.EngineGroupName, v.Status}]++
	}
	for k, n := range counts {
		ch <- prometheus.MustNewConstMetric(descEngines, prometheus.GaugeValue, float64(n), k.group, k.status)
	}
	return nil
}

func (c collector) collectDisconnected(ctx context.Context, ch chan<- prometheus.Metric) error {
	var n int
	if err := c.st.Pool.QueryRow(ctx, `select count(*) from engines e left join instances i on i.id = e.connected_instance
		where e.deleted_at is null and e.revoked_at is null
		and not (e.connected_instance is not null and coalesce(i.heartbeat_at > now() - interval '15 seconds', false))
		and coalesce(e.last_seen_at, e.enrolled_at) < now() - interval '60 seconds'`).Scan(&n); err != nil {
		return store.MapError(err)
	}
	ch <- prometheus.MustNewConstMetric(descDisconnected, prometheus.GaugeValue, float64(n))
	return nil
}

func (c collector) collectRollouts(ctx context.Context, ch chan<- prometheus.Metric) error {
	rows, err := c.st.Pool.Query(ctx, `select g.name, r.state, count(*) from rollouts r join engine_groups g on g.id = r.engine_group_id
		where r.state in ('pending', 'canary', 'verifying', 'rolling', 'halted') group by 1, 2`)
	if err != nil {
		return store.MapError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var group, state string
		var n int
		if err := rows.Scan(&group, &state, &n); err != nil {
			return store.MapError(err)
		}
		ch <- prometheus.MustNewConstMetric(descRollouts, prometheus.GaugeValue, float64(n), group, state)
	}
	return store.MapError(rows.Err())
}
