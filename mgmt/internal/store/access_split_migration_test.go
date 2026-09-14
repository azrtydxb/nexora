package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/migrations"
)

// An install at 00503 with a custom ACL keeps it as recursion access and gets "any" authoritative access.
func TestAccessSplitMigrationKeepsBehaviour(t *testing.T) {
	pg := harness.New(t).StartPostgres()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, err := store.Open(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := stdlib.OpenDBFromPool(st.Pool)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 503); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `update access_control set allow_cidrs = array['192.0.2.0/24']::cidr[]`); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var rec, auth []string
	if err := st.Pool.QueryRow(ctx, `select array(select c::text from unnest(allow_cidrs) c), array(select c::text from unnest(authoritative_allow_cidrs) c) from access_control`).Scan(&rec, &auth); err != nil {
		t.Fatal(err)
	}
	if len(rec) != 1 || rec[0] != "192.0.2.0/24" {
		t.Fatalf("recursion ACL changed: %v", rec)
	}
	if len(auth) != 2 || auth[0] != "0.0.0.0/0" || auth[1] != "::/0" {
		t.Fatalf("authoritative default: %v", auth)
	}
}
