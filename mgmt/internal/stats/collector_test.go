package stats_test

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/piwi3910/nexora/e2e/harness"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/catalog"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/stats"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func TestCollectorExportsFleetMetrics(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ctx := context.Background()
	st, err := store.Open(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, "insert into instances(id) values ('i1')"); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := st.Pool.QueryRow(ctx, "insert into engines(node_name, certificate_serial, connected_instance) values ('edge-1','1','i1') returning id").Scan(&id); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	bounds := []uint64{50, 100, 250, 500, 1000, 2500, 5000, 10000, 25000, 50000, 100000, 250000, 500000, 1000000, 2000000}
	counts := make([]uint64, 15)
	for i := range counts {
		counts[i] = uint64(i+1) * 100
	}
	for i, total := range []uint64{1000, 3000} {
		s := &controlv1.Stats{UnixMs: now - int64(10000*(1-i)), QueriesTotal: total, CacheHitsTotal: total / 2, CacheMissesTotal: total / 2,
			DurationBucketBoundsUs: bounds, DurationBucketCounts: counts, DurationSumUs: 5000,
			Upstreams: []*controlv1.UpstreamStatus{{Id: "u", Name: "fx", Up: true, RttUs: 900}}}
		if err := stats.Record(ctx, st, id, s); err != nil {
			t.Fatal(err)
		}
	}
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(stats.NewCollector(st))
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*dto.MetricFamily{}
	for _, f := range fams {
		got[f.GetName()] = f
	}
	for _, n := range []string{"nexora_fleet_qps", "nexora_fleet_queries_total", "nexora_fleet_query_duration_seconds", "nexora_fleet_cache_hit_ratio", "nexora_fleet_upstream_up", "nexora_fleet_engines_connected"} {
		if got[n] == nil {
			t.Errorf("missing %s", n)
		}
	}
	if got["nexora_fleet_qps"] == nil {
		t.FailNow()
	}
	if q := got["nexora_fleet_qps"].GetMetric()[0].GetGauge().GetValue(); q != 200 {
		t.Fatalf("qps = %v", q)
	}
}

func TestCollectorExportsCategoryStaleness(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Sync(ctx, st, snapshot.BuildConfig{}, cat, catalog.Raw()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, "update filter_categories set enabled = true where key = 'gambling'"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, "update filter_lists set last_error = 'boom' where source_key = 'hagezi-gambling'"); err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewRegistry()
	reg.MustRegister(stats.NewCollector(st))
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]float64{}
	for _, f := range fams {
		if f.GetName() != "nexora_mgmt_filter_category_stale" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "category" {
					values[l.GetValue()] = m.GetGauge().GetValue()
				}
			}
		}
	}
	if v, ok := values["gambling"]; !ok || v != 1 {
		t.Fatalf("gambling stale = %v (present %v), want 1: %v", v, ok, values)
	}
	if v, ok := values["adult"]; !ok || v != 0 {
		t.Fatalf("adult (disabled) stale = %v (present %v), want 0: %v", v, ok, values)
	}
}
