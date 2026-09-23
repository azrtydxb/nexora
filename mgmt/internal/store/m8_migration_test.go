package store_test

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/migrations"
)

// An install at the M7 head keeps its published behaviour after the M8 migrations: no ZONEMD
// generation, no verification of existing transfers, no mDNS and ODoH off. Only rows created after
// the upgrade default to zonemd_verify=if_present.
func TestM8MigrationKeepsBehaviour(t *testing.T) {
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
	if _, err := provider.UpTo(ctx, 899); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`insert into zones (name, kind, soa_mname, soa_rname) values ('p.test.', 'primary', 'ns.p.test.', 'h.p.test.')`,
		`insert into zones (name, kind, soa_mname, soa_rname, primaries) values ('s.test.', 'secondary', '', '', '[{"address":"127.0.0.1:53","tsig_key_id":null}]')`,
		`insert into rpz_zones (name, position, source_type, primary_address) values ('rpz.test.', 1, 'transfer', '127.0.0.1:53')`,
		`insert into engine_groups (name) values ('edge')`,
	} {
		if _, err := st.Pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var gen int
	var verify []string
	if err := st.Pool.QueryRow(ctx, `select count(*) filter (where zonemd_generate), array_agg(distinct zonemd_verify) from zones`).Scan(&gen, &verify); err != nil {
		t.Fatal(err)
	}
	if gen != 0 || len(verify) != 1 || verify[0] != "off" {
		t.Fatalf("existing zones: generate=%d verify=%v, want 0 and [off]", gen, verify)
	}
	var rpz string
	if err := st.Pool.QueryRow(ctx, `select zonemd_verify from rpz_zones`).Scan(&rpz); err != nil || rpz != "off" {
		t.Fatalf("existing rpz zonemd_verify %q, want off: %v", rpz, err)
	}
	// Assert the actual engine configuration before adding post-upgrade rows.
	m8AssertMigratedSnapshot(t, st)
	// New rows default to if_present.
	var newZone, newRPZ string
	if err := st.Pool.QueryRow(ctx, `insert into zones (name, kind, soa_mname, soa_rname, primaries) values ('new.test.', 'secondary', '', '', '[{"address":"127.0.0.1:53","tsig_key_id":null}]') returning zonemd_verify`).Scan(&newZone); err != nil || newZone != "if_present" {
		t.Fatalf("new zone zonemd_verify %q, want if_present: %v", newZone, err)
	}
	if err := st.Pool.QueryRow(ctx, `insert into rpz_zones (name, position, source_type, primary_address) values ('rpz.new.', 2, 'transfer', '127.0.0.1:53') returning zonemd_verify`).Scan(&newRPZ); err != nil || newRPZ != "if_present" {
		t.Fatalf("new rpz zonemd_verify %q, want if_present: %v", newRPZ, err)
	}
	var mdns int
	if err := st.Pool.QueryRow(ctx, `select count(*) from engine_groups where mdns_enabled or mdns_reflect`).Scan(&mdns); err != nil || mdns != 0 {
		t.Fatalf("mdns groups %d: %v", mdns, err)
	}
	var rows int
	var target, proxy bool
	if err := st.Pool.QueryRow(ctx, `select count(*), bool_or(target_enabled), bool_or(proxy_enabled) from odoh_settings`).Scan(&rows, &target, &proxy); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || target || proxy {
		t.Fatalf("odoh_settings rows=%d target=%v proxy=%v", rows, target, proxy)
	}
	// The generate flag is accepted on a primary (positive path) and refused on a secondary.
	if _, err := st.Pool.Exec(ctx, `update zones set zonemd_generate = true where name = 'p.test.'`); err != nil {
		t.Fatalf("a primary zone refused zonemd_generate: %v", err)
	}
	if _, err := st.Pool.Exec(ctx, `update zones set zonemd_generate = true where name = 's.test.'`); err == nil {
		t.Fatal("a secondary zone accepted zonemd_generate")
	}
}

// Exercise the actual baseline schema and preserve whole rows, including stream
// identity, immutable snapshots, installation identity and failover reservations.
func TestM8BaselineUpgrade(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.MigrateDownTo(ctx, 1301); err != nil {
		t.Fatal(err)
	}
	a := storetest.InsertEngine(t, st, "m8-a", store.DefaultEngineGroupID)
	b := storetest.InsertEngine(t, st, "m8-b", store.DefaultEngineGroupID)
	if _, err := st.Pool.Exec(ctx, `update engines set connection_session = gen_random_uuid()`); err != nil {
		t.Fatal(err)
	}
	pair := uuid.New()
	// This is the actual 1301 schema: current CRUD requires newer reservation
	// tables and must not be used to seed a historical installation.
	if err := st.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into failover_groups(id,name,frontend_ip,member_a,member_b) values($1,'m8-pair','192.0.2.136',$2,$3)`, pair, a, b); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `insert into failover_members(group_id,engine_id,slot) values($1,$2,1),($1,$3,2)`, pair, a, b)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `insert into config_versions(version, created_by) values (987, 'm8-upgrade-test')`); err != nil {
		t.Fatal(err)
	}
	tables := []string{"engines", "installation", "config_versions", "failover_groups", "failover_members", "resolver_settings"}
	columns := make(map[string][]string)
	for _, table := range tables {
		var names []string
		if err := st.Pool.QueryRow(ctx, `select array_agg(attname::text order by attnum) from pg_catalog.pg_attribute where attrelid=$1::regclass and attnum>0 and not attisdropped`, table).Scan(&names); err != nil {
			t.Fatal(err)
		}
		columns[table] = names
	}
	before := m8LegacyRows(t, st, tables, columns)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if after := m8LegacyRows(t, st, tables, columns); !reflect.DeepEqual(before, after) {
		t.Fatalf("baseline rows changed: before=%v after=%v", before, after)
	}
	var lifecycle string
	var ips, members int
	if err := st.Pool.QueryRow(ctx, `select lifecycle,
	 (select count(*) from failover_ip_reservations where group_id=$1 and frontend_ip='192.0.2.136'),
	 (select count(*) from failover_engine_reservations where group_id=$1 and engine_id in ($2,$3))
	 from failover_groups where id=$1`, pair, a, b).Scan(&lifecycle, &ips, &members); err != nil || lifecycle != "active" || ips != 1 || members != 2 {
		t.Fatalf("additive lifecycle/reservations: %q %d %d: %v", lifecycle, ips, members, err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
}

// Compare every historical column, including nulls, without rejecting additive
// migrations. Other history-refusal tests retain full-row comparisons via m8Rows.
func m8LegacyRows(t *testing.T, st *store.Store, tables []string, columns map[string][]string) []string {
	t.Helper()
	var out []string
	for _, table := range tables {
		var rows string
		if err := st.Pool.QueryRow(context.Background(), `select coalesce(jsonb_agg(v order by v::text), '[]'::jsonb)::text from
		 (select (select jsonb_object_agg(key,value) from jsonb_each(to_jsonb(r)) where key=any($1::text[])) v from `+pgx.Identifier{table}.Sanitize()+` r) s`, columns[table]).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		out = append(out, rows)
	}
	return out
}

func m8Rows(t *testing.T, st *store.Store, tables []string) []string {
	t.Helper()
	var out []string
	for _, table := range tables {
		var rows string
		// table names are fixed test constants, never request input.
		if err := st.Pool.QueryRow(context.Background(), `select coalesce(jsonb_agg(v order by v::text), '[]'::jsonb)::text from (select to_jsonb(r) v from `+pgx.Identifier{table}.Sanitize()+` r) s`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		out = append(out, rows)
	}
	return out
}

func TestM8FreshInstall(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	var target, proxy bool
	if err := st.Pool.QueryRow(ctx, `select target_enabled, proxy_enabled from odoh_settings`).Scan(&target, &proxy); err != nil || target || proxy {
		t.Fatalf("ODoH defaults: %t %t %v", target, proxy, err)
	}
	var versions int
	if err := st.Pool.QueryRow(ctx, `select count(*) from goose_db_version where version_id between 1300 and 1305 and is_applied`).Scan(&versions); err != nil || versions != 6 {
		t.Fatalf("versions=%d: %v", versions, err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
}

// Reproduce each historical M8 prefix using the original version mapping,
// including partial installs. Refusal must leave both data and history intact.
func TestM8RejectsLegacyHistory(t *testing.T) {
	for _, version := range []int64{1300, 1301, 1302, 1303} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			pg := harness.New(t).StartPostgres()
			ctx := context.Background()
			st, err := store.Open(ctx, pg.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			legacy := fstest.MapFS{}
			entries, err := fs.ReadDir(migrations.FS, ".")
			if err != nil {
				t.Fatal(err)
			}
			renamed := map[string]string{"01302_zonemd.sql": "01300_zonemd.sql", "01303_catalog_zones.sql": "01301_catalog_zones.sql", "01304_odoh.sql": "01302_odoh.sql", "01305_engine_group_mdns.sql": "01303_engine_group_mdns.sql"}
			for _, entry := range entries {
				name := entry.Name()
				if name == "01300_failover_groups.sql" || name == "01301_engine_connection_session.sql" {
					continue
				}
				data, err := fs.ReadFile(migrations.FS, name)
				if err != nil {
					t.Fatal(err)
				}
				if old, ok := renamed[name]; ok {
					name = old
				}
				legacy[name] = &fstest.MapFile{Data: data}
			}
			db := stdlib.OpenDBFromPool(st.Pool)
			defer db.Close()
			provider, err := goose.NewProvider(goose.DialectPostgres, db, legacy)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provider.UpTo(ctx, version); err != nil {
				t.Fatal(err)
			}
			tables := []string{"goose_db_version", "installation", "zones", "engine_groups"}
			before := m8Rows(t, st, tables)
			if err := st.Migrate(ctx); err == nil || !strings.Contains(err.Error(), "unsupported legacy M8") {
				t.Fatalf("legacy migration: %v", err)
			}
			if after := m8Rows(t, st, tables); !reflect.DeepEqual(before, after) {
				t.Fatal("refused upgrade mutated data/history")
			}
		})
	}
}
