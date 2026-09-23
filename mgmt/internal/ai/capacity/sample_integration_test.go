package capacity_test

import (
	"context"
	"testing"
	"time"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/ai/capacity"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func TestSampleTodayRecursorCache(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	a := storetest.InsertEngine(t, st, "a", store.DefaultEngineGroupID)
	b := storetest.InsertEngine(t, st, "b", store.DefaultEngineGroupID)
	deleted := storetest.InsertEngine(t, st, "deleted", store.DefaultEngineGroupID)
	if _, err := st.Pool.Exec(ctx, `update engines set deleted_at = now() where id = $1`, deleted); err != nil {
		t.Fatal(err)
	}
	report := func(bytes, limit uint64) *controlv1.Stats {
		return &controlv1.Stats{RecursorCache: &controlv1.RecursorCacheStats{Enabled: true, Bytes: bytes, MaxBytes: limit}}
	}
	assertSample := func(present bool, value, limit float64) {
		t.Helper()
		if err := capacity.SampleToday(ctx, st, now); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := st.Pool.QueryRow(ctx, `select count(*) from ai_capacity_samples where day = $1 and resource = 'recursor_cache'`, now.Format(time.DateOnly)).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if !present {
			if count != 0 {
				t.Fatal("invented recursor measurement")
			}
			return
		}
		var v, l float64
		if count != 1 {
			t.Fatalf("sample count = %d", count)
		}
		if err := st.Pool.QueryRow(ctx, `select value, limit_value from ai_capacity_samples where day = $1 and resource = 'recursor_cache'`, now.Format(time.DateOnly)).Scan(&v, &l); err != nil {
			t.Fatal(err)
		}
		if v != value || l != limit {
			t.Fatalf("sample = %v/%v, want %v/%v", v, l, value, limit)
		}
	}
	// Yesterday, future and deleted-engine measurements cannot create today's sample.
	sample(t, st, "engine_stats", "at", a, now.Add(-24*time.Hour), report(900, 4<<20))
	sample(t, st, "engine_stats", "at", a, now.Add(time.Hour), report(800, 4<<20))
	sample(t, st, "engine_stats", "at", deleted, now.Add(-time.Minute), report(700, 4<<20))
	assertSample(false, 0, 0)
	sample(t, st, "engine_stats", "at", a, now.Add(-5*time.Hour), &controlv1.Stats{})
	sample(t, st, "engine_stats", "at", b, now.Add(-5*time.Hour), &controlv1.Stats{RecursorCache: &controlv1.RecursorCacheStats{Bytes: 999, MaxBytes: 4 << 20}})
	assertSample(false, 0, 0)
	// The newest sample supersedes an older measurement even if it lacks telemetry.
	sample(t, st, "engine_stats", "at", a, now.Add(-4*time.Hour), report(600, 4<<20))
	sample(t, st, "engine_stats", "at", a, now.Add(-3*time.Hour), &controlv1.Stats{})
	sample(t, st, "engine_stats", "at", b, now.Add(-3*time.Hour), report(500, 0))
	assertSample(false, 0, 0)
	// Zero bytes with a measured budget is a valid point, not missing data.
	sample(t, st, "engine_stats", "at", a, now.Add(-2*time.Hour), report(0, 32<<20))
	assertSample(true, 0, 32<<20)
	sample(t, st, "engine_stats", "at", a, now.Add(-time.Hour), report(200, 128<<20))
	sample(t, st, "engine_stats", "at", b, now.Add(-time.Hour), report(100, 4<<20))
	assertSample(true, 200, 128<<20)
	assertSample(true, 200, 128<<20) // idempotent daily upsert
	// Newest missing/disabled/corrupt reports must not resurrect older measurements,
	// including a sample written by an earlier run today. Prior days are retained.
	history(t, st, now, "recursor_cache", 1, func(int) float64 { return 42 }, 64<<20)
	sample(t, st, "engine_stats", "at", a, now.Add(-30*time.Minute), &controlv1.Stats{})
	sample(t, st, "engine_stats", "at", b, now.Add(-30*time.Minute), &controlv1.Stats{RecursorCache: &controlv1.RecursorCacheStats{Bytes: 999, MaxBytes: 4 << 20}})
	assertSample(false, 0, 0)
	sample(t, st, "engine_stats", "at", a, now.Add(-20*time.Minute), report(250, 16<<20))
	assertSample(true, 250, 16<<20)
	if _, err := st.Pool.Exec(ctx, `insert into engine_stats(engine_id, at, stats) values ($1, $2, $3)`, a, now.Add(-10*time.Minute), []byte{0xff}); err != nil {
		t.Fatal(err)
	}
	assertSample(false, 0, 0)
	var previous float64
	if err := st.Pool.QueryRow(ctx, `select value from ai_capacity_samples where day = $1 and resource = 'recursor_cache'`, now.AddDate(0, 0, -1).Format(time.DateOnly)).Scan(&previous); err != nil || previous != 42 {
		t.Fatalf("previous day = %v, err = %v", previous, err)
	}
}
