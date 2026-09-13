package control_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	"github.com/piwi3910/nexora/e2e/harness"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
)

func connectAs(t *testing.T, f *fixture, node string) (controlv1.EngineControl_ConnectClient, string) {
	t.Helper()
	client, id := f.enroll(t, f.addr[0])
	stream, err := client.Connect(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id, NodeName: node}}})
	s := recvSnapshot(t, stream)
	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Applied{Applied: &controlv1.Applied{Version: s.Version}}})
	return stream, id
}

func TestCanaryVersionReachesOnlyCanariesUntilHealthy(t *testing.T) {
	f := setup(t, 1)
	a, aID := connectAs(t, f, "canary-a")
	b, _ := connectAs(t, f, "canary-b")
	harness.Eventually(t, 5*time.Second, func() error {
		return expectRow(f, "select count(*) from rollouts where version = 1 and state = 'completed'", int64(1))
	})
	if _, err := f.st.Pool.Exec(f.ctx, `update engine_groups set rollout_strategy = 'canary', canary_count = 1, min_health_queries = 10`); err != nil {
		t.Fatal(err)
	}
	v, err := snapshot.Mutate(f.ctx, f.st, snapshot.BuildConfig{}, auth.Actor{Type: "system", ID: "t", Name: "t"}, func(tx pgx.Tx) (auth.Change, error) {
		_, err := tx.Exec(f.ctx, "update resolver_settings set block_ttl = 11")
		return auth.Change{Action: "updateResolverSettings", TargetType: "resolver_settings", TargetID: "singleton"}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	// Positive path first: the canary receives the new version.
	if s := recvSnapshot(t, a); s.Version != v {
		t.Fatalf("canary got version %d, want %d", s.Version, v)
	}
	got := make(chan *controlv1.ServerMessage, 1)
	go func() { m, _ := b.Recv(); got <- m }()
	select {
	case m := <-got:
		t.Fatalf("non-canary received %v during the canary phase", m)
	case <-time.After(1500 * time.Millisecond):
	}
	_ = a.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Applied{Applied: &controlv1.Applied{Version: v}}})
	harness.Eventually(t, 5*time.Second, func() error {
		return expectRow(f, "select count(*) from rollouts where state = 'verifying'", int64(1))
	})
	raw := func(q uint64) []byte { r, _ := proto.Marshal(&controlv1.Stats{QueriesTotal: q}); return r }
	for i, q := range []uint64{100, 300, 500} {
		if _, err := f.st.Pool.Exec(f.ctx, `insert into engine_stats (engine_id, at, stats)
			select $1, now() - interval '40 seconds' + $2 * interval '10 seconds', $3`, aID, i, raw(q)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.st.Pool.Exec(f.ctx, `update rollouts set phase_started_at = now() - interval '35 seconds' where state = 'verifying'`); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if m.GetSnapshot().GetVersion() != v {
			t.Fatalf("non-canary got %v, want version %d after the health gate", m, v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("non-canary never received the version after the canary passed")
	}
}

func expectRow(f *fixture, sql string, want any) error {
	var got any
	if err := f.st.Pool.QueryRow(f.ctx, sql).Scan(&got); err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("%s = %v, want %v", sql, got, want)
	}
	return nil
}
