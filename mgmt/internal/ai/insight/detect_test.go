package insight_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/ai/finding"
	"github.com/piwi3910/nexora/mgmt/internal/ai/insight"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

// engineSeries describes one engine's samples: a steady 100 qps with baseServfail over the day and
// recentServfail in the last 15 minutes, and one upstream whose RTT moves from baseRTTMs to recentRTTMs.
type engineSeries struct {
	node                         string
	baseServfail, recentServfail float64
	upstream                     string
	baseRTTMs, recentRTTMs       uint32
}

// seedEngine inserts cumulative engine_stats samples every 5 min from 24 h ago until 15 min ago, then
// every 10 s until now.
func seedEngine(t *testing.T, st *store.Store, id uuid.UUID, s engineSeries, now time.Time) {
	t.Helper()
	boundary := now.Add(-15 * time.Minute)
	var queries, servfail, fast, mid uint64
	started := now.Add(-25 * time.Hour).UnixMilli()
	var prevAt time.Time
	for at := now.Add(-24*time.Hour + time.Minute); at.Before(now); {
		ratio, rtt := s.baseServfail, s.baseRTTMs
		if at.After(boundary) {
			ratio, rtt = s.recentServfail, s.recentRTTMs
		}
		if !prevAt.IsZero() {
			n := uint64(at.Sub(prevAt).Seconds() * 100)
			queries += n
			servfail += uint64(float64(n) * ratio)
			fast += n / 2
			mid += n * 99 / 100
		}
		stats := &controlv1.Stats{
			StartedUnixMs: started, QueriesTotal: queries,
			QueriesByRcode:         map[string]uint64{"NOERROR": queries - servfail, "SERVFAIL": servfail},
			DurationBucketBoundsUs: []uint64{1000, 10000, 100000},
			DurationBucketCounts:   []uint64{fast, mid, queries},
			Upstreams:              []*controlv1.UpstreamStatus{{Name: s.upstream, Up: true, RttUs: rtt * 1000}},
		}
		raw, err := proto.Marshal(stats)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Pool.Exec(context.Background(), "insert into engine_stats (engine_id, at, stats) values ($1, $2, $3)", id, at, raw); err != nil {
			t.Fatal(err)
		}
		prevAt = at
		if at.Before(boundary) {
			at = at.Add(5 * time.Minute)
			if at.After(boundary) {
				at = boundary.Add(5 * time.Second)
			}
		} else {
			at = at.Add(10 * time.Second)
		}
	}
}

// seedSpike creates engines edge-a (SERVFAIL 0.1% then 15%, upstream fx 20 ms then 300 ms) and edge-b
// (steady, upstream fy at 20 ms), both connected.
func seedSpike(t *testing.T, st *store.Store, now time.Time) {
	t.Helper()
	a := storetest.InsertEngine(t, st, "edge-a", store.DefaultEngineGroupID)
	b := storetest.InsertEngine(t, st, "edge-b", store.DefaultEngineGroupID)
	storetest.ConnectEngine(t, st, a)
	storetest.ConnectEngine(t, st, b)
	seedEngine(t, st, a, engineSeries{"edge-a", 0.001, 0.15, "fx", 20, 300}, now)
	seedEngine(t, st, b, engineSeries{"edge-b", 0.001, 0.001, "fy", 20, 20}, now)
}

func byID(cands []finding.Candidate, id string) *finding.Candidate {
	i := slices.IndexFunc(cands, func(c finding.Candidate) bool { return c.ID == id })
	if i < 0 {
		return nil
	}
	return &cands[i]
}

func TestInsightDetectors(t *testing.T) {
	st := storetest.New(t)
	now := time.Now()
	seedSpike(t, st, now)

	cands, err := insight.Detect(context.Background(), st.Pool, now)
	if err != nil {
		t.Fatal(err)
	}
	if c := byID(cands, "servfail_spike:edge-a"); c == nil || c.Severity != "critical" || c.Kind != "insight" || c.Type != "servfail_spike" {
		t.Fatalf("servfail_spike:edge-a missing or not critical: %+v", cands)
	}
	if c := byID(cands, "upstream_degraded:fx"); c == nil || c.Severity != "warning" {
		t.Fatalf("upstream_degraded:fx missing: %+v", cands)
	}
	for _, c := range cands {
		switch c.ID {
		case "servfail_spike:edge-a", "upstream_degraded:fx":
		default:
			t.Errorf("unexpected candidate %s (%s): %s", c.ID, c.Severity, c.Description)
		}
	}
}

func TestInsightScore(t *testing.T) {
	open := func(sev ...string) []finding.Finding {
		var out []finding.Finding
		for _, s := range sev {
			out = append(out, finding.Finding{Kind: "insight", Status: "open", Severity: s})
		}
		return out
	}
	if got := insight.Score(open()); got != 10 {
		t.Fatalf("no insights = %d, want 10", got)
	}
	if got := insight.Score(open("critical", "warning")); got != 6 {
		t.Fatalf("one critical and one warning = %d, want 6", got)
	}
	if got := insight.Score(open("critical", "critical", "critical", "critical", "critical")); got != 0 {
		t.Fatalf("five criticals = %d, want 0", got)
	}
	if got := insight.Score(open("info")); got != 10 {
		t.Fatalf("info = %d, want 10", got)
	}
}
