package snapshot_test

import (
	"context"
	"testing"
	"time"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

func TestAddAuthZonesListsImageAndContiguousDeltas(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	actor := auth.Actor{Type: "user", ID: "t", Name: "t"}
	s := &zone.Service{Store: st, Now: time.Now}
	z, err := s.CreateZone(ctx, actor, zone.CreateZoneInput{Name: "snap.test.", Kind: "primary", DefaultTTL: 300,
		SOA: zone.SOA{MName: "ns1.snap.test.", RName: "h.snap.test."}, Nameservers: []string{"ns1.snap.test."}})
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"192.0.2.1", "192.0.2.2", "192.0.2.3"} {
		if _, err := s.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "www.snap.test.", Type: "A", TTL: 300, Data: ip}); err != nil {
			t.Fatal(err)
		}
	}
	tx, _ := st.Pool.Begin(ctx)
	defer tx.Rollback(ctx)
	snap := &controlv1.ConfigSnapshot{}
	if err := snapshot.AddAuthZones(ctx, tx, snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.AuthZones) != 1 {
		t.Fatalf("zones: %d", len(snap.AuthZones))
	}
	az := snap.AuthZones[0]
	if az.Name != "snap.test." || az.Serial != 4 || az.ImageSerial != 1 || az.ImageDeltaOffset != 0 || len(az.Deltas) != 3 {
		t.Fatalf("auth zone: %+v", az)
	}
	for i, d := range az.Deltas {
		if d.FromSerial != uint32(i+1) || d.ToSerial != uint32(i+2) || len(d.Blob.Sha256) != 64 {
			t.Fatalf("delta %d: %+v", i, d)
		}
	}
	var stored int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM blobs WHERE sha256 = ANY($1)`, []string{az.Image.Sha256, az.Deltas[2].Blob.Sha256}).Scan(&stored); err != nil || stored != 2 {
		t.Fatalf("image and delta blobs in blobs: %d %v", stored, err)
	}
	_, latest, err := snapshot.Latest(ctx, tx)
	if err != nil || len(latest.AuthZones) != 1 || latest.AuthZones[0].Serial != 4 {
		t.Fatalf("published snapshot does not carry the zone: %v %v", latest.GetAuthZones(), err)
	}
}
