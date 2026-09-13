package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func TestFleetMigration(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	db := st.Pool

	var name string
	if err := db.QueryRow(ctx, `select name from engine_groups where id = $1`, store.DefaultEngineGroupID).Scan(&name); err != nil || name != "default" {
		t.Fatalf("default engine group: name=%q err=%v", name, err)
	}
	hasColumn := func(table, column string) bool {
		var n int
		err := db.QueryRow(ctx, `select count(*) from information_schema.columns
			where table_schema = 'public' and table_name = $1 and column_name = $2`, table, column).Scan(&n)
		return err == nil && n == 1
	}
	for _, table := range store.EngineScopedTables {
		if !hasColumn(table, "engine_group_id") {
			t.Errorf("%s.engine_group_id missing", table)
		}
	}
	if !hasColumn("rewrites", "group_id") {
		t.Error("rewrites.group_id (the M2 policy group) must stay")
	}
	for _, c := range []string{"engine_group_id", "labels", "revoked_at", "cert_rotate_requested_at", "revision"} {
		if !hasColumn("engines", c) {
			t.Errorf("engines.%s missing", c)
		}
	}
	for _, c := range []string{"engine_group_id", "labels", "max_uses"} {
		if !hasColumn("join_tokens", c) {
			t.Errorf("join_tokens.%s missing", c)
		}
	}
	for _, tbl := range []string{"group_snapshots", "rollouts", "engine_certificates"} {
		var reg *string
		if err := db.QueryRow(ctx, `select to_regclass('public.' || $1)::text`, tbl).Scan(&reg); err != nil || reg == nil {
			t.Errorf("table %s missing (err=%v)", tbl, err)
		}
	}
	var def string
	if err := db.QueryRow(ctx, `select indexdef from pg_indexes where indexname = 'rollouts_one_active_per_group'`).Scan(&def); err != nil ||
		!strings.Contains(def, "UNIQUE") || !strings.Contains(def, "verifying") {
		t.Errorf("rollouts_one_active_per_group: %q err=%v", def, err)
	}
	var nullable string
	if err := db.QueryRow(ctx, `select is_nullable from information_schema.columns
		where table_name = 'config_versions' and column_name = 'snapshot'`).Scan(&nullable); err != nil || nullable != "YES" {
		t.Errorf("config_versions.snapshot nullable = %q err=%v", nullable, err)
	}

	// Positive path first: a valid canary configuration is accepted.
	if _, err := db.Exec(ctx, `update engine_groups set rollout_strategy = 'canary', canary_count = 1 where id = $1`, store.DefaultEngineGroupID); err != nil {
		t.Fatalf("valid canary configuration rejected: %v", err)
	}
	if _, err := db.Exec(ctx, `update engine_groups set canary_count = 0, canary_percent = 0 where id = $1`, store.DefaultEngineGroupID); err == nil {
		t.Fatal("canary strategy without a canary size was accepted")
	}
	if _, err := db.Exec(ctx, `insert into engine_groups (name) values ('Bad_Name')`); err == nil {
		t.Fatal("engine group name outside [a-z0-9-] was accepted")
	}
	if _, err := db.Exec(ctx, `insert into policy_groups (name) values ('p1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `insert into rewrites (group_id, engine_group_id, name, type, value)
		select id, $1, 'a.test', 'A', '192.0.2.1' from policy_groups where name = 'p1'`, store.DefaultEngineGroupID); err == nil {
		t.Fatal("a policy group rewrite with its own engine_group_id was accepted")
	}
}

func TestFleetMigrationBackfillsCertificatesAndDownUp(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	if err := st.MigrateDownTo(ctx, 499); err != nil {
		t.Fatalf("down to 499: %v", err)
	}
	if _, err := st.Pool.Exec(ctx, `insert into engines (node_name, certificate_serial, enrolled_at)
		values ('old-engine', 'a1b2', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	var notAfter, group string
	if err := st.Pool.QueryRow(ctx, `select c.not_after::date::text, e.engine_group_id::text from engine_certificates c
		join engines e on e.id = c.engine_id where c.serial = 'a1b2'`).Scan(&notAfter, &group); err != nil {
		t.Fatalf("backfilled certificate: %v", err)
	}
	if notAfter != "2027-01-01" || group != store.DefaultEngineGroupID.String() {
		t.Fatalf("backfill not_after %s group %s", notAfter, group)
	}
}

// TestFleetMigrationPopulatedM4Database migrates a database holding M1-M4 rows (engines, join
// tokens, scoped configuration and published versions) and checks every backfill.
func TestFleetMigrationPopulatedM4Database(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	db := st.Pool
	if err := st.MigrateDownTo(ctx, 403); err != nil {
		t.Fatalf("down to 403: %v", err)
	}
	for _, q := range []string{
		`insert into join_tokens (name, secret_hash, created_by, expires_at) values ('kw', '\x01', 'admin', now() + interval '1 day')`,
		`insert into engines (node_name, join_token_id, certificate_serial, enrolled_at, applied_version, deleted_at)
			select 'gone', id, 'C0FFEE', '2026-02-01T00:00:00Z', 2, '2026-03-01T00:00:00Z' from join_tokens`,
		`insert into engines (node_name, join_token_id, certificate_serial, applied_version)
			select 'live', id, 'beef01', 2 from join_tokens`,
		`insert into upstreams (name, protocol, address, position) values ('u1', 'udp', '192.0.2.53:53', 0)`,
		`insert into policy_groups (name) values ('office')`,
		`insert into rewrites (name, type, value) values ('global.test', 'A', '192.0.2.1')`,
		`insert into rewrites (group_id, name, type, value) select id, 'office.test', 'A', '192.0.2.2' from policy_groups`,
		`insert into config_versions (version, created_by, snapshot) values (1, 'system', '\x0801'), (2, 'admin', '\x0802')`,
	} {
		if _, err := db.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate populated M4 database: %v", err)
	}

	var engines, defaultEngines int
	if err := db.QueryRow(ctx, `select count(*), count(*) filter (where engine_group_id = $1 and labels = '{}'::jsonb
		and revision = 1 and revoked_at is null) from engines`, store.DefaultEngineGroupID).Scan(&engines, &defaultEngines); err != nil {
		t.Fatal(err)
	}
	if engines != 2 || defaultEngines != 2 {
		t.Fatalf("engines %d, in default group %d; want 2 and 2", engines, defaultEngines)
	}
	var tokenGroup string
	var maxUses *int32
	if err := db.QueryRow(ctx, `select engine_group_id::text, max_uses from join_tokens`).Scan(&tokenGroup, &maxUses); err != nil ||
		tokenGroup != store.DefaultEngineGroupID.String() || maxUses != nil {
		t.Fatalf("join token group %s max_uses %v err %v", tokenGroup, maxUses, err)
	}
	for _, table := range store.EngineScopedTables {
		var scoped int
		if err := db.QueryRow(ctx, `select count(*) from `+table+` where engine_group_id is not null`).Scan(&scoped); err != nil || scoped != 0 {
			t.Errorf("%s rows with an engine group after backfill: %d err %v", table, scoped, err)
		}
	}

	var gsVersion int64
	var sha string
	if err := db.QueryRow(ctx, `select version, content_sha256 from group_snapshots where engine_group_id = $1`,
		store.DefaultEngineGroupID).Scan(&gsVersion, &sha); err != nil || gsVersion != 2 || len(sha) != 64 {
		t.Fatalf("group snapshot version %d sha %q err %v; want only version 2", gsVersion, sha, err)
	}
	var state, kind, strategy string
	if err := db.QueryRow(ctx, `select state, kind, strategy from rollouts where engine_group_id = $1 and version = 2`,
		store.DefaultEngineGroupID).Scan(&state, &kind, &strategy); err != nil ||
		state != "completed" || kind != "change" || strategy != "all_at_once" {
		t.Fatalf("backfilled rollout %s/%s/%s err %v", state, kind, strategy, err)
	}
	var stable *int64
	if err := db.QueryRow(ctx, `select stable_version from engine_groups where id = $1`, store.DefaultEngineGroupID).Scan(&stable); err != nil ||
		stable == nil || *stable != 2 {
		t.Fatalf("default stable_version %v err %v; want 2", stable, err)
	}

	type cert struct {
		serial, notBefore, notAfter string
		reason                      *string
	}
	certs := map[string]cert{}
	rows, err := db.Query(ctx, `select e.node_name, c.serial, (c.not_before at time zone 'UTC')::date::text, (c.not_after at time zone 'UTC')::date::text, c.revoke_reason
		from engine_certificates c join engines e on e.id = c.engine_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var node string
		var c cert
		if err := rows.Scan(&node, &c.serial, &c.notBefore, &c.notAfter, &c.reason); err != nil {
			t.Fatal(err)
		}
		certs[node] = c
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	gone, live := certs["gone"], certs["live"]
	if gone.serial != "c0ffee" || gone.notBefore != "2026-01-31" || gone.notAfter != "2027-02-01" || gone.reason == nil || *gone.reason != "revoked" {
		t.Errorf("deleted engine certificate %+v", gone)
	}
	if live.serial != "beef01" || live.reason != nil {
		t.Errorf("live engine certificate %+v", live)
	}

	// Existing M1-M4 writes keep working without naming an engine group.
	if _, err := db.Exec(ctx, `insert into upstreams (name, protocol, address, position) values ('u2', 'udp', '192.0.2.54:53', 1)`); err != nil {
		t.Fatalf("M4-style upstream insert after migration: %v", err)
	}
	if _, err := db.Exec(ctx, `insert into config_versions (version, created_by) values (3, 'admin')`); err != nil {
		t.Fatalf("version without a global snapshot: %v", err)
	}

	// The down migration restores the M4 schema on the same data (dropping snapshot-less versions).
	if err := st.MigrateDownTo(ctx, 403); err != nil {
		t.Fatalf("down again: %v", err)
	}
	var n int
	if err := db.QueryRow(ctx, `select count(*) from engines`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("engines after down: %d err %v", n, err)
	}
}
