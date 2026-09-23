package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
	"google.golang.org/protobuf/proto"
)

func m8BuildSnapshot(t *testing.T, st *store.Store, group uuid.UUID) *controlv1.ConfigSnapshot {
	t.Helper()
	var snap *controlv1.ConfigSnapshot
	if err := st.InTx(context.Background(), func(tx pgx.Tx) (err error) {
		snap, err = snapshot.BuildForGroup(context.Background(), tx, 1, snapshot.BuildConfig{}, group)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return snap
}

// Called from the real M7 upgrade fixture, before inserting any new M8 rows.
func m8AssertMigratedSnapshot(t *testing.T, st *store.Store) {
	t.Helper()
	var edge uuid.UUID
	if err := st.Pool.QueryRow(context.Background(), `select id from engine_groups where name='edge'`).Scan(&edge); err != nil {
		t.Fatal(err)
	}
	for _, group := range []uuid.UUID{store.DefaultEngineGroupID, edge} {
		snap := m8BuildSnapshot(t, st, group)
		if err := m8SnapshotBehaviour(snap); err != nil {
			t.Fatal(err)
		}
	}
}

// Build real zone images before crossing the supported 1301 boundary, then
// compare every published field and all retained identities/images/records.
func TestM8UpgradePreservesPopulatedSnapshot(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	svc := &zone.Service{Store: st, Now: time.Now}
	actor := auth.Actor{Type: "user", ID: "migration-test", Name: "migration-test"}
	z, err := svc.CreateZone(ctx, actor, zone.CreateZoneInput{Name: "upgrade.test.", Kind: "primary", DefaultTTL: 60,
		SOA: zone.SOA{MName: "ns.upgrade.test.", RName: "h.upgrade.test."}, Nameservers: []string{"ns.upgrade.test."}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "www.upgrade.test.", Type: "A", TTL: 60, Data: "192.0.2.19"}); err != nil {
		t.Fatal(err)
	}
	// A loaded secondary has the same immutable image representation. Build a
	// genuine image, then model an already completed historical transfer without
	// starting a network transfer worker in this migration-only regression.
	secondary, err := svc.CreateZone(ctx, actor, zone.CreateZoneInput{Name: "secondary.upgrade.test.", Kind: "primary", DefaultTTL: 60,
		SOA: zone.SOA{MName: "ns.secondary.upgrade.test.", RName: "h.secondary.upgrade.test."}, Nameservers: []string{"ns.secondary.upgrade.test."}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateRecord(ctx, actor, secondary.ID, zone.RecordInput{Name: "www.secondary.upgrade.test.", Type: "A", TTL: 60, Data: "192.0.2.20"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `update zones set kind='secondary', loaded=true, primaries='[{"address":"192.0.2.53:53","tsig_key_id":null}]', zonemd_verify='off' where id=$1`, secondary.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `insert into rpz_zones(name,position,source_type,primary_address,zonemd_verify) values ('rpz.upgrade.test.',1,'transfer','192.0.2.53:53','off')`); err != nil {
		t.Fatal(err)
	}
	before := m8BuildSnapshot(t, st, store.DefaultEngineGroupID)
	if len(before.AuthZones) != 2 || before.AuthZones[0].Image == nil || len(before.AuthZones[0].Deltas) == 0 || len(before.RpzZones) != 1 {
		t.Fatal("empty migration fixture")
	}
	if before.AuthZones[0].Name != "secondary.upgrade.test." || before.AuthZones[0].Kind != controlv1.AuthZoneKind_AUTH_ZONE_KIND_SECONDARY || before.AuthZones[0].Primaries[0] != "192.0.2.53:53" {
		t.Fatal("missing loaded secondary fixture")
	}
	if err := st.MigrateDownTo(ctx, 1301); err != nil {
		t.Fatal(err)
	}
	tables := []string{"installation", "engines", "zone_records", "zone_images", "zone_journal", "blobs", "group_snapshots", "config_versions"}
	rows := m8Rows(t, st, tables)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for i, after := range m8Rows(t, st, tables) {
		if after != rows[i] {
			t.Errorf("upgrade changed %s", tables[i])
		}
	}
	after := m8BuildSnapshot(t, st, store.DefaultEngineGroupID)
	before.CreatedUnixMs, after.CreatedUnixMs = 0, 0
	if after.Mdns != nil || after.Odoh != nil || len(after.RpzZones) != 1 || after.RpzZones[0].GetTransfer().GetZonemdVerify() != controlv1.ZonemdVerify_ZONEMD_VERIFY_OFF {
		t.Fatal("upgrade activated M8 behavior")
	}
	if !proto.Equal(before, after) {
		t.Fatalf("effective snapshot changed:\nbefore %v\nafter %v", before, after)
	}
}

func m8SnapshotBehaviour(snap *controlv1.ConfigSnapshot) error {
	if snap.Mdns != nil || snap.Odoh != nil {
		return fmt.Errorf("upgrade enabled M8 roles: mdns=%v odoh=%v", snap.Mdns, snap.Odoh)
	}
	if len(snap.RpzZones) != 1 {
		return fmt.Errorf("RPZ count = %d, want existing transfer", len(snap.RpzZones))
	}
	rpz := snap.RpzZones[0]
	tr := rpz.GetTransfer()
	if rpz.Name != "rpz.test." || tr == nil || tr.Primary != "127.0.0.1:53" || tr.ZonemdVerify != controlv1.ZonemdVerify_ZONEMD_VERIFY_OFF {
		return fmt.Errorf("existing RPZ changed effective behavior: %v", rpz)
	}
	return nil
}

// Negative controls prove the post-upgrade assertion detects non-nil disabled
// sections, an omitted RPZ, and verification silently enabled for an old row.
func TestM8SnapshotBehaviourRejectsChangedDefaults(t *testing.T) {
	good := &controlv1.ConfigSnapshot{RpzZones: []*controlv1.RpzZone{{Name: "rpz.test.", Source: &controlv1.RpzZone_Transfer{Transfer: &controlv1.RpzTransferSource{Primary: "127.0.0.1:53", ZonemdVerify: controlv1.ZonemdVerify_ZONEMD_VERIFY_OFF}}}}}
	if err := m8SnapshotBehaviour(good); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*controlv1.ConfigSnapshot){
		"mdns-section": func(s *controlv1.ConfigSnapshot) { s.Mdns = &controlv1.MdnsConfig{} },
		"odoh-section": func(s *controlv1.ConfigSnapshot) { s.Odoh = &controlv1.OdohConfig{} },
		"missing-rpz":  func(s *controlv1.ConfigSnapshot) { s.RpzZones = nil },
		"enabled-verification": func(s *controlv1.ConfigSnapshot) {
			s.RpzZones[0].GetTransfer().ZonemdVerify = controlv1.ZonemdVerify_ZONEMD_VERIFY_IF_PRESENT
		},
		"changed-primary": func(s *controlv1.ConfigSnapshot) { s.RpzZones[0].GetTransfer().Primary = "192.0.2.53:53" },
	} {
		t.Run(name, func(t *testing.T) {
			broken := proto.Clone(good).(*controlv1.ConfigSnapshot)
			mutate(broken)
			if m8SnapshotBehaviour(broken) == nil {
				t.Fatal("changed migration behavior passed")
			}
		})
	}
}
