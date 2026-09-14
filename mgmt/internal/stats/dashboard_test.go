package stats_test

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/catalog"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/stats"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func insertRaw(t *testing.T, st *store.Store, table, column string, id uuid.UUID, at time.Time, s *controlv1.Stats) {
	t.Helper()
	raw, err := proto.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(context.Background(), "insert into "+table+" (engine_id, "+column+", stats) values ($1, $2, $3)", id, at, raw); err != nil {
		t.Fatal(err)
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// sampleAt builds the i-th cumulative sample (10 s apart) of edge-1 (first) or edge-2; edge-2 grows
// slower and reports no rcode, miss histogram, recursion, DNSSEC or alert-worthy fields.
func sampleAt(i uint64, first bool, now time.Time) *controlv1.Stats {
	s := &controlv1.Stats{DurationBucketBoundsUs: []uint64{1000, 10000, 100000}}
	if first { // edge-1
		s.QueriesTotal = 5000 + 1000*i
		s.CacheHitsTotal, s.CacheMissesTotal, s.CacheStaleServedTotal = 700*i, 300*i, 30*i
		s.FilterBlockedTotal = 60 * i
		s.DurationBucketCounts = []uint64{500 * i, 950 * i, 1000 * i}
		s.MissDurationBucketCounts = []uint64{0, 100 * i, 200 * i}
		s.QueriesByTransport = map[string]uint64{"udp": 800 * i, "tcp": 200 * i}
		s.QueriesByRcode = map[string]uint64{"NOERROR": 900 * i, "NXDOMAIN": 100 * i}
		s.AnswersByRoute = map[string]uint64{"cache": 600 * i, "forwarded": 400 * i}
		s.FilterRewrittenTotal = 10 * i
		s.ResolutionFailuresTotal = 7 * i
		s.Dnssec = &controlv1.DnssecStats{Secure: 40 * i, Insecure: 60 * i, Bogus: 20 * i,
			TrustAnchors: []*controlv1.TrustAnchorStatus{{Zone: ".", KeyTag: 20326, LastError: "refresh timed out"}}}
		s.Recursion = &controlv1.RecursionStats{UpstreamQueries: 40 * i, UpstreamTimeouts: 5 * i, LameMarked: 3 * i}
		s.FilterIndex = &controlv1.FilterIndexStats{Bytes: 4096, BlockedByCategory: map[string]uint64{"ads-tracking": 50 * i}}
		s.TlsCertificateNotAfterUnix = now.Add(72 * time.Hour).Unix()
		s.Upstreams = []*controlv1.UpstreamStatus{{Name: "fx", Up: false}, {Name: "q9", Up: true}}
		s.ExportDroppedTotal = map[string]uint64{"logs": 5 * i}
		s.CacheEntries, s.CacheBytes = 10, 1<<20
		return s
	}
	s.QueriesTotal = 500 * i
	s.CacheHitsTotal, s.CacheMissesTotal = 200*i, 300*i
	s.DurationBucketCounts = []uint64{0, 500 * i, 500 * i}
	s.QueriesByTransport = map[string]uint64{"udp": 500 * i}
	s.AnswersByRoute = map[string]uint64{"cache": 300 * i}
	s.FilterIndex = &controlv1.FilterIndexStats{Bytes: 2048, BlockedByCategory: map[string]uint64{"ads-tracking": 30 * i}}
	s.Upstreams = []*controlv1.UpstreamStatus{{Name: "fx", Up: true}, {Name: "q9", Up: true}}
	s.CacheEntries, s.CacheBytes = 20, 2<<20
	return s
}

func TestDashboardAggregations(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	e1 := storetest.InsertEngine(t, st, "edge-1", store.DefaultEngineGroupID)
	e2 := storetest.InsertEngine(t, st, "edge-2", store.DefaultEngineGroupID)
	storetest.ConnectEngine(t, st, e1)

	now := time.Now()
	// 12 samples 10 s apart over the last 2 minutes, 5 s past a 10 s boundary so each pair lands in its own step.
	base := now.Truncate(10 * time.Second).Add(-115 * time.Second)
	for i := uint64(0); i < 12; i++ {
		at := base.Add(time.Duration(i) * 10 * time.Second)
		insertRaw(t, st, "engine_stats", "at", e1, at, sampleAt(i, true, now))
		insertRaw(t, st, "engine_stats", "at", e2, at, sampleAt(i, false, now))
	}
	// Rollup rows 1 h apart over 6 days for edge-1: 3,600 queries per hour is 1 qps.
	hourBase := now.Truncate(time.Hour).Add(-144*time.Hour + 30*time.Minute)
	for i := uint64(0); i <= 144; i++ {
		insertRaw(t, st, "engine_stats_rollup", "bucket", e1, hourBase.Add(time.Duration(i)*time.Hour),
			&controlv1.Stats{QueriesTotal: 3600 * i, CacheHitsTotal: 1800 * i, CacheMissesTotal: 1800 * i})
	}
	// A stale category: enabled, with a source whose last refresh failed.
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

	series, err := stats.DashboardSeries(ctx, st.Pool, "15m", now)
	if err != nil {
		t.Fatal(err)
	}
	if series.StepSeconds != 10 || len(series.Points) != 11 {
		t.Fatalf("15m: step %d, %d points", series.StepSeconds, len(series.Points))
	}
	p := series.Points[len(series.Points)-1]
	// Per 10 s: edge-1 +1000 queries, edge-2 +500, so 150 qps across the fleet.
	checks := map[string][2]float64{
		"qps":                 {p.QPS, 150},
		"udp":                 {p.QPSByTransport["udp"], 130},
		"tcp":                 {p.QPSByTransport["tcp"], 20},
		"nxdomain":            {p.QPSByRcode["NXDOMAIN"], 10},
		"p50 (summed deltas)": {p.P50Ms, 10},  // 750 of 1500 reached in the 10 ms bucket (edge-1 alone: 1 ms)
		"p95":                 {p.P95Ms, 10},  // 1425 <= 1450
		"p99":                 {p.P99Ms, 100}, // 1485 > 1450
		"miss p95":            {p.MissP95Ms, 100},
		"miss p50":            {p.MissP50Ms, 10},
		"cache route":         {p.AnswersByRoute["cache"], 90},
		"dnssec bogus":        {p.DNSSECBogusQPS, 2},
		"dnssec secure":       {p.DNSSECSecureQPS, 4},
		"dnssec insecure":     {p.DNSSECInsecureQPS, 6},
		"ads-tracking":        {p.BlockedByCategory["ads-tracking"], 8},
		"blocked":             {p.BlockedQPS, 6},
		"rewritten":           {p.RewrittenQPS, 1},
		"hit ratio":           {p.CacheHitRatio, 900.0 / 1500},
		"miss ratio":          {p.CacheMissRatio, 600.0 / 1500},
		"stale ratio":         {p.CacheStaleRatio, 30.0 / 1500},
		"recursion upstream":  {p.RecursionUpstreamQPS, 4},
		"recursion timeouts":  {p.RecursionTimeoutsQPS, 0.5},
		"lame marked":         {p.LameMarked, 3},
		"resolution failures": {p.ResolutionFailuresQPS, 0.7},
	}
	for name, c := range checks {
		if !near(c[0], c[1]) {
			t.Errorf("15m %s = %v, want %v", name, c[0], c[1])
		}
	}
	if len(series.Engines) != 2 || series.Engines[0].NodeName != "edge-1" || series.Engines[0].CacheBytes != 1<<20 ||
		series.Engines[1].CacheBytes != 2<<20 || series.Engines[1].FilterIndexBytes != 2048 || series.Engines[0].CacheEntries != 10 {
		t.Errorf("engines: %+v", series.Engines)
	}

	week, err := stats.DashboardSeries(ctx, st.Pool, "7d", now)
	if err != nil {
		t.Fatal(err)
	}
	if week.StepSeconds != 3600 || len(week.Points) <= 100 || !near(week.Points[0].QPS, 1) || !near(week.Points[0].CacheHitRatio, 0.5) {
		t.Fatalf("7d: step %d, %d points, first %+v", week.StepSeconds, len(week.Points), week.Points)
	}
	if _, err := stats.DashboardSeries(ctx, st.Pool, "2d", now); err == nil {
		t.Fatal("unknown range accepted")
	}

	health, err := stats.DashboardHealth(ctx, st.Pool, now)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]stats.Alert{}
	for _, a := range health.Alerts {
		kinds[a.Kind+"/"+a.Subject] = a
	}
	for _, want := range []struct{ key, severity string }{
		{"engine_disconnected/edge-2", "critical"},
		{"category_stale/gambling", "warning"},
		{"upstream_down/fx", "warning"},
		{"certificate_expiring/edge-1", "critical"},
		{"trust_anchor_refresh_failed/edge-1", "warning"},
		{"export_dropped/edge-1", "warning"},
	} {
		a, ok := kinds[want.key]
		if !ok || a.Severity != want.severity || a.Message == "" {
			t.Errorf("alert %s: %+v (present %v); all: %+v", want.key, a, ok, health.Alerts)
		}
	}
	if _, ok := kinds["engine_disconnected/edge-1"]; ok {
		t.Errorf("connected engine alerted as disconnected")
	}
	if _, ok := kinds["upstream_down/q9"]; ok {
		t.Errorf("healthy upstream alerted as down")
	}
	if a := kinds["trust_anchor_refresh_failed/edge-1"]; !strings.Contains(a.Message, "refresh timed out") {
		t.Errorf("trust anchor message: %q", a.Message)
	}
	if len(health.Groups) != 1 || health.Groups[0].Name != "default" || health.Groups[0].Engines != 2 || health.Groups[0].Connected != 1 {
		t.Errorf("groups: %+v", health.Groups)
	}
	// The catalog sync published config version 1, which the connected edge-1 has not applied.
	if len(health.Engines) != 2 || health.Engines[0].Status != "behind" || health.Engines[1].Status != "disconnected" ||
		!near(health.Engines[0].QPS, 100) || health.Engines[0].FilterIndexBytes != 4096 || health.Engines[0].EngineGroupName != "default" {
		t.Errorf("health engines: %+v", health.Engines)
	}

	// Record keeps one rollup row per 5-minute bucket, holding the newest sample.
	for _, q := range []uint64{1, 2} {
		if err := stats.Record(ctx, st, e2.String(), &controlv1.Stats{QueriesTotal: 100000 * q}); err != nil {
			t.Fatal(err)
		}
	}
	var rows int
	var raw []byte
	if err := st.Pool.QueryRow(ctx, "select count(*) over (), stats from engine_stats_rollup where engine_id = $1", e2).Scan(&rows, &raw); err != nil {
		t.Fatal(err)
	}
	var newest controlv1.Stats
	if err := proto.Unmarshal(raw, &newest); err != nil || rows != 1 || newest.QueriesTotal != 200000 {
		t.Fatalf("rollup: %d rows, newest %d, %v", rows, newest.QueriesTotal, err)
	}
}
