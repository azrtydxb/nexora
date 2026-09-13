package snapshot_test

import (
	"context"
	"errors"
	"slices"
	"testing"

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

var testActor = auth.Actor{Type: "system", ID: "test", Name: "test"}

func mutateSQL(t *testing.T, st *store.Store, sql string, args ...any) uint64 {
	t.Helper()
	v, err := snapshot.Mutate(context.Background(), st, snapshot.BuildConfig{}, testActor, func(tx pgx.Tx) (auth.Change, error) {
		_, err := tx.Exec(context.Background(), sql, args...)
		return auth.Change{Action: "test", TargetType: "test", TargetID: "1"}, err
	})
	if err != nil {
		t.Fatalf("mutate %q: %v", sql, err)
	}
	return v
}

func groupSnapshot(t *testing.T, st *store.Store, version uint64, group uuid.UUID) *controlv1.ConfigSnapshot {
	t.Helper()
	var raw []byte
	if err := st.Pool.QueryRow(context.Background(), `select snapshot from group_snapshots where version = $1 and engine_group_id = $2`,
		int64(version), group).Scan(&raw); err != nil {
		t.Fatalf("group snapshot (%d, %s): %v", version, group, err)
	}
	s := &controlv1.ConfigSnapshot{}
	if err := proto.Unmarshal(raw, s); err != nil {
		t.Fatal(err)
	}
	return s
}

func rolloutState(t *testing.T, st *store.Store, group uuid.UUID, version uint64) string {
	t.Helper()
	var s string
	if err := st.Pool.QueryRow(context.Background(), `select state from rollouts where engine_group_id = $1 and version = $2`,
		group, int64(version)).Scan(&s); err != nil {
		t.Fatalf("rollout (%s, %d): %v", group, version, err)
	}
	return s
}

func upstreamNames(s *controlv1.ConfigSnapshot) []string {
	out := []string{}
	for _, u := range s.Upstreams {
		out = append(out, u.Name)
	}
	return out
}

func TestPublishPerEngineGroup(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	if _, err := snapshot.EnsureInitial(ctx, st, snapshot.BuildConfig{}); err != nil {
		t.Fatal(err)
	}
	var edge uuid.UUID
	if err := st.Pool.QueryRow(ctx, `insert into engine_groups (name, rollout_strategy, canary_count, upstream_mode)
		values ('edge', 'canary', 1, 'inherit') returning id`).Scan(&edge); err != nil {
		t.Fatal(err)
	}

	v1 := mutateSQL(t, st, `insert into upstreams (name, protocol, address, position) values ('global', 'udp', '192.0.2.1:53', 0)`)
	v2 := mutateSQL(t, st, `insert into upstreams (name, protocol, address, position, engine_group_id)
		values ('edge-only', 'udp', '192.0.2.2:53', 1, $1)`, edge)

	if got := upstreamNames(groupSnapshot(t, st, v2, store.DefaultEngineGroupID)); !slices.Equal(got, []string{"global"}) {
		t.Fatalf("default group upstreams = %v, want [global]", got)
	}
	edgeSnap := groupSnapshot(t, st, v2, edge)
	if got := upstreamNames(edgeSnap); !slices.Equal(got, []string{"edge-only", "global"}) || edgeSnap.Version != v2 {
		t.Fatalf("edge upstreams = %v version %d, want [edge-only global] at %d", got, edgeSnap.Version, v2)
	}
	if _, err := st.Pool.Exec(ctx, `update engine_groups set upstream_mode = 'override' where id = $1`, edge); err != nil {
		t.Fatal(err)
	}
	vOverride := mutateSQL(t, st, `update resolver_settings set block_ttl = 61`)
	if got := upstreamNames(groupSnapshot(t, st, vOverride, edge)); !slices.Equal(got, []string{"edge-only"}) {
		t.Fatalf("override upstreams = %v, want [edge-only]", got)
	}

	// all_at_once starts in rolling; a canary change waits in pending and supersedes the older one.
	if s := rolloutState(t, st, store.DefaultEngineGroupID, v2); s != "rolling" && s != "superseded" {
		t.Fatalf("default rollout v2 = %s", s)
	}
	if s := rolloutState(t, st, store.DefaultEngineGroupID, vOverride); s != "rolling" {
		t.Fatalf("default rollout %d = %s, want rolling", vOverride, s)
	}
	if s := rolloutState(t, st, edge, v1); s != "superseded" {
		t.Fatalf("edge rollout v1 = %s, want superseded", s)
	}
	if s := rolloutState(t, st, edge, vOverride); s != "pending" {
		t.Fatalf("edge rollout %d = %s, want pending (canary change)", vOverride, s)
	}

	// Content equal to the stable version rolls out at once, even in a canary group.
	if _, err := st.Pool.Exec(ctx, `update rollouts set state = 'completed' where engine_group_id = $1 and version = $2`, edge, int64(vOverride)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `update engine_groups set stable_version = $1 where id = $2`, int64(vOverride), edge); err != nil {
		t.Fatal(err)
	}
	vSame := mutateSQL(t, st, `update resolver_settings set block_ttl = 61`)
	if s := rolloutState(t, st, edge, vSame); s != "rolling" {
		t.Fatalf("unchanged content rollout = %s, want rolling", s)
	}

	// Rollback: the halted rollout becomes rolled_back, the group pauses, the copy rolls out at once.
	vBad := mutateSQL(t, st, `update upstreams set timeout_ms = 400 where name = 'edge-only'`)
	if _, err := st.Pool.Exec(ctx, `update rollouts set state = 'halted' where engine_group_id = $1 and version = $2`, edge, int64(vBad)); err != nil {
		t.Fatal(err)
	}
	var vRB uint64
	var rid uuid.UUID
	err := st.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		vRB, rid, err = snapshot.Republish(ctx, tx, testActor, edge, vOverride, rollout.KindRollback)
		return err
	})
	if err != nil || rid == uuid.Nil || vRB <= vBad {
		t.Fatalf("rollback: version %d rollout %s err %v", vRB, rid, err)
	}
	if rolloutState(t, st, edge, vBad) != "rolled_back" || rolloutState(t, st, edge, vRB) != "rolling" {
		t.Fatal("rollback must mark the halted rollout rolled_back and start rolling at once")
	}
	var paused bool
	_ = st.Pool.QueryRow(ctx, `select rollouts_paused from engine_groups where id = $1`, edge).Scan(&paused)
	if !paused {
		t.Fatal("rollback must pause change rollouts")
	}
	rb, src := groupSnapshot(t, st, vRB, edge), groupSnapshot(t, st, vOverride, edge)
	if rb.Version != vRB {
		t.Fatalf("rollback snapshot version %d, want %d", rb.Version, vRB)
	}
	rb.Version, rb.CreatedUnixMs, src.CreatedUnixMs = src.Version, 0, 0
	if !proto.Equal(rb, src) {
		t.Fatal("rollback snapshot must equal the source snapshot apart from its version")
	}
	vHeld := mutateSQL(t, st, `update upstreams set timeout_ms = 450 where name = 'edge-only'`)
	if s := rolloutState(t, st, edge, vHeld); s != "pending" {
		t.Fatalf("change while paused = %s, want pending", s)
	}

	if _, latest, err := snapshot.Latest(ctx, st.Pool); err != nil || latest.Version != vHeld {
		t.Fatalf("Latest = %v err %v, want the default group's version %d", latest.GetVersion(), err, vHeld)
	}
	err = st.InTx(ctx, func(tx pgx.Tx) error {
		_, _, err := snapshot.Republish(ctx, tx, testActor, edge, 999999, rollout.KindRollback)
		return err
	})
	if !errors.Is(err, snapshot.ErrUnknownVersion) {
		t.Fatalf("unknown version: err = %v", err)
	}
}
