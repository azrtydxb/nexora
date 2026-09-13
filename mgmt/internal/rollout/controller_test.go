package rollout_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/rollout"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func stateOf(t *testing.T, st *store.Store, version uint64) (string, []uuid.UUID) {
	t.Helper()
	var s string
	var canaries []uuid.UUID
	if err := st.Pool.QueryRow(context.Background(), `select state, canary_engine_ids from rollouts
		where engine_group_id = $1 and version = $2`, store.DefaultEngineGroupID, int64(version)).Scan(&s, &canaries); err != nil {
		t.Fatal(err)
	}
	return s, canaries
}

func sample(t *testing.T, st *store.Store, engine uuid.UUID, at string, queries, servfail uint64) {
	t.Helper()
	raw, _ := proto.Marshal(&controlv1.Stats{QueriesTotal: queries, ServfailTotal: servfail})
	if _, err := st.Pool.Exec(context.Background(), `insert into engine_stats (engine_id, at, stats)
		select $1, phase_started_at + $2::interval, $3 from rollouts where state = 'verifying'`, engine, at, raw); err != nil {
		t.Fatal(err)
	}
}

func TestControllerDrivesCanaryUnderAdvisoryLock(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	if _, err := snapshot.EnsureInitial(ctx, st, snapshot.BuildConfig{}); err != nil {
		t.Fatal(err)
	}
	a := storetest.InsertEngine(t, st, "a", store.DefaultEngineGroupID)
	b := storetest.InsertEngine(t, st, "b", store.DefaultEngineGroupID)
	storetest.ConnectEngine(t, st, a)
	storetest.ConnectEngine(t, st, b)
	c := &rollout.Controller{Store: st}

	if n, err := c.Step(ctx); err != nil || n != 0 {
		t.Fatalf("version 1 completed before any ack: driven=%d err=%v", n, err)
	}
	if _, err := st.Pool.Exec(ctx, `update engines set applied_version = 1`); err != nil {
		t.Fatal(err)
	}
	if n, err := c.Step(ctx); err != nil || n != 1 {
		t.Fatalf("rolling -> completed: driven=%d err=%v", n, err)
	}

	if _, err := st.Pool.Exec(ctx, `update engine_groups set rollout_strategy = 'canary', canary_count = 1, min_health_queries = 10`); err != nil {
		t.Fatal(err)
	}
	v2, err := snapshot.Mutate(ctx, st, snapshot.BuildConfig{}, auth.Actor{Type: "system", ID: "t", Name: "t"}, func(tx pgx.Tx) (auth.Change, error) {
		_, err := tx.Exec(ctx, `update resolver_settings set block_ttl = 7`)
		return auth.Change{Action: "updateResolverSettings", TargetType: "resolver_settings", TargetID: "singleton"}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	if err := st.Pool.QueryRow(ctx, `select id from rollouts where version = $1`, int64(v2)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	holder, err := st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, `select pg_advisory_xact_lock(hashtext('nexora:rollout:' || $1::text))`, id); err != nil {
		t.Fatal(err)
	}
	if n, err := c.Step(ctx); err != nil || n != 0 {
		t.Fatalf("locked rollout: driven=%d err=%v, want 0 nil", n, err)
	}
	_ = holder.Rollback(ctx)

	if n, err := c.Step(ctx); err != nil || n != 1 {
		t.Fatalf("pending -> canary: driven=%d err=%v", n, err)
	}
	if s, canaries := stateOf(t, st, v2); s != "canary" || len(canaries) != 1 || canaries[0] != a {
		t.Fatalf("state %s canaries %v, want canary [a]", s, canaries)
	}
	if _, err := st.Pool.Exec(ctx, `update engines set applied_version = $1 where id = $2`, int64(v2), a); err != nil {
		t.Fatal(err)
	}
	if n, err := c.Step(ctx); err != nil || n != 1 {
		t.Fatalf("canary -> verifying: driven=%d err=%v", n, err)
	}
	if s, _ := stateOf(t, st, v2); s != "verifying" {
		t.Fatalf("state %s, want verifying", s)
	}
	if _, err := st.Pool.Exec(ctx, `update rollouts set phase_started_at = now() - interval '31 seconds' where id = $1`, id); err != nil {
		t.Fatal(err)
	}
	sample(t, st, a, "-5 seconds", 1000, 3)
	sample(t, st, a, "10 seconds", 1400, 5)
	sample(t, st, a, "20 seconds", 1900, 8)
	var since time.Time
	_ = st.Pool.QueryRow(ctx, `select phase_started_at from rollouts where id = $1`, id).Scan(&since)
	if h, err := rollout.HealthSince(ctx, st.Pool, a, since); err != nil || h.Samples != 3 || h.Queries != 900 || h.Servfail != 5 {
		t.Fatalf("health = %+v err %v, want 3 samples, 900 queries, 5 servfail", h, err)
	}
	if n, err := c.Step(ctx); err != nil || n != 1 {
		t.Fatalf("verifying -> rolling: driven=%d err=%v", n, err)
	}
	if _, err := st.Pool.Exec(ctx, `update engines set applied_version = $1 where id = $2`, int64(v2), b); err != nil {
		t.Fatal(err)
	}
	if n, err := c.Step(ctx); err != nil || n != 1 {
		t.Fatalf("rolling -> completed: driven=%d err=%v", n, err)
	}
	var stable int64
	if err := st.Pool.QueryRow(ctx, `select stable_version from engine_groups where id = $1`, store.DefaultEngineGroupID).Scan(&stable); err != nil || uint64(stable) != v2 {
		t.Fatalf("stable_version = %d err %v, want %d", stable, err, v2)
	}
	if n, _ := c.Step(ctx); n != 0 {
		t.Fatalf("terminal rollout driven again (%d)", n)
	}
}
