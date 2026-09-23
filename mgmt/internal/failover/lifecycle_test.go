package failover_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/failover"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"google.golang.org/protobuf/proto"
)

type lifecycleFixture struct {
	t        *testing.T
	ctx      context.Context
	st       *store.Store
	g        failover.Group
	token    failover.PublisherToken
	ids      [4]uuid.UUID
	evidence []failover.MemberEvidence
}

// These are actual PostgreSQL tests using the repository's isolated database
// harness. They do not claim that the synthetic verifier proves runtime fencing.
func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	f := &lifecycleFixture{t: t, ctx: ctx, st: storetest.New(t)}
	for i := range f.ids {
		f.ids[i] = storetest.InsertEngine(t, f.st, fmt.Sprintf("node-%d", i), store.DefaultEngineGroupID)
	}
	f.tx(func(tx pgx.Tx) error {
		var err error
		f.g, err = failover.Create(ctx, tx, failover.Group{Name: "dns136", FrontendIP: "192.168.10.136", Members: [2]uuid.UUID{f.ids[0], f.ids[2]}})
		return err
	})
	f.source("controller")
	f.tx(func(tx pgx.Tx) error {
		var err error
		f.token, err = failover.RotatePublisher(ctx, tx, f.g.ID, f.g.Generation, 0, uuid.New())
		return err
	})
	return f
}
func (f *lifecycleFixture) tx(fn func(pgx.Tx) error) {
	f.t.Helper()
	if err := f.st.InTx(f.ctx, fn); err != nil {
		f.t.Fatal(err)
	}
}
func (f *lifecycleFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.st.Pool.Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatal(err)
	}
}

// Provisioning is explicit test-only SQL; production must use separate minimal
// LOGINs rather than giving an API/migration owner source authority.
func (f *lifecycleFixture) source(purpose string) {
	f.t.Helper()
	f.exec(`insert into failover_sources(role_oid,role_name,purpose)
 select oid,rolname,$1 from pg_roles where rolname=session_user
 on conflict(role_oid) do update set purpose=excluded.purpose,incarnation=gen_random_uuid()`, purpose)
}
func (f *lifecycleFixture) refresh() {
	now := time.Now().UTC()
	for i := range f.evidence {
		e := &f.evidence[i]
		e.ObservedAt = now
		e.Placement.ObservedAt = now
		e.Ready.ObservedAt = now
		e.Management.ObservedAt = now
		e.DirectDNS.ObservedAt = now
		e.SnapshotObservedAt = now
	}
	f.exec(`update engines set last_seen_at=clock_timestamp() where id in ($1,$2)`, f.g.Members[0], f.g.Members[1])
}
func (f *lifecycleFixture) prepareEvidence() {
	snap := &controlv1.ConfigSnapshot{Version: 1}
	raw, err := proto.Marshal(snap)
	if err != nil {
		f.t.Fatal(err)
	}
	digest, err := snapshot.ContentDigest(snap)
	if err != nil {
		f.t.Fatal(err)
	}
	f.exec(`insert into config_versions(version,created_by,summary) values(1,'fixture','failover fixture')`)
	f.exec(`insert into group_snapshots(version,engine_group_id,snapshot,content_sha256) values(1,$1,$2,$3)`, store.DefaultEngineGroupID, raw, digest)
	f.exec(`update engine_groups set stable_version=1 where id=$1`, store.DefaultEngineGroupID)
	for i, id := range f.g.Members {
		storetest.ConnectEngine(f.t, f.st, id)
		session := uuid.New()
		pod := uuid.NewString()
		f.exec(`update engines set connection_session=$2,applied_version=1 where id=$1`, id, session)
		f.exec(`insert into engine_certificates(serial,engine_id,not_before,not_after) select certificate_serial,id,now()-interval '1 hour',now()+interval '1 hour' from engines where id=$1`, id)
		e := failover.MemberEvidence{EngineID: id, PolicyGroupID: store.DefaultEngineGroupID, ConnectionSession: session, PodUID: pod, ContainerID: "container-" + pod,
			Placement: failover.TrustedPlacement{EngineID: id, PodUID: pod, NodeUID: uuid.NewString(), NodeName: fmt.Sprintf("node-%d", i)},
			Ready:     failover.EligibilityCheck{OK: true}, Management: failover.EligibilityCheck{OK: true}, DirectDNS: failover.EligibilityCheck{OK: true},
			Applied: failover.EligibilitySnapshot{Version: 1, Digest: digest}, Target: failover.EligibilitySnapshot{Version: 1, Digest: digest}}
		f.evidence = append(f.evidence, e)
	}
	f.refresh()
	f.source("collector")
}

type collectFunc func(context.Context, failover.Group, time.Duration) ([]failover.MemberEvidence, error)

func (c collectFunc) Collect(ctx context.Context, g failover.Group, d time.Duration) ([]failover.MemberEvidence, error) {
	return c(ctx, g, d)
}
func (f *lifecycleFixture) publish(sequence int64) (failover.Eligibility, error) {
	return failover.CollectAndPublish(f.ctx, f.st.Pool, f.token, sequence, 30*time.Second, collectFunc(func(context.Context, failover.Group, time.Duration) ([]failover.MemberEvidence, error) {
		return f.evidence, nil
	}))
}
func (f *lifecycleFixture) read() failover.Eligibility {
	f.t.Helper()
	var r failover.Eligibility
	f.tx(func(tx pgx.Tx) error { var err error; r, err = failover.ReadPublished(f.ctx, tx, f.g.ID); return err })
	return r
}
func (f *lifecycleFixture) pendingDelete() {
	f.tx(func(tx pgx.Tx) error {
		var err error
		f.g, err = failover.RequestDeletion(f.ctx, tx, f.g.ID, f.g.Generation)
		return err
	})
	f.rotateCurrent()
}

func (f *lifecycleFixture) rotateCurrent() {
	f.source("controller")
	f.tx(func(tx pgx.Tx) error {
		var err error
		f.token, err = failover.RotatePublisher(f.ctx, tx, f.g.ID, f.g.Generation, f.token.Epoch, uuid.New())
		return err
	})
}

type withdrawalFunc func(context.Context, failover.WithdrawalScope, []byte) error

func (v withdrawalFunc) VerifyWithdrawal(ctx context.Context, s failover.WithdrawalScope, b []byte) error {
	return v(ctx, s, b)
}
func (f *lifecycleFixture) complete(v failover.WithdrawalVerifier) error {
	// Explicit one-attempt transaction: a verifier may have a durable external effect.
	tx, err := f.st.Pool.Begin(f.ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	g, err := failover.CompleteWithdrawal(f.ctx, tx, f.token, []byte("test-verifier-receipt"), v)
	if err != nil {
		return err
	}
	if err = auth.WriteAudit(f.ctx, tx, auth.Actor{Type: "system", ID: "fixture", Name: "fixture"}, auth.Change{Action: "failoverWithdrawal", TargetType: "failover", TargetID: g.ID.String()}, nil); err != nil {
		return err
	}
	if err = tx.Commit(f.ctx); err == nil {
		f.g = g
	}
	return err
}

func TestLifecyclePendingDeletionRetentionAndReuse(t *testing.T) {
	f := newLifecycleFixture(t)
	f.pendingDelete()
	if f.g.Lifecycle != "deleting" {
		t.Fatal(f.g)
	}
	create := func(g failover.Group) error {
		return f.st.InTx(f.ctx, func(tx pgx.Tx) error { _, err := failover.Create(f.ctx, tx, g); return err })
	}
	other := failover.Group{Name: "replacement", FrontendIP: f.g.FrontendIP, Members: [2]uuid.UUID{f.ids[1], f.ids[3]}}
	if err := create(other); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("pending IP reuse: %v", err)
	}
	other.FrontendIP = "192.168.10.139"
	other.Members = f.g.Members
	if err := create(other); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("pending member reuse: %v", err)
	}
	f.source("withdrawal")
	if err := f.complete(nil); !errors.Is(err, failover.ErrInvalid) {
		t.Fatalf("nil verifier: %v", err)
	}
	var typedNil withdrawalFunc
	if err := f.complete(typedNil); !errors.Is(err, failover.ErrInvalid) {
		t.Fatalf("typed nil verifier: %v", err)
	}
	denied := errors.New("runtime proof unavailable")
	if err := f.complete(withdrawalFunc(func(context.Context, failover.WithdrawalScope, []byte) error { return denied })); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	if err := create(other); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("verifier failure released member: %v", err)
	}
	if err := f.complete(withdrawalFunc(func(_ context.Context, s failover.WithdrawalScope, _ []byte) error {
		if s.Group.Generation != f.g.Generation || len(s.Publishers) != 2 || len(s.ReservedMembers) != 2 {
			return fmt.Errorf("incomplete proof scope: %+v", s)
		}
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	if f.g.Lifecycle != "deleted" {
		t.Fatal(f.g)
	}
	other.Name = "dns136"
	other.FrontendIP = f.g.FrontendIP
	if err := create(other); err != nil {
		t.Fatalf("verified release: %v", err)
	}
	history, err := failover.Get(f.ctx, f.st.Pool, f.g.ID)
	if err != nil || history.Lifecycle != "deleted" {
		t.Fatalf("lost tombstone: %+v %v", history, err)
	}
	if err := f.complete(withdrawalFunc(func(context.Context, failover.WithdrawalScope, []byte) error { return nil })); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("old proof replay: %v", err)
	}
	groups, err := failover.List(f.ctx, f.st.Pool)
	if err != nil || len(groups) != 1 || groups[0].ID == f.g.ID {
		t.Fatalf("list tombstone filter: %+v %v", groups, err)
	}
	var receipts, audits int
	if err = f.st.Pool.QueryRow(f.ctx, `select (select count(*) from failover_withdrawals),(select count(*) from audit_log where action='failoverWithdrawal')`).Scan(&receipts, &audits); err != nil || receipts != 1 || audits != 1 {
		t.Fatalf("receipt/audit: %d %d %v", receipts, audits, err)
	}
	if _, err = f.st.Pool.Exec(f.ctx, `delete from failover_groups where id=$1`, f.g.ID); err == nil {
		t.Fatal("raw deletion removed tombstone")
	}
	if err = f.st.MigrateDownTo(f.ctx, 1305); err == nil {
		t.Fatal("downgrade discarded lifecycle history")
	}
}

func TestLifecycleMembershipDrainAndAuditRollback(t *testing.T) {
	f := newLifecycleFixture(t)
	rollback := errors.New("audit failed")
	err := f.st.InTx(f.ctx, func(tx pgx.Tx) error {
		if _, err := failover.RequestDeletion(f.ctx, tx, f.g.ID, 1); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	current, err := failover.Get(f.ctx, f.st.Pool, f.g.ID)
	if err != nil || current.Generation != 1 || current.Lifecycle != "active" {
		t.Fatalf("rollback: %+v %v", current, err)
	}
	f.g.Members[1] = f.ids[3]
	f.tx(func(tx pgx.Tx) error { var err error; f.g, err = failover.Update(f.ctx, tx, f.g, 1); return err })
	if f.g.Lifecycle != "draining" {
		t.Fatal(f.g)
	}
	f.tx(func(tx pgx.Tx) error {
		r, err := failover.ReadPublished(f.ctx, tx, f.g.ID)
		if len(r.Eligible) != 0 {
			t.Fatal("draining pool")
		}
		return err
	})
	err = f.st.InTx(f.ctx, func(tx pgx.Tx) error { _, err := failover.Update(f.ctx, tx, f.g, 2); return err })
	if !errors.Is(err, failover.ErrInvalid) {
		t.Fatalf("edit bypassed drain: %v", err)
	}
	other := failover.Group{Name: "dns139", FrontendIP: "192.168.10.139", Members: [2]uuid.UUID{f.ids[1], f.ids[2]}}
	err = f.st.InTx(f.ctx, func(tx pgx.Tx) error { _, err := failover.Create(f.ctx, tx, other); return err })
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("old member freed: %v", err)
	}
	f.rotateCurrent()
	f.source("withdrawal")
	if err = f.complete(withdrawalFunc(func(_ context.Context, s failover.WithdrawalScope, _ []byte) error {
		if len(s.ReservedMembers) != 3 {
			return fmt.Errorf("missing historical member: %+v", s)
		}
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	if f.g.Lifecycle != "withdrawn" {
		t.Fatal(f.g)
	}
	f.tx(func(tx pgx.Tx) error { _, err := failover.Create(f.ctx, tx, other); return err })
	f.tx(func(tx pgx.Tx) error {
		var err error
		f.g, err = failover.Resume(f.ctx, tx, f.g.ID, f.g.Generation)
		return err
	})
	if f.g.Lifecycle != "active" || f.g.Generation != 4 {
		t.Fatal(f.g)
	}
	// A delayed deletion request for the pre-drain generation cannot mutate it.
	err = f.st.InTx(f.ctx, func(tx pgx.Tx) error { _, err := failover.RequestDeletion(f.ctx, tx, f.g.ID, 1); return err })
	if !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
}

func TestPublicationDatabaseRevalidation(t *testing.T) {
	for _, kind := range []string{"session", "revoked", "certificate", "persist error", "policy", "target", "source revoked", "source regrant", "drain", "deleted engine"} {
		t.Run(kind, func(t *testing.T) {
			f := newLifecycleFixture(t)
			f.prepareEvidence()
			r, err := f.publish(1)
			if err != nil || len(r.Eligible) != 2 {
				t.Fatalf("initial: %+v %v", r, err)
			}
			if r = f.read(); len(r.Eligible) != 2 {
				t.Fatalf("read initial: %+v", r)
			}
			switch kind {
			case "session":
				f.exec(`update engines set connection_session=$2 where id=$1`, f.g.Members[0], uuid.New())
			case "revoked":
				f.exec(`update engines set revoked_at=clock_timestamp() where id=$1`, f.g.Members[0])
			case "certificate":
				f.exec(`update engine_certificates set revoked_at=clock_timestamp(),revoke_reason='revoked' where engine_id=$1`, f.g.Members[0])
			case "persist error":
				f.exec(`update engines set persist_error='disk failure' where id=$1`, f.g.Members[0])
			case "policy":
				var policy uuid.UUID
				if err = f.st.Pool.QueryRow(f.ctx, `insert into engine_groups(name) values('other') returning id`).Scan(&policy); err != nil {
					t.Fatal(err)
				}
				f.exec(`update engines set engine_group_id=$2 where id=$1`, f.g.Members[0], policy)
			case "target":
				f.exec(`update engine_groups set stable_version=null where id=$1`, store.DefaultEngineGroupID)
			case "source revoked":
				f.exec(`delete from failover_sources`)
			case "source regrant":
				f.source("collector")
			case "drain":
				f.tx(func(tx pgx.Tx) error { _, err := failover.BeginDrain(f.ctx, tx, f.g.ID, f.g.Generation); return err })
			case "deleted engine":
				f.exec(`update engines set deleted_at=clock_timestamp() where id=$1`, f.g.Members[0])
			}
			r = f.read()
			if kind == "revoked" || kind == "certificate" || kind == "persist error" || kind == "deleted engine" {
				if !reflect.DeepEqual(r.Eligible, []uuid.UUID{f.g.Members[1]}) {
					t.Fatalf("healthy partner lost or bad member retained: %+v", r)
				}
			} else if len(r.Eligible) != 0 {
				t.Fatalf("stale publication used: %+v", r)
			}
		})
	}
}

func TestPublicationCollectionFailuresAndDelayedWrites(t *testing.T) {
	f := newLifecycleFixture(t)
	f.prepareEvidence()
	if r, err := f.publish(1); err != nil || len(r.Eligible) != 2 {
		t.Fatalf("initial %+v %v", r, err)
	}
	probeErr := errors.New("direct DNS failed once")
	calls := 0
	r, err := failover.CollectAndPublish(f.ctx, f.st.Pool, f.token, 2, 30*time.Second, collectFunc(func(context.Context, failover.Group, time.Duration) ([]failover.MemberEvidence, error) {
		calls++
		return f.evidence, probeErr
	}))
	if !errors.Is(err, probeErr) || calls != 1 || len(r.Eligible) != 0 || len(f.read().Eligible) != 0 {
		t.Fatalf("error retained previous pool/retried: %+v %v calls %d", r, err, calls)
	}
	// Out-of-order collection completes after a newer sequence; the old write loses.
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := failover.CollectAndPublish(f.ctx, f.st.Pool, f.token, 3, 30*time.Second, collectFunc(func(ctx context.Context, _ failover.Group, _ time.Duration) ([]failover.MemberEvidence, error) {
			close(started)
			select {
			case <-release:
				return f.evidence, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}))
		done <- err
	}()
	select {
	case <-started:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	if len(f.read().Eligible) != 0 {
		t.Fatal("in-flight collector retained old pool")
	}
	if r, err = f.publish(4); err != nil || len(r.Eligible) != 2 {
		t.Fatalf("new collection %+v %v", r, err)
	}
	close(release)
	if err = <-done; !errors.Is(err, store.ErrConflict) {
		t.Fatalf("late write: %v", err)
	}
	if _, err = f.publish(4); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("sequence replay: %v", err)
	}
	// Identical desired policy group does not make different effective targets equal.
	f.evidence[1].Target.Digest = strings.Repeat("f", 64)
	if r, err = f.publish(5); err != nil || len(r.Eligible) != 0 {
		t.Fatalf("mismatch: %+v %v", r, err)
	}
}

func TestPublicationStaleAuthorityAndGenerationDuringCollection(t *testing.T) {
	for _, kind := range []string{"generation", "owner", "session", "source regrant", "future time", "old time", "nil session"} {
		t.Run(kind, func(t *testing.T) {
			f := newLifecycleFixture(t)
			f.prepareEvidence()
			r, err := failover.CollectAndPublish(f.ctx, f.st.Pool, f.token, 1, 30*time.Second, collectFunc(func(context.Context, failover.Group, time.Duration) ([]failover.MemberEvidence, error) {
				switch kind {
				case "generation":
					f.tx(func(tx pgx.Tx) error {
						g := f.g
						g.Name = "renamed"
						_, err := failover.Update(f.ctx, tx, g, g.Generation)
						return err
					})
				case "owner":
					f.source("controller")
					f.tx(func(tx pgx.Tx) error {
						_, err := failover.RotatePublisher(f.ctx, tx, f.g.ID, f.g.Generation, 1, uuid.New())
						return err
					})
					f.source("collector")
				case "session":
					f.exec(`update engines set connection_session=$2 where id=$1`, f.g.Members[0], uuid.New())
				case "source regrant":
					f.source("collector")
				case "future time":
					f.evidence[0].DirectDNS.ObservedAt = time.Now().Add(time.Hour)
				case "old time":
					f.evidence[0].DirectDNS.ObservedAt = time.Now().Add(-time.Hour)
				case "nil session":
					f.evidence[0].ConnectionSession = uuid.Nil
				}
				return f.evidence, nil
			}))
			if kind == "generation" {
				if !errors.Is(err, store.ErrConflict) {
					t.Fatalf("%v", err)
				}
			} else if kind == "source regrant" || kind == "owner" {
				if !errors.Is(err, failover.ErrUnauthorized) {
					t.Fatalf("%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if kind == "future time" || kind == "old time" {
				if !reflect.DeepEqual(r.Eligible, []uuid.UUID{f.g.Members[1]}) {
					t.Fatalf("bad check admitted: %+v", r)
				}
			} else if len(r.Eligible) != 0 {
				t.Fatalf("stale collection: %+v", r)
			}
		})
	}
}

func TestLifecycleAuthorizationAndOwnerCAS(t *testing.T) {
	f := newLifecycleFixture(t)
	f.exec(`delete from failover_sources`)
	err := f.st.InTx(f.ctx, func(tx pgx.Tx) error {
		_, err := failover.RotatePublisher(f.ctx, tx, f.g.ID, 1, 1, uuid.New())
		return err
	})
	if !errors.Is(err, failover.ErrUnauthorized) {
		t.Fatalf("default authority: %v", err)
	}
	f.source("controller")
	old := f.token
	f.tx(func(tx pgx.Tx) error {
		var err error
		f.token, err = failover.RotatePublisher(f.ctx, tx, f.g.ID, 1, 1, uuid.New())
		return err
	})
	err = f.st.InTx(f.ctx, func(tx pgx.Tx) error {
		_, err := failover.RotatePublisher(f.ctx, tx, f.g.ID, 1, 1, uuid.New())
		return err
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale rotation: %v", err)
	}
	f.pendingDelete()
	f.source("withdrawal")
	old.Generation = f.g.Generation
	called := false
	err = f.st.InTx(f.ctx, func(tx pgx.Tx) error {
		_, err := failover.CompleteWithdrawal(f.ctx, tx, old, []byte("opaque"), withdrawalFunc(func(context.Context, failover.WithdrawalScope, []byte) error { called = true; return nil }))
		return err
	})
	if !errors.Is(err, store.ErrConflict) || called {
		t.Fatalf("stale owner reached verifier: %v %v", err, called)
	}
	if err = f.complete(withdrawalFunc(func(_ context.Context, s failover.WithdrawalScope, _ []byte) error {
		if len(s.Publishers) != 3 {
			return fmt.Errorf("forgot old writer: %+v", s)
		}
		return nil
	})); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleCannotRetagPublisherGeneration(t *testing.T) {
	f := newLifecycleFixture(t)
	f.tx(func(tx pgx.Tx) error {
		var err error
		f.g, err = failover.BeginDrain(f.ctx, tx, f.g.ID, f.g.Generation)
		return err
	})
	f.source("withdrawal")
	f.token.Generation = f.g.Generation // Deliberately forge the newer desired generation.
	calls := 0
	if err := f.complete(withdrawalFunc(func(context.Context, failover.WithdrawalScope, []byte) error { calls++; return nil })); !errors.Is(err, store.ErrConflict) || calls != 0 {
		t.Fatalf("retagged token reached verifier: %v, calls=%d", err, calls)
	}
	f.rotateCurrent()
	f.source("withdrawal")
	if err := f.complete(withdrawalFunc(func(_ context.Context, s failover.WithdrawalScope, _ []byte) error {
		if len(s.Publishers) != 2 || s.Publishers[0].Generation != 1 || s.Publishers[1].Generation != 2 {
			return fmt.Errorf("invented historical grants: %+v", s.Publishers)
		}
		return nil
	})); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleReservationMutationGuards(t *testing.T) {
	f := newLifecycleFixture(t)
	for _, sql := range []string{
		`update failover_ip_reservations set frontend_ip='192.168.10.140'`,
		`update failover_engine_reservations set engine_id='00000000-0000-0000-0000-000000000099'`,
		`delete from failover_ip_reservations`,
		`delete from failover_engine_reservations`,
	} {
		if _, err := f.st.Pool.Exec(f.ctx, sql); err == nil {
			t.Fatalf("reservation bypass: %s", sql)
		}
	}
}

func TestLifecycleSetRoleCannotAuthorizeSession(t *testing.T) {
	f := newLifecycleFixture(t)
	f.exec(`delete from failover_sources`)
	// A NOLOGIN role cannot be the original connection identity. Even granting
	// SET ROLE and selecting it must not impersonate a provisioned controller.
	role := "failover_fixture_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	ident := pgx.Identifier{role}.Sanitize()
	f.exec(`create role ` + ident + ` nologin`)
	t.Cleanup(func() {
		_, _ = f.st.Pool.Exec(context.Background(), `drop owned by `+ident)
		_, _ = f.st.Pool.Exec(context.Background(), `drop role `+ident)
	})
	f.exec(`grant select on failover_sources to ` + ident)
	f.exec(`grant update(lock_marker) on failover_sources to ` + ident)
	f.exec(`insert into failover_sources(role_oid,role_name,purpose) select oid,rolname,'controller' from pg_roles where rolname=$1`, role)
	err := f.st.InTx(f.ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(f.ctx, `set local role `+ident); err != nil {
			return err
		}
		_, err := failover.RotatePublisher(f.ctx, tx, f.g.ID, 1, 1, uuid.New())
		return err
	})
	if !errors.Is(err, failover.ErrUnauthorized) {
		t.Fatalf("current_user impersonated session_user: %v", err)
	}
}

func TestLifecycleConcurrentIPReservation(t *testing.T) {
	f := newLifecycleFixture(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, ids := range [][2]uuid.UUID{{f.ids[0], f.ids[2]}, {f.ids[1], f.ids[3]}} {
		// First pair is already reserved, so use fresh engine identities for both.
		ids[0] = storetest.InsertEngine(t, f.st, fmt.Sprintf("extra-%d-a", i), store.DefaultEngineGroupID)
		ids[1] = storetest.InsertEngine(t, f.st, fmt.Sprintf("extra-%d-b", i), store.DefaultEngineGroupID)
		g := failover.Group{Name: fmt.Sprintf("concurrent-%d", i), FrontendIP: "192.0.2.99", Members: ids}
		go func() {
			<-start
			results <- f.st.InTx(f.ctx, func(tx pgx.Tx) error { _, err := failover.Create(f.ctx, tx, g); return err })
		}()
	}
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if errors.Is(err, store.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestEligibilityInventoryDeadlineAndDrain(t *testing.T) {
	g, members, now := eligibilityFixture()
	members[0].InventoryValidUntil = now
	r := failover.EvaluateEligibility(g, members, now, time.Minute)
	if !reflect.DeepEqual(r.Eligible, []uuid.UUID{g.Members[1]}) {
		t.Fatalf("expired DB evidence admitted: %+v", r)
	}
	g.Lifecycle = "draining"
	if r = failover.EvaluateEligibility(g, members, now, time.Minute); len(r.Eligible) != 0 {
		t.Fatalf("draining pair admitted: %+v", r)
	}
}

func TestPublicationCertificateExpiresWhileDatabaseReadWaits(t *testing.T) {
	f := newLifecycleFixture(t)
	f.prepareEvidence()
	if r, err := f.publish(1); err != nil || len(r.Eligible) != 2 {
		t.Fatalf("initial %+v %v", r, err)
	}
	var expiry time.Time
	if err := f.st.Pool.QueryRow(f.ctx, `update engine_certificates set not_after=clock_timestamp()+interval '2 seconds' where engine_id=$1 returning not_after`, f.g.Members[0]).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	holder, err := f.st.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.Background())
	if _, err = holder.Exec(f.ctx, `select id from engine_groups where id=$1 for update`, store.DefaultEngineGroupID); err != nil {
		t.Fatal(err)
	}
	pidReady := make(chan int32, 1)
	type outcome struct {
		r   failover.Eligibility
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		tx, err := f.st.Pool.Begin(f.ctx)
		if err != nil {
			done <- outcome{err: err}
			return
		}
		defer tx.Rollback(context.Background())
		var pid int32
		if err = tx.QueryRow(f.ctx, `select pg_backend_pid()`).Scan(&pid); err != nil {
			done <- outcome{err: err}
			return
		}
		pidReady <- pid
		r, err := failover.ReadPublished(f.ctx, tx, f.g.ID)
		done <- outcome{r, err}
	}()
	var pid int32
	select {
	case pid = <-pidReady:
	case got := <-done:
		t.Fatal(got.err)
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	sawWait := false
	for {
		var waiting, expired bool
		if err = f.st.Pool.QueryRow(f.ctx, `select coalesce((select wait_event_type='Lock' from pg_stat_activity where pid=$1),false),clock_timestamp()>$2`, pid, expiry).Scan(&waiting, &expired); err != nil {
			t.Fatal(err)
		}
		if waiting && !expired {
			sawWait = true
		}
		if expired {
			break
		}
		select {
		case got := <-done:
			t.Fatalf("read did not wait: %+v %v", got.r, got.err)
		case <-time.After(10 * time.Millisecond):
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
		}
	}
	if !sawWait {
		t.Fatal("fixture failed to block inventory validation before certificate expiry")
	}
	if err = holder.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || !reflect.DeepEqual(got.r.Eligible, []uuid.UUID{f.g.Members[1]}) {
			t.Fatalf("expiry during lock wait: %+v %v", got.r, got.err)
		}
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
}

func TestLifecycleTemporaryTableCannotForgeSourceAuthority(t *testing.T) {
	f := newLifecycleFixture(t)
	f.exec(`delete from failover_sources`)
	err := f.st.InTx(f.ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(f.ctx, `create temporary table failover_sources (like public.failover_sources including all) on commit drop`); err != nil {
			return err
		}
		if _, err := tx.Exec(f.ctx, `insert into pg_temp.failover_sources(role_oid,role_name,purpose) select oid,rolname,'controller' from pg_roles where rolname=session_user`); err != nil {
			return err
		}
		_, err := failover.RotatePublisher(f.ctx, tx, f.g.ID, 1, 1, uuid.New())
		return err
	})
	if !errors.Is(err, failover.ErrUnauthorized) {
		t.Fatalf("temporary allowlist forged authority: %v", err)
	}
}

func TestPublicationTemporaryInventoryCannotHideReconnect(t *testing.T) {
	f := newLifecycleFixture(t)
	f.prepareEvidence()
	if r, err := f.publish(1); err != nil || len(r.Eligible) != 2 {
		t.Fatalf("initial %+v %v", r, err)
	}
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(f.ctx, `create temporary table engines (like public.engines including all) on commit drop`); err != nil {
			return err
		}
		if _, err := tx.Exec(f.ctx, `insert into pg_temp.engines select * from public.engines`); err != nil {
			return err
		}
		if _, err := tx.Exec(f.ctx, `update public.engines set connection_session=$2 where id=$1`, f.g.Members[0], uuid.New()); err != nil {
			return err
		}
		if _, err := tx.Exec(f.ctx, `set local search_path = pg_temp,public`); err != nil {
			return err
		}
		r, err := failover.ReadPublished(f.ctx, tx, f.g.ID)
		if len(r.Eligible) != 0 {
			t.Fatalf("temporary inventory concealed reconnect: %+v", r)
		}
		return err
	})
}

func TestLifecycleMigrationBackfillPreservesBaseline(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.MigrateDownTo(ctx, 1305); err != nil {
		t.Fatal(err)
	}
	a := storetest.InsertEngine(t, st, "baseline-a", store.DefaultEngineGroupID)
	b := storetest.InsertEngine(t, st, "baseline-b", store.DefaultEngineGroupID)
	id := uuid.New()
	// Seed the actual old schema directly: current CRUD requires migration 1306.
	if err := st.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into failover_groups(id,name,frontend_ip,member_a,member_b) values($1,'baseline','192.0.2.136',$2,$3)`, id, a, b); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `insert into failover_members(group_id,engine_id,slot) values($1,$2,1),($1,$3,2)`, id, a, b)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	const oldState = `select jsonb_build_object('groups',(select jsonb_agg(to_jsonb(g)-'lifecycle' order by id) from failover_groups g),'engines',(select jsonb_agg(to_jsonb(e) order by id) from engines e),'members',(select jsonb_agg(to_jsonb(m) order by engine_id) from failover_members m))::text`
	var before, after string
	if err := st.Pool.QueryRow(ctx, oldState).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool.QueryRow(ctx, oldState).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("migration changed baseline identities/data: before=%s after=%s", before, after)
	}
	var members, ips, sources int
	if err := st.Pool.QueryRow(ctx, `select (select count(*) from failover_engine_reservations where group_id=$1),(select count(*) from failover_ip_reservations where group_id=$1),(select count(*) from failover_sources)`, id).Scan(&members, &ips, &sources); err != nil || members != 2 || ips != 1 || sources != 0 {
		t.Fatalf("backfill %d %d %d: %v", members, ips, sources, err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
}
