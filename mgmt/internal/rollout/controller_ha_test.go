package rollout_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	"github.com/piwi3910/nexora/e2e/harness"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/rollout"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

// recordTransitions makes PostgreSQL log every rollout write that sets updated_at (every controller
// step and supersede; the tests' own backdating leaves it alone), so a test sees each transition
// exactly as committed, whichever controller made it, and a repeated write shows as "s->s".
func recordTransitions(t *testing.T, st *store.Store) {
	t.Helper()
	if _, err := st.Pool.Exec(context.Background(), `
		create table rollout_transitions (seq bigserial primary key, rollout_id uuid, version bigint, from_state text, to_state text);
		create function record_rollout_transition() returns trigger language plpgsql as $$
		begin
			if old.updated_at is distinct from new.updated_at then
				insert into rollout_transitions (rollout_id, version, from_state, to_state) values (new.id, new.version, old.state, new.state);
			end if;
			return new;
		end $$;
		create trigger rollout_transitions after update on rollouts for each row execute function record_rollout_transition();`); err != nil {
		t.Fatal(err)
	}
}

func transitions(t *testing.T, st *store.Store, version uint64) []string {
	t.Helper()
	rows, err := st.Pool.Query(context.Background(), `select from_state || '->' || to_state from rollout_transitions
		where version = $1 order by seq`, int64(version))
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// secondInstance opens another connection pool on the same database: a second management plane.
func secondInstance(t *testing.T, st *store.Store) *store.Store {
	t.Helper()
	other, err := store.Open(context.Background(), st.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.Close)
	return other
}

func rolloutRow(t *testing.T, st *store.Store, version uint64) (uuid.UUID, string, []uuid.UUID) {
	t.Helper()
	var id uuid.UUID
	var state string
	var canaries []uuid.UUID
	if err := st.Pool.QueryRow(context.Background(), `select id, state, canary_engine_ids from rollouts
		where engine_group_id = $1 and version = $2`, store.DefaultEngineGroupID, int64(version)).Scan(&id, &state, &canaries); err != nil {
		t.Fatal(err)
	}
	return id, state, canaries
}

func canaryGroupWithEngines(t *testing.T, st *store.Store, names ...string) []uuid.UUID {
	t.Helper()
	ctx := context.Background()
	if _, err := snapshot.EnsureInitial(ctx, st, snapshot.BuildConfig{}); err != nil {
		t.Fatal(err)
	}
	var ids []uuid.UUID
	for _, n := range names {
		id := storetest.InsertEngine(t, st, n, store.DefaultEngineGroupID)
		storetest.ConnectEngine(t, st, id)
		ids = append(ids, id)
	}
	if _, err := st.Pool.Exec(ctx, `update engines set applied_version = 1`); err != nil {
		t.Fatal(err)
	}
	return ids
}

func publishCanaryChange(t *testing.T, st *store.Store, ttl int) uint64 {
	t.Helper()
	ctx := context.Background()
	if _, err := st.Pool.Exec(ctx, `update engine_groups set rollout_strategy = 'canary', canary_count = 1,
		min_health_queries = 10, health_window_seconds = 20`); err != nil {
		t.Fatal(err)
	}
	v, err := snapshot.Mutate(ctx, st, snapshot.BuildConfig{}, auth.Actor{Type: "system", ID: "t", Name: "t"}, func(tx pgx.Tx) (auth.Change, error) {
		_, err := tx.Exec(ctx, `update resolver_settings set block_ttl = $1`, ttl)
		return auth.Change{Action: "updateResolverSettings", TargetType: "resolver_settings", TargetID: "singleton"}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// passHealthWindow stores healthy samples for the canary and backdates the verifying phase past
// the health window, in one transaction so no controller sees the window without the samples.
func passHealthWindow(ctx context.Context, st *store.Store, rolloutID, canary uuid.UUID) error {
	return st.InTx(ctx, func(tx pgx.Tx) error {
		var phase time.Time
		if err := tx.QueryRow(ctx, `update rollouts set phase_started_at = now() - interval '25 seconds'
			where id = $1 and state = 'verifying' returning phase_started_at`, rolloutID).Scan(&phase); err != nil {
			return err
		}
		for i, q := range []uint64{100, 400, 700} {
			raw, _ := proto.Marshal(&controlv1.Stats{QueriesTotal: q})
			if _, err := tx.Exec(ctx, `insert into engine_stats (engine_id, at, stats) values ($1, $2, $3)`,
				canary, phase.Add(time.Duration(i*10-5)*time.Second), raw); err != nil {
				return err
			}
		}
		return nil
	})
}

// Two management plane instances, each with a running controller and extra goroutines calling
// Step as fast as they can, drive one canary rollout: every transition is committed exactly once,
// in order.
func TestTwoControllersNeverDoubleAdvance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st := storetest.New(t)
	recordTransitions(t, st)
	engines := canaryGroupWithEngines(t, st, "a", "b", "c")
	instances := []*store.Store{st, secondInstance(t, st)}

	// Positive path first: version 1 completes once all three engines applied it.
	if n, err := (&rollout.Controller{Store: st}).Step(ctx); err != nil || n != 1 {
		t.Fatalf("version 1: driven=%d err=%v", n, err)
	}
	v := publishCanaryChange(t, st, 13)

	var wg sync.WaitGroup
	runCtx, stop := context.WithCancel(ctx)
	errs := make(chan error, 64)
	for _, inst := range instances {
		c := &rollout.Controller{Store: inst, Tick: 20 * time.Millisecond}
		wg.Add(1)
		go func() { defer wg.Done(); c.Run(runCtx) }()
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for runCtx.Err() == nil {
					if _, err := c.Step(runCtx); err != nil && runCtx.Err() == nil {
						errs <- err
						return
					}
				}
			}()
		}
	}

	// The "engines": apply on push and report healthy stats, driven by the committed state only.
	healthy := false
	harness.Eventually(t, 45*time.Second, func() error {
		id, state, canaries := rolloutRow(t, st, v)
		switch state {
		case "canary":
			if _, err := st.Pool.Exec(ctx, `update engines set applied_version = $1 where id = any($2)`, int64(v), canaries); err != nil {
				return err
			}
		case "verifying":
			if !healthy {
				if err := passHealthWindow(ctx, st, id, canaries[0]); err != nil {
					return err
				}
				healthy = true
			}
		case "rolling":
			if _, err := st.Pool.Exec(ctx, `update engines set applied_version = $1`, int64(v)); err != nil {
				return err
			}
		case "completed":
			return nil
		default:
			var reason string
			_ = st.Pool.QueryRow(ctx, `select halt_reason from rollouts where id = $1`, id).Scan(&reason)
			return fmt.Errorf("unexpected state %s: %s", state, reason)
		}
		return fmt.Errorf("state %s", state)
	})
	stop()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent step: %v", err)
	}

	want := []string{"pending->canary", "canary->verifying", "verifying->rolling", "rolling->completed"}
	if got := transitions(t, st, v); !slices.Equal(got, want) {
		t.Fatalf("transitions of version %d = %v, want %v", v, got, want)
	}
	if got := transitions(t, st, 1); !slices.Equal(got, []string{"rolling->completed"}) {
		t.Fatalf("transitions of version 1 = %v", got)
	}
	var stable int64
	if err := st.Pool.QueryRow(ctx, `select stable_version from engine_groups where id = $1`, store.DefaultEngineGroupID).Scan(&stable); err != nil || uint64(stable) != v {
		t.Fatalf("stable_version = %d err %v, want %d", stable, err, v)
	}
	_, _, canaries := rolloutRow(t, st, v)
	if len(canaries) != 1 || !slices.Contains(engines, canaries[0]) {
		t.Fatalf("canaries %v", canaries)
	}
}

// A controller that stops (process exit) or dies inside its transaction leaves the rollout in its
// last committed state; a controller on another instance resumes from there.
func TestControllerRestartResumesFromDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st := storetest.New(t)
	recordTransitions(t, st)
	canaryGroupWithEngines(t, st, "a", "b")
	v := publishCanaryChange(t, st, 17)

	// Instance A drives pending -> canary and exits.
	aCtx, stopA := context.WithCancel(ctx)
	aDone := make(chan struct{})
	go func() { defer close(aDone); (&rollout.Controller{Store: st, Tick: 20 * time.Millisecond}).Run(aCtx) }()
	harness.Eventually(t, 10*time.Second, func() error {
		if _, state, _ := rolloutRow(t, st, v); state != "canary" {
			return fmt.Errorf("state %s", state)
		}
		return nil
	})
	stopA()
	<-aDone
	id, _, canaries := rolloutRow(t, st, v)
	if _, err := st.Pool.Exec(ctx, `update engines set applied_version = $1 where id = $2`, int64(v), canaries[0]); err != nil {
		t.Fatal(err)
	}

	// A controller dies mid-step: it holds the advisory lock and has written verifying, but its
	// backend is terminated before COMMIT.
	dying, err := st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := dying.QueryRow(ctx, `select pg_backend_pid() from (select pg_advisory_xact_lock(hashtext('nexora:rollout:' || $1::text))) l`, id).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := dying.Exec(ctx, `update rollouts set state = 'verifying', phase_started_at = now() where id = $1`, id); err != nil {
		t.Fatal(err)
	}
	b := secondInstance(t, st)
	cb := &rollout.Controller{Store: b}
	if n, err := cb.Step(ctx); err != nil || n != 0 {
		t.Fatalf("step while another controller holds the lock: driven=%d err=%v", n, err)
	}
	if _, err := b.Pool.Exec(ctx, `select pg_terminate_backend($1)`, pid); err != nil {
		t.Fatal(err)
	}
	_ = dying.Rollback(context.Background())
	if _, state, _ := rolloutRow(t, st, v); state != "canary" {
		t.Fatalf("state after the crash = %s, want the committed canary", state)
	}

	// Instance B resumes: canary -> verifying -> rolling -> completed.
	harness.Eventually(t, 10*time.Second, func() error {
		if _, err := cb.Step(ctx); err != nil {
			return err
		}
		if _, state, _ := rolloutRow(t, st, v); state != "verifying" {
			return fmt.Errorf("state %s", state)
		}
		return nil
	})
	if err := passHealthWindow(ctx, b, id, canaries[0]); err != nil {
		t.Fatal(err)
	}
	if n, err := cb.Step(ctx); err != nil || n != 1 {
		t.Fatalf("verifying -> rolling: driven=%d err=%v", n, err)
	}
	if _, err := st.Pool.Exec(ctx, `update engines set applied_version = $1`, int64(v)); err != nil {
		t.Fatal(err)
	}
	if n, err := cb.Step(ctx); err != nil || n != 1 {
		t.Fatalf("rolling -> completed: driven=%d err=%v", n, err)
	}
	want := []string{"pending->canary", "canary->verifying", "verifying->rolling", "rolling->completed"}
	if got := transitions(t, st, v); !slices.Equal(got, want) {
		t.Fatalf("transitions = %v, want %v", got, want)
	}
}
