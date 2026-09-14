package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

type pgxTx = pgx.Tx

func TestMigrateSeedsAndMapsErrors(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := store.Open(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	// One stable installation id (labels PKCS#11 key objects); a second row is refused.
	id1, err1 := st.InstallationID(ctx)
	id2, err2 := st.InstallationID(ctx)
	if err1 != nil || err2 != nil || len(id1) != 36 || id1 != id2 {
		t.Fatalf("installation id %q %q (%v %v)", id1, id2, err1, err2)
	}
	if _, err := st.Pool.Exec(ctx, "insert into installation default values"); err == nil {
		t.Fatal("a second installation row was accepted")
	}
	for _, table := range []string{"users", "sessions", "api_tokens", "setup_tokens", "oidc_login_states", "audit_log", "config_versions", "resolver_settings", "upstreams", "access_control", "blobs", "filter_lists", "allowlist", "join_tokens", "engines", "engine_stats", "instances"} {
		var ok bool
		if err := st.Pool.QueryRow(ctx, "select to_regclass($1) is not null", "public."+table).Scan(&ok); err != nil || !ok {
			t.Errorf("table %s missing (%v)", table, err)
		}
	}
	var cidrs int
	if err := st.Pool.QueryRow(ctx, "select cardinality(allow_cidrs) from access_control").Scan(&cidrs); err != nil || cidrs != 8 {
		t.Fatalf("ACL seed = %d (%v)", cidrs, err)
	}
	if _, err := st.Pool.Exec(ctx, "insert into upstreams(name, protocol, address, position) values ('a','udp','1.1.1.1:53',0)"); err != nil {
		t.Fatal(err)
	}
	_, err = st.Pool.Exec(ctx, "insert into upstreams(name, protocol, address, position) values ('a','udp','1.1.1.1:53',1)")
	if !errors.Is(store.MapError(err), store.ErrConflict) {
		t.Fatalf("duplicate name -> %v", store.MapError(err))
	}
	st.Close()
	pgDown := env.StartPostgres()
	st2, err := store.Open(ctx, pgDown.URL)
	if err != nil {
		t.Fatal(err)
	}
	harness.StopPostgres(t, pgDown)
	err = st2.InTx(ctx, func(tx pgxTx) error { _, err := tx.Exec(ctx, "select 1"); return err })
	if !errors.Is(err, store.ErrUnavailable) {
		t.Fatalf("down database -> %v, want ErrUnavailable", err)
	}
}
