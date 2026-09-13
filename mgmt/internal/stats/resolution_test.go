package stats_test

import (
	"context"
	"testing"

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
