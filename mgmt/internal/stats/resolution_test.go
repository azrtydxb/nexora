package stats_test

import (
	"context"
	"errors"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/e2e/harness"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/stats"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

func TestRecordM3UpsertsStatusAndSkipsUnknownZones(t *testing.T) {
	ctx := context.Background()
	pg := harness.New(t).StartPostgres()
	st, err := store.Open(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var engineID string
	if err := st.Pool.QueryRow(ctx, "insert into engines(node_name, certificate_serial) values ('e1', '01') returning id::text").Scan(&engineID); err != nil {
		t.Fatal(err)
	}
	var zone store.RPZZone
	if err := st.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		zone, err = store.CreateRPZZone(ctx, tx, store.RPZZone{Name: "rpz.file.test.", SourceType: "file", MinRefreshSeconds: 60, PolicyOverride: "given"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	s := &controlv1.Stats{
		Dnssec:   &controlv1.DnssecStats{Secure: 4, TrustAnchors: []*controlv1.TrustAnchorStatus{{Zone: ".", KeyTag: 20326, Algorithm: 8, State: controlv1.TrustAnchorState_TRUST_ANCHOR_STATE_VALID}}},
		RpzZones: []*controlv1.RpzZoneStatus{{Id: zone.ID.String(), Serial: 7, Records: 3}, {Id: uuid.NewString(), Serial: 9}, {Id: "not-a-uuid"}},
	}
	for i := 0; i < 2; i++ {
		if err := stats.RecordM3(ctx, st, engineID, s); err != nil {
			t.Fatal(err)
		}
	}
	dnssec, err := store.ListEngineDnssecStatus(ctx, st.Pool)
	if err != nil || len(dnssec) != 1 || dnssec[0].EngineName != "e1" {
		t.Fatalf("dnssec status = %v %v", dnssec, err)
	}
	byZone, err := store.ListRPZEngineStatus(ctx, st.Pool)
	if err != nil || len(byZone) != 1 || len(byZone[zone.ID]) != 1 || byZone[zone.ID][0].Serial != 7 {
		t.Fatalf("rpz status = %v %v", byZone, err)
	}
}

// Both DNSSEC and M8 ZONEMD reports must use the caller's fenced transaction.
// A pool write here would survive rollback (and deadlock with a one-slot pool).
func TestRecordM3ZonemdUsesCallerTransaction(t *testing.T) {
	st := storetest.New(t)
	engine := storetest.InsertEngine(t, st, "zmd-tx", store.DefaultEngineGroupID)
	// The budget covers the queries below only: starting PostgreSQL and migrating it takes
	// most of 15s on a loaded CI runner under -race.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var id uuid.UUID
	if err := st.Pool.QueryRow(ctx, `insert into rpz_zones(name, position, source_type, primary_address) values ('zmd-tx.test.', 1, 'transfer', '127.0.0.1:53') returning id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("ownership transaction rejected")
	report := &controlv1.Stats{Dnssec: &controlv1.DnssecStats{Secure: 9}, RpzZones: []*controlv1.RpzZoneStatus{{Id: id.String(), Serial: 42, Zonemd: controlv1.ZonemdStatus_ZONEMD_STATUS_FAILED, ZonemdError: "digest mismatch"}}}
	err := st.InTx(ctx, func(tx pgx.Tx) error {
		if err := stats.RecordM3WithQuerier(ctx, tx, engine.String(), report); err != nil {
			return err
		}
		var status, detail string
		if err := tx.QueryRow(ctx, `select zonemd, zonemd_error from engine_rpz_status where engine_id=$1 and rpz_zone_id=$2`, engine, id).Scan(&status, &detail); err != nil {
			return err
		}
		if status != "failed" || detail != "digest mismatch" {
			t.Fatalf("status=%q error=%q", status, detail)
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	for _, table := range []string{"engine_rpz_status", "engine_dnssec_status"} {
		var count int
		if err := st.Pool.QueryRow(ctx, `select count(*) from `+pgx.Identifier{table}.Sanitize()+` where engine_id=$1`, engine).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s rollback count=%d err=%v", table, count, err)
		}
	}
}
