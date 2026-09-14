package fleet_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/fleet"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func insertSample(t *testing.T, st *store.Store, id uuid.UUID, at time.Time, s *controlv1.Stats) {
	t.Helper()
	raw, err := proto.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(context.Background(), "insert into engine_stats (engine_id, at, stats) values ($1, $2, $3)", id, at, raw); err != nil {
		t.Fatal(err)
	}
}

func TestEngineMetricsSeries(t *testing.T) {
	st := storetest.New(t)
	engineID := storetest.InsertEngine(t, st, "metrics", store.DefaultEngineGroupID)
	bounds := []uint64{1000, 10000, 100000}
	at := time.Now().Add(-4 * time.Minute)
	sample := func(i int, queries, servfail, nx, refused, blocked uint64, started int64, cpu float64) {
		s := &controlv1.Stats{
			UnixMs: at.Add(time.Duration(i) * 10 * time.Second).UnixMilli(), QueriesTotal: queries, ServfailTotal: servfail,
			FilterBlockedTotal: blocked, DurationBucketBoundsUs: bounds, DurationBucketCounts: []uint64{queries / 2, queries * 99 / 100, queries},
			QueriesByRcode: map[string]uint64{"NXDOMAIN": nx, "REFUSED": refused, "SERVFAIL": servfail}, CacheHitsTotal: queries / 2, CacheMissesTotal: queries / 2,
			StartedUnixMs: started, ProcessCpuSecondsTotal: cpu, ProcessResidentBytes: 100 << 20, MemoryLimitBytes: 1 << 30,
			OpenConnections: map[string]uint64{"dot": 3},
			Upstreams:       []*controlv1.UpstreamStatus{{Name: "fx", RttUs: 2500, FailuresTotal: uint64(i), RaceWinsTotal: uint64(2 * i)}},
		}
		insertSample(t, st, engineID, at.Add(time.Duration(i)*10*time.Second), s)
	}
	sample(0, 1000, 0, 0, 0, 0, 1, 1.0)
	sample(1, 2000, 10, 100, 50, 200, 1, 3.0)
	sample(2, 50, 0, 0, 0, 0, 2, 0.1) // restart: counters went down, start time changed
	sample(3, 1050, 10, 0, 0, 100, 2, 1.1)
	d, err := fleet.EngineMetrics(context.Background(), st.Pool, engineID, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Points) != 2 || d.Restarts != 1 {
		t.Fatalf("points %d restarts %d", len(d.Points), d.Restarts)
	}
	p := d.Points[0]
	if p.QPS != 100 || p.ServfailRatio != 0.01 || p.NXDomainRatio != 0.1 || p.RefusedRatio != 0.05 || p.BlockedQPS != 20 || p.CPUCores != 0.2 {
		t.Fatalf("rates: %+v", p)
	}
	if p.P99Ms != 10 || p.P50Ms != 1 || p.Connections["dot"] != 3 || p.MemoryLimitBytes != 1<<30 {
		t.Fatalf("latency/resources: %+v", p)
	}
	if u := d.Upstreams["fx"]; len(u) != 2 || u[0].RTTMs != 2.5 || u[0].FailuresPerSecond != 0.1 || u[0].RaceWinsPerSecond != 0.2 {
		t.Fatalf("upstreams: %+v", d.Upstreams)
	}
	if d.StartedAt == nil || d.StartedAt.UnixMilli() != 2 {
		t.Fatalf("started: %v", d.StartedAt)
	}
}
