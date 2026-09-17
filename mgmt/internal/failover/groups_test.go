package failover_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/piwi3910/nexora/mgmt/internal/failover"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func TestValidation(t *testing.T) {
	g := failover.Group{Name: "dns136", FrontendIP: "192.168.10.136", Members: [2]uuid.UUID{uuid.New(), uuid.New()}}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"", "dns.example", "192.168.10.136/32", "::1", "::ffff:192.168.10.136", "2001:db8::1", "0.1.2.3", "127.0.0.2", "169.254.0.1", "224.0.0.1", "240.0.0.1", "255.255.255.255"} {
		bad := g
		bad.FrontendIP = ip
		if !errors.Is(bad.Validate(), failover.ErrInvalid) {
			t.Errorf("accepted %q", ip)
		}
	}
	for _, ids := range [][2]uuid.UUID{{}, {g.Members[0], g.Members[0]}, {g.Members[0], uuid.Nil}} {
		bad := g
		bad.Members = ids
		if !errors.Is(bad.Validate(), failover.ErrInvalid) {
			t.Errorf("accepted %v", ids)
		}
	}
}

func TestStatus(t *testing.T) {
	now := time.Now()
	g := failover.Group{Generation: 2}
	base := failover.Observation{AppliedGeneration: 2, Owner: "lb-a", State: "healthy", ObservedAt: now}
	if got := g.Status(nil, now); got != "unknown" {
		t.Fatal(got)
	}
	for _, tc := range []struct {
		name, want string
		mutate     func(*failover.Observation)
	}{
		{"fresh", "healthy", func(o *failover.Observation) {}},
		{"old generation", "pending", func(o *failover.Observation) { o.AppliedGeneration = 1 }},
		{"future generation", "pending", func(o *failover.Observation) { o.AppliedGeneration = 3 }},
		{"stale", "unknown", func(o *failover.Observation) { o.ObservedAt = now.Add(-31 * time.Second) }},
		{"future", "unknown", func(o *failover.Observation) { o.ObservedAt = now.Add(time.Second) }},
		{"no owner", "unknown", func(o *failover.Observation) { o.Owner = "" }},
		{"bad state", "unknown", func(o *failover.Observation) { o.State = "ready" }},
		{"degraded", "degraded", func(o *failover.Observation) { o.State = "degraded" }},
		{"down", "unavailable", func(o *failover.Observation) { o.State = "unavailable" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := base
			tc.mutate(&o)
			if got := g.Status(&o, now); got != tc.want {
				t.Fatalf("%s != %s", got, tc.want)
			}
		})
	}
}

func TestPersistence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st := storetest.New(t)
	if err := st.MigrateDownTo(ctx, 1207); err != nil {
		t.Fatal(err)
	}
	ids := [4]uuid.UUID{}
	for i, n := range []string{"a", "b", "c", "d"} {
		ids[i] = storetest.InsertEngine(t, st, n, store.DefaultEngineGroupID)
	}
	var before, after string
	// Migration 01301 adds a nullable stream token; compare all pre-existing
	// engine fields, not the additive schema key, across the full migration chain.
	const inventory = `select jsonb_agg(to_jsonb(e) - 'connection_session' order by id)::text from engines e`
	if err := st.Pool.QueryRow(ctx, inventory).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool.QueryRow(ctx, inventory).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("migration changed existing engines")
	}
	create := func(g failover.Group) (failover.Group, error) {
		var out failover.Group
		err := st.InTx(ctx, func(tx pgx.Tx) error { var err error; out, err = failover.Create(ctx, tx, g); return err })
		return out, err
	}
	g, err := create(failover.Group{Name: "dns136", FrontendIP: "192.168.10.136", Members: [2]uuid.UUID{ids[0], ids[2]}})
	if err != nil {
		t.Fatal(err)
	}
	if g.ID == uuid.Nil || g.Generation != 1 || g.Status(nil, time.Now()) != "unknown" {
		t.Fatalf("%+v", g)
	}
	other := failover.Group{Name: "dns139", FrontendIP: "192.168.10.139", Members: [2]uuid.UUID{ids[1], ids[3]}}
	for _, tc := range []struct {
		name   string
		mutate func(*failover.Group)
		want   error
	}{
		{"IP collision", func(x *failover.Group) { x.FrontendIP = g.FrontendIP }, store.ErrConflict},
		{"name collision", func(x *failover.Group) { x.Name = g.Name }, store.ErrConflict},
		{"cross slot reuse", func(x *failover.Group) { x.Members[1] = ids[0] }, store.ErrConflict},
		{"missing identity", func(x *failover.Group) { x.Members[0] = uuid.New() }, failover.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := other
			tc.mutate(&x)
			if _, err := create(x); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
	if _, err := st.Pool.Exec(ctx, `update engines set node_name='b' where id=$1`, ids[3]); err != nil {
		t.Fatal(err)
	}
	if _, err := create(other); !errors.Is(err, failover.ErrInvalid) {
		t.Fatalf("same node: %v", err)
	}
	if _, err := st.Pool.Exec(ctx, `update engines set node_name='d',revoked_at=now() where id=$1`, ids[3]); err != nil {
		t.Fatal(err)
	}
	if _, err := create(other); !errors.Is(err, failover.ErrInvalid) {
		t.Fatalf("revoked: %v", err)
	}
	if _, err := st.Pool.Exec(ctx, `update engines set revoked_at=null where id=$1`, ids[3]); err != nil {
		t.Fatal(err)
	}
	if _, err := create(other); err != nil {
		t.Fatal(err)
	}
	if o, err := failover.GetObservation(ctx, st.Pool, g.ID); err != nil || o != nil {
		t.Fatalf("unobserved group: %+v %v", o, err)
	}
	if _, err := st.Pool.Exec(ctx, `insert into failover_observations(group_id,applied_generation,owner,state,reason,observed_at)
		values($1,1,'fixture-owner','healthy','fixture only',now())`, g.ID); err != nil {
		t.Fatal(err)
	}
	g.Name = "renamed"
	// Reversing slots must preserve identities and exact cardinality.
	g.Members[0], g.Members[1] = g.Members[1], g.Members[0]
	err = st.InTx(ctx, func(tx pgx.Tx) error { var err error; g, err = failover.Update(ctx, tx, g, 1); return err })
	if err != nil || g.Generation != 2 {
		t.Fatalf("update %+v %v", g, err)
	}
	err = st.InTx(ctx, func(tx pgx.Tx) error { _, err := failover.Update(ctx, tx, g, 1); return err })
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale: %v", err)
	}
	o, err := failover.GetObservation(ctx, st.Pool, g.ID)
	if err != nil || g.Status(o, time.Now()) != "pending" {
		t.Fatalf("old report must be pending: %+v %v", o, err)
	}
	changed := g
	changed.FrontendIP = "192.168.10.140"
	err = st.InTx(ctx, func(tx pgx.Tx) error { _, err := failover.Update(ctx, tx, changed, 2); return err })
	if !errors.Is(err, failover.ErrInvalid) {
		t.Fatalf("IP change: %v", err)
	}
	groups, err := failover.List(ctx, st.Pool)
	if err != nil || len(groups) != 2 {
		t.Fatalf("list %+v %v", groups, err)
	}
	// Direct SQL cannot commit incomplete membership or remove one member.
	err = st.InTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `delete from failover_members where engine_id=$1`, ids[0])
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("expected deferred membership FK rejection, got %v", err)
	}
	// An extra third member cannot be smuggled into a third slot.
	err = st.InTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into failover_members(group_id,engine_id,slot) values($1,$2,3)`, g.ID, ids[1])
		return err
	})
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("expected slot CHECK rejection, got %v", err)
	}
	var count int
	if err := st.Pool.QueryRow(ctx, `select count(*) from engines`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("engines changed %d %v", count, err)
	}
}

func TestConcurrentMembership(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := storetest.New(t)
	a := storetest.InsertEngine(t, st, "a", store.DefaultEngineGroupID)
	b := storetest.InsertEngine(t, st, "b", store.DefaultEngineGroupID)
	c := storetest.InsertEngine(t, st, "c", store.DefaultEngineGroupID)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, g := range []failover.Group{
		{Name: "one", FrontendIP: "192.0.2.1", Members: [2]uuid.UUID{a, b}},
		{Name: "two", FrontendIP: "192.0.2.2", Members: [2]uuid.UUID{c, a}},
	} {
		go func() {
			<-start
			results <- st.InTx(ctx, func(tx pgx.Tx) error { _, err := failover.Create(ctx, tx, g); return err })
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
