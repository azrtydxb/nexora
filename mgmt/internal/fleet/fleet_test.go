package fleet_test

import (
	"context"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/piwi3910/nexora/mgmt/internal/fleet"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func TestEngineStatusTargetsAndMetrics(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	if _, err := snapshot.EnsureInitial(ctx, st, snapshot.BuildConfig{}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"current", "behind", "rejected", "revoked", "gone"} {
		storetest.ConnectEngine(t, st, storetest.InsertEngine(t, st, n, store.DefaultEngineGroupID))
	}
	quiet := storetest.InsertEngine(t, st, "quiet", store.DefaultEngineGroupID)
	for _, stmt := range []string{
		`update engines set applied_version = 1 where node_name in ('current', 'revoked')`,
		`update engines set rejected_version = 1, rejected_reason = 'bad' where node_name = 'rejected'`,
		`update engines set revoked_at = now() where node_name = 'revoked'`,
		`update engines set connected_instance = null, last_seen_at = now() - interval '2 minutes' where node_name = 'gone'`,
		`update engines set enrolled_at = now() - interval '5 seconds' where node_name = 'quiet'`,
	} {
		if _, err := st.Pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	views, err := fleet.ListEngines(ctx, st.Pool, fleet.EngineFilter{})
	if err != nil || len(views) != 6 {
		t.Fatalf("views %d err %v", len(views), err)
	}
	want := map[string]string{"current": "current", "behind": "behind", "rejected": "rejected", "revoked": "revoked", "gone": "disconnected", "quiet": "disconnected"}
	for _, v := range views {
		if v.Status != want[v.NodeName] || v.TargetVersion != 1 || v.EngineGroupName != "default" {
			t.Errorf("%s: status %s target %d group %s, want %s 1 default", v.NodeName, v.Status, v.TargetVersion, v.EngineGroupName, want[v.NodeName])
		}
	}
	target, err := fleet.TargetFor(ctx, st.Pool, quiet)
	if err != nil || target.Version != 1 || target.Snapshot.GetVersion() != 1 || target.EngineGroupID != store.DefaultEngineGroupID {
		t.Fatalf("target = %+v err %v", target, err)
	}

	// Positive first: the gauge counts "gone" (unseen for 2 minutes); "quiet" enrolled 5 s ago and
	// "revoked" are not counted.
	expected := `
# HELP nexora_mgmt_engines_disconnected Non-revoked engines without a live control stream for more than 60 seconds
# TYPE nexora_mgmt_engines_disconnected gauge
nexora_mgmt_engines_disconnected 1
`
	if err := testutil.CollectAndCompare(fleet.NewCollector(st), strings.NewReader(expected), "nexora_mgmt_engines_disconnected"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `update engines set revoked_at = now() where node_name = 'gone'`); err != nil {
		t.Fatal(err)
	}
	if err := testutil.CollectAndCompare(fleet.NewCollector(st), strings.NewReader(strings.Replace(expected, "disconnected 1", "disconnected 0", 1)),
		"nexora_mgmt_engines_disconnected"); err != nil {
		t.Fatalf("revoked engines must not count as disconnected: %v", err)
	}
	if n := testutil.CollectAndCount(fleet.NewCollector(st), "nexora_mgmt_engines"); n == 0 {
		t.Fatal("nexora_mgmt_engines not exported")
	}
}
