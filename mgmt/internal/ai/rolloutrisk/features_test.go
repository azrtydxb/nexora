package rolloutrisk_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/ai/rolloutrisk"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

var actor = auth.Actor{Type: "user", ID: "u1", Name: "operator"}

// pastRollout inserts a config version, the default group's snapshot for it, one audit row naming
// action on targetType, and a terminal rollout in that state.
type rolloutSpec struct {
	version              int64
	action, targetType   string
	kind                 string // "" means "change"
	state, strategy      string
	haltReason           string
	canaries             []uuid.UUID
	phaseStart, finished *time.Time
	createdAt            *time.Time
}

func insertRollout(t *testing.T, st *store.Store, s rolloutSpec) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	err := st.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into config_versions(version, created_by, snapshot) values ($1, 'test', '\x00')`,
			s.version); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into group_snapshots(version, engine_group_id, snapshot, content_sha256)
			values ($1, $2, '\x00', md5(random()::text) || md5(random()::text))`, s.version, store.DefaultEngineGroupID); err != nil {
			return err
		}
		v := uint64(s.version)
		if err := auth.WriteAudit(ctx, tx, actor, auth.Change{Action: s.action, TargetType: s.targetType,
			TargetID: "t" + s.targetType}, &v); err != nil {
			return err
		}
		canaries := s.canaries
		if canaries == nil {
			canaries = []uuid.UUID{}
		}
		kind := s.kind
		if kind == "" {
			kind = "change"
		}
		return tx.QueryRow(ctx, `insert into rollouts(engine_group_id, version, kind, strategy, state, params,
			canary_engine_ids, phase_started_at, finished_at, created_by, created_at)
			values ($1, $2, $3, $4, $5, '{"strategy":"canary","canary_count":1,"min_health_queries":100,"max_servfail_ratio":0.05}'::jsonb,
			$6, $7, $8, 'test', coalesce($9, now())) returning id`,
			store.DefaultEngineGroupID, s.version, kind, s.strategy, s.state, canaries, s.phaseStart, s.finished, s.createdAt).Scan(&id)
	})
	if err != nil {
		t.Fatalf("insert rollout %d: %v", s.version, err)
	}
	if s.haltReason != "" {
		if _, err := st.Pool.Exec(ctx, `update rollouts set halt_reason = $2 where id = $1`, id, s.haltReason); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func stats(t *testing.T, st *store.Store, engine uuid.UUID, at time.Time, queries, servfail uint64) {
	t.Helper()
	raw, err := proto.Marshal(&controlv1.Stats{QueriesTotal: queries, ServfailTotal: servfail})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(context.Background(), `insert into engine_stats(engine_id, at, stats) values ($1, $2, $3)`,
		engine, at, raw); err != nil {
		t.Fatal(err)
	}
}

// TestRolloutRiskFeatures catches a Jaccard that is not the set ratio, a high-risk target type that
// does not raise HighRisk, history that is not ordered by similarity then by the newer version, a
// halted canary that is not reported as rejected, and SERVFAIL ratios computed without the restart
// baseline.
func TestRolloutRiskFeatures(t *testing.T) {
	ctx := context.Background()
	policy := []rolloutrisk.Change{{TargetType: "policy_group", Action: "updatePolicyGroup"}}
	both := []rolloutrisk.Change{{TargetType: "policy_group", Action: "updatePolicyGroup"},
		{TargetType: "resolver_settings", Action: "updateResolverSettings"}}
	if got := rolloutrisk.Jaccard(both, policy); got != 0.5 {
		t.Fatalf("Jaccard(2, 1 shared) = %v, want 0.5", got)
	}
	if got := rolloutrisk.Jaccard(nil, policy); got != 0 {
		t.Fatalf("Jaccard(empty) = %v, want 0", got)
	}

	st := storetest.New(t)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	a := storetest.InsertEngine(t, st, "rr-a", store.DefaultEngineGroupID)
	b := storetest.InsertEngine(t, st, "rr-b", store.DefaultEngineGroupID)
	storetest.InsertEngine(t, st, "rr-c", store.DefaultEngineGroupID)

	insertRollout(t, st, rolloutSpec{version: 1, action: "updatePolicyGroup", targetType: "policy_group",
		state: "completed", strategy: "canary"})
	insertRollout(t, st, rolloutSpec{version: 2, action: "updateUpstream", targetType: "upstream",
		state: "completed", strategy: "all_at_once"})
	phase := now.Add(-2 * time.Hour)
	finished := now.Add(-110 * time.Minute)
	insertRollout(t, st, rolloutSpec{version: 3, action: "updatePolicyGroup", targetType: "policy_group",
		state: "halted", strategy: "canary", haltReason: "engine rr-a servfail ratio 0.100 > 0.050",
		canaries: []uuid.UUID{a}, phaseStart: &phase, finished: &finished})
	newID := insertRollout(t, st, rolloutSpec{version: 4, action: "updatePolicyGroup", targetType: "policy_group",
		state: "pending", strategy: "canary"})

	// The halted rollout's canary: 1,000 queries and 100 SERVFAILs inside its canary window.
	stats(t, st, a, phase.Add(time.Second), 100, 5)
	stats(t, st, a, finished.Add(-time.Second), 1100, 105)
	// The fleet over the last 15 minutes: engine b restarts, so its counters start again from zero.
	stats(t, st, b, now.Add(-14*time.Minute), 900, 80)
	stats(t, st, b, now.Add(-10*time.Minute), 0, 0)
	stats(t, st, b, now.Add(-time.Minute), 1000, 50)

	f, err := rolloutrisk.Collect(ctx, st.Pool, newID, now)
	if err != nil {
		t.Fatal(err)
	}
	if f.RolloutID != newID || f.Version != 4 || f.EngineGroupID != store.DefaultEngineGroupID {
		t.Fatalf("features identity = %+v", f)
	}
	if len(f.Changes) != 1 || f.Changes[0].TargetType != "policy_group" || f.ChangedResources != 1 || !f.HighRisk {
		t.Fatalf("changes = %+v high_risk=%v", f.Changes, f.HighRisk)
	}
	if f.Engines != 3 || f.Disconnected != 3 {
		t.Fatalf("engines = %d disconnected = %d, want 3 and 3", f.Engines, f.Disconnected)
	}
	if f.FleetServfailRatio != 0.05 {
		t.Fatalf("fleet servfail ratio = %v, want 0.05 (50/1000 after the restart)", f.FleetServfailRatio)
	}
	if len(f.Params) == 0 || string(f.Params) == "null" {
		t.Fatalf("params = %s", f.Params)
	}
	if len(f.Similar) != 3 {
		t.Fatalf("similar = %+v, want the three terminal rollouts", f.Similar)
	}
	h := f.Similar[0]
	if h.Version != 3 || h.Outcome != "halted" || h.Similarity != 1 || !h.CanaryRejected || h.HaltReason == "" {
		t.Fatalf("similar[0] = %+v, want the halted policy-group rollout 3", h)
	}
	if h.Description != "updatePolicyGroup" {
		t.Fatalf("similar[0].Description = %q", h.Description)
	}
	if h.MaxServfailRatio != 0.1 {
		t.Fatalf("similar[0].MaxServfailRatio = %v, want 0.1", h.MaxServfailRatio)
	}
	if f.Similar[1].Version != 1 || f.Similar[1].Similarity != 1 || f.Similar[1].CanaryRejected {
		t.Fatalf("similar[1] = %+v, want the completed policy-group rollout 1", f.Similar[1])
	}
	if f.Similar[2].Version != 2 || f.Similar[2].Similarity != 0 {
		t.Fatalf("similar[2] = %+v, want the unrelated upstream rollout 2", f.Similar[2])
	}
}
