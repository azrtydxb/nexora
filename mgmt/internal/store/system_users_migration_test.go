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

// An install at the last pre-M9 migration keeps its users and then accepts system users.
func TestSystemUsersMigration(t *testing.T) {
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
	sources := provider.ListSources()
	var before int64
	for _, s := range sources {
		if s.Version < 1000 && s.Version > before {
			before = s.Version
		}
	}
	if _, err := provider.UpTo(ctx, before); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `insert into users(username, role, source, password_hash) values ('alice', 'admin', 'local', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `insert into users(username, role, source) values ('early', 'admin', 'system')`); err == nil {
		t.Fatal("source system accepted before 01000")
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.Pool.QueryRow(ctx, `select count(*) from users where username = 'alice' and source = 'local' and role = 'admin'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("alice after migration: n=%d err=%v", n, err)
	}
	if _, err := st.Pool.Exec(ctx, `insert into users(username, role, source) values ('nexora-operator', 'admin', 'system')`); err != nil {
		t.Fatalf("system user refused after 01000: %v", err)
	}
	if _, err := st.Pool.Exec(ctx, `insert into users(username, role, source) values ('bad', 'admin', 'robot')`); err == nil {
		t.Fatal("unknown source accepted")
	}
}
