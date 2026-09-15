package upstreampred_test

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/aifake"
	"github.com/piwi3910/nexora/mgmt/internal/ai/forecast"
	"github.com/piwi3910/nexora/mgmt/internal/ai/proposal"
	"github.com/piwi3910/nexora/mgmt/internal/ai/upstreampred"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func answer(trend string, rec map[string]any) map[string]any {
	return map[string]any{"trend": trend, "confidence": 0.8, "reasoning": "p99 rises about 10 ms per hour", "recommendation": rec}
}

// insertSamples stores one raw engine sample per hour for the last hours hours, where the upstream's RTT
// is rtt(i) ms for the i-th oldest sample.
func insertSamples(t *testing.T, st *store.Store, engineID uuid.UUID, upstream string, now time.Time, hours int, rtt func(i int) float64) {
	t.Helper()
	for i := 0; i < hours; i++ {
		s := &controlv1.Stats{Upstreams: []*controlv1.UpstreamStatus{{Name: upstream, Up: true, RttUs: uint32(rtt(i) * 1000),
			QueriesTotal: uint64(1000 * (i + 1)), FailuresTotal: uint64(i)}}}
		raw, err := proto.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		at := now.Add(-time.Duration(hours-i)*time.Hour + 30*time.Minute)
		if _, err := st.Pool.Exec(context.Background(), `insert into engine_stats(engine_id, at, stats) values ($1, $2, $3)`, engineID, at, raw); err != nil {
			t.Fatal(err)
		}
	}
}

// TestUpstreamPredictionAgent catches trends taken from the model instead of code, a missing forecast or
// proposal, an unsafe disable of the only enabled upstream stored as a proposal, and a model call for an
// upstream with too few points.
func TestUpstreamPredictionAgent(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	var fxID, fewID uuid.UUID
	if err := st.Pool.QueryRow(ctx, `insert into upstreams(name, protocol, address, timeout_ms, position, enabled)
		values ('fx', 'udp', '192.0.2.53:53', 2000, 0, true) returning id`).Scan(&fxID); err != nil {
		t.Fatal(err)
	}
	// "few" is scoped to another engine group, so fx stays the only enabled upstream of its scope.
	var edge uuid.UUID
	if err := st.Pool.QueryRow(ctx, `insert into engine_groups(name) values ('edge') returning id`).Scan(&edge); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool.QueryRow(ctx, `insert into upstreams(name, protocol, address, timeout_ms, position, enabled, engine_group_id)
		values ('few', 'udp', '192.0.2.54:53', 250, 0, true, $1) returning id`, edge).Scan(&fewID); err != nil {
		t.Fatal(err)
	}
	engine := storetest.InsertEngine(t, st, "node-a", uuid.MustParse("00000000-0000-0000-0000-000000000001"))
	insertSamples(t, st, engine, "fx", now, 24, func(i int) float64 { return 100 + 10*float64(i) })
	other := storetest.InsertEngine(t, st, "node-b", edge)
	insertSamples(t, st, other, "few", now, 5, func(int) float64 { return 20 })

	model := aifake.Model(
		aifake.JSON(answer("degrading", map[string]any{"type": "switch_strategy", "description": "race the upstreams", "strategy": "fastest"})),
		aifake.JSON(answer("failing", map[string]any{"type": "disable", "description": "stop using fx"})),
		aifake.JSON(answer("degrading", map[string]any{"type": "none", "description": "keep watching"})),
	)
	svc := aifake.Service(t, st, model, nil)
	agent := &upstreampred.Agent{Store: st, Service: svc, Validator: &proposal.Validator{Store: st}, Interval: 6 * time.Hour,
		Now: func() time.Time { return now }}
	if agent.Name() != "upstream_prediction" {
		t.Fatalf("name %q", agent.Name())
	}

	run := &ai.Run{Agent: agent.Name(), Outcome: "ok", Detail: map[string]any{}}
	if err := agent.Run(ctx, run); err != nil {
		t.Fatal(err)
	}
	if n := len(model.RecordedCalls()); n != 1 {
		t.Fatalf("model calls after the first run: %d, want 1 (none for the upstream with 5 points)", n)
	}
	if prompt := model.RecordedCalls()[0].Messages; !strings.Contains(asText(t, prompt), `slope_ms_per_hour\": 10,`) {
		t.Fatalf("prompt carries no code-computed features: %s", asText(t, prompt))
	}

	type detail struct {
		UpstreamName      string                `json:"upstream_name"`
		Trend             string                `json:"trend"`
		Slope             float64               `json:"slope_ms_per_hour"`
		CurrentP99        float64               `json:"current_rtt_p99_ms"`
		DataPoints        int                   `json:"data_points_analyzed"`
		PeriodicHours     []int                 `json:"periodic_hours"`
		ProjectedCrossing *string               `json:"projected_time_to_threshold"`
		Recommendation    struct{ Type string } `json:"recommendation"`
	}
	latest := func() map[string]forecast.Forecast {
		t.Helper()
		fs, err := forecast.Latest(ctx, st.Pool, "upstream")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]forecast.Forecast{}
		for _, f := range fs {
			out[f.Subject] = f
		}
		return out
	}
	decode := func(f forecast.Forecast) detail {
		t.Helper()
		var d detail
		if err := json.Unmarshal(f.Detail, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}

	fs := latest()
	fx, ok := fs[fxID.String()]
	if !ok || len(fs) != 2 {
		t.Fatalf("forecasts: %+v", fs)
	}
	d := decode(fx)
	if d.Trend != "degrading" || d.UpstreamName != "fx" || math.Abs(d.Slope-10) > 0.5 || d.CurrentP99 != 330 || d.DataPoints != 24 ||
		d.ProjectedCrossing == nil || d.Recommendation.Type != "switch_strategy" {
		t.Fatalf("fx forecast detail: %s", fx.Detail)
	}
	if !fx.GeneratedAt.Equal(now) || !fx.ValidUntil.Equal(now.Add(6*time.Hour)) || fx.ProposalID == nil {
		t.Fatalf("fx forecast: %+v", fx)
	}
	few := fs[fewID.String()]
	if fd := decode(few); fd.Trend != "insufficient_data" || fd.DataPoints != 5 || few.ProposalID != nil || fd.Recommendation.Type != "none" {
		t.Fatalf("few forecast: %s (proposal %v)", few.Detail, few.ProposalID)
	}

	props, err := proposal.List(ctx, st, proposal.Filter{Source: "upstream_prediction"})
	if err != nil || len(props) != 1 || props[0].ID != *fx.ProposalID {
		t.Fatalf("proposals: %+v %v", props, err)
	}
	if a := props[0].Actions; len(a) != 1 || a[0].OperationID != "updateResolverSettings" {
		t.Fatalf("actions: %+v", a)
	}
	var body map[string]any
	if err := json.Unmarshal(props[0].Actions[0].Body, &body); err != nil || body["strategy"] != "fastest" || body["revision"] != float64(1) ||
		body["cache_max_bytes"] != float64(268435456) {
		t.Fatalf("switch_strategy body: %s", props[0].Actions[0].Body)
	}

	// Second run: disable for the only enabled upstream of its scope is rejected and re-asked; the
	// corrected answer "none" stores a forecast without a proposal.
	later := now.Add(time.Hour)
	agent.Now = func() time.Time { return later }
	if err := agent.Run(ctx, &ai.Run{Agent: agent.Name(), Outcome: "ok", Detail: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	calls := model.RecordedCalls()
	if len(calls) != 3 {
		t.Fatalf("model calls: %d, want 3 (disable re-asked)", len(calls))
	}
	if retry := asText(t, calls[2].Messages); !strings.Contains(retry, "enabled upstream") {
		t.Fatalf("re-ask does not name the disable rejection: %s", retry)
	}
	fx = latest()[fxID.String()]
	if d := decode(fx); !fx.GeneratedAt.Equal(later) || fx.ProposalID != nil || d.Recommendation.Type != "none" {
		t.Fatalf("second fx forecast: %+v %s", fx, fx.Detail)
	}
	if props, _ := proposal.List(ctx, st, proposal.Filter{Source: "upstream_prediction"}); len(props) != 1 {
		t.Fatalf("proposals after the rejected disable: %d, want 1", len(props))
	}
	var enabled bool
	if err := st.Pool.QueryRow(ctx, `select enabled from upstreams where id = $1`, fxID).Scan(&enabled); err != nil || !enabled {
		t.Fatalf("fx enabled %v %v", enabled, err)
	}
}

func asText(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
