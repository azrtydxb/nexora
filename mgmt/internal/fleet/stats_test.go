package fleet_test

import (
	"context"
	"math"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/fleet"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func TestSeriesDerivesRatesAndSkipsCounterResets(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	id := storetest.InsertEngine(t, st, "stats", store.DefaultEngineGroupID)
	bounds := []uint64{1000, 5000, 20000}
	samples := []*controlv1.Stats{
		{QueriesTotal: 1000, CacheHitsTotal: 100, CacheMissesTotal: 100, ServfailTotal: 0, DurationBucketBoundsUs: bounds, DurationBucketCounts: []uint64{900, 990, 1000}},
		// +1000 queries in 10 s: 750 hits of 1000 lookups, 50 SERVFAIL, 99% of durations within 5 ms.
		{QueriesTotal: 2000, CacheHitsTotal: 850, CacheMissesTotal: 350, ServfailTotal: 50, DurationBucketBoundsUs: bounds, DurationBucketCounts: []uint64{1500, 1980, 2000}},
		// Engine restart: counters went down, the pair is skipped.
		{QueriesTotal: 10, DurationBucketBoundsUs: bounds, DurationBucketCounts: []uint64{10, 10, 10}},
	}
	start := time.Now().Add(-time.Minute)
	for i, s := range samples {
		raw, _ := proto.Marshal(s)
		if _, err := st.Pool.Exec(ctx, "insert into engine_stats (engine_id, at, stats) values ($1, $2, $3)", id, start.Add(time.Duration(i)*10*time.Second), raw); err != nil {
			t.Fatal(err)
		}
	}
	points, err := fleet.Series(ctx, st.Pool, id, 5*time.Minute)
	if err != nil || len(points) != 1 {
		t.Fatalf("points %+v err %v, want one", points, err)
	}
	p := points[0]
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	if !near(p.QPS, 100) || !near(p.CacheHitRatio, 0.75) || !near(p.ServfailRatio, 0.05) || !near(p.P99Ms, 5) {
		t.Fatalf("point %+v, want qps 100, hit ratio 0.75, servfail 0.05, p99 5 ms", p)
	}
	if points, err := fleet.Series(ctx, st.Pool, id, 5*time.Second); err != nil || len(points) != 0 {
		t.Fatalf("window excludes old samples: %+v %v", points, err)
	}
}
