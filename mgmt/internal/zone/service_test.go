package zone_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/nzf"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

var actor = auth.Actor{Type: "user", ID: "test", Name: "test"}

func newService(t *testing.T) *zone.Service {
	return &zone.Service{Store: storetest.New(t), Now: time.Now}
}

// published returns the number of config versions and the audit actions in order.
func published(t *testing.T, s *zone.Service) (int, []string) {
	t.Helper()
	ctx := context.Background()
	var versions int
	if err := s.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM config_versions`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	var actions []string
	if err := s.Store.Pool.QueryRow(ctx, `SELECT coalesce(array_agg(action ORDER BY id), '{}') FROM audit_log WHERE target_type = 'zone'`).Scan(&actions); err != nil {
		t.Fatal(err)
	}
	return versions, actions
}

func createZone(t *testing.T, s *zone.Service, name string) *zone.Zone {
	t.Helper()
	z, err := s.CreateZone(context.Background(), actor, zone.CreateZoneInput{
		Name: name, Kind: "primary", DefaultTTL: 300,
		SOA:         zone.SOA{MName: "ns1." + name, RName: "hostmaster." + name},
		Nameservers: []string{"ns1." + name},
	})
	if err != nil {
		t.Fatalf("CreateZone: %v", err)
	}
	return z
}

func TestCreateRecordBumpsSerialJournalAndPublishes(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	z := createZone(t, s, "unit.test.")
	if z.Serial != 1 || z.CurrentSeq != 1 || z.ImageSeq != 1 {
		t.Fatalf("new zone: serial=%d seq=%d image=%d", z.Serial, z.CurrentSeq, z.ImageSeq)
	}
	versionsBefore, _ := published(t, s)
	if _, err := s.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "www.unit.test.", Type: "A", TTL: 300, Data: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	z2, _ := s.GetZone(ctx, z.ID)
	if z2.Serial != 2 || z2.CurrentSeq != 2 || z2.Revision <= z.Revision {
		t.Fatalf("after record: serial=%d seq=%d revision %d->%d", z2.Serial, z2.CurrentSeq, z.Revision, z2.Revision)
	}
	var from, to int64
	var blob []byte
	err := s.Store.Pool.QueryRow(ctx, `SELECT j.from_serial, j.to_serial, b.data FROM zone_journal j JOIN blobs b ON b.sha256 = j.blob_sha256 WHERE j.zone_id=$1 AND j.seq=2`, z.ID).Scan(&from, &to, &blob)
	if err != nil || from != 1 || to != 2 {
		t.Fatalf("journal row: %d->%d err=%v", from, to, err)
	}
	raw, err := nzf.Decompress(blob, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	_, _, d, err := nzf.Decode(raw)
	if err != nil || len(d.Added) != 2 || d.Added[0].Type != dns.TypeSOA || d.Added[1].Type != dns.TypeA {
		t.Fatalf("delta: %+v err=%v", d, err)
	}
	versions, actions := published(t, s)
	if versions != versionsBefore+1 || len(actions) != 2 || actions[0] != "createZone" || actions[1] != "createZoneRecord" {
		t.Fatalf("config versions %d -> %d, audit=%v", versionsBefore, versions, actions)
	}
}

func TestStaleRecordRevisionConflicts(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	z := createZone(t, s, "conflict.test.")
	other := auth.Actor{Type: "user", ID: "other", Name: "other"}
	r, err := s.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "www.conflict.test.", Type: "A", TTL: 300, Data: "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateRecord(ctx, actor, z.ID, r.ID, r.Revision, zone.RecordInput{Name: "www.conflict.test.", Type: "A", TTL: 300, Data: "192.0.2.2"}); err != nil {
		t.Fatalf("first update: %v", err)
	}
	_, err = s.UpdateRecord(ctx, other, z.ID, r.ID, r.Revision, zone.RecordInput{Name: "www.conflict.test.", Type: "A", TTL: 300, Data: "192.0.2.3"})
	if !errors.Is(err, zone.ErrConflict) {
		t.Fatalf("stale update: got %v, want ErrConflict", err)
	}
	if err := s.DeleteRecord(ctx, other, z.ID, r.ID, r.Revision); !errors.Is(err, zone.ErrConflict) {
		t.Fatalf("stale delete: got %v", err)
	}
}

func TestValidationRules(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	z := createZone(t, s, "rules.test.")
	mustCode := func(in zone.RecordInput, code string) {
		t.Helper()
		_, err := s.CreateRecord(ctx, actor, z.ID, in)
		var ve *zone.ValidationError
		if !errors.As(err, &ve) || ve.Code != code {
			t.Fatalf("%+v: got %v, want %s", in, err, code)
		}
	}
	if _, err := s.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "www.rules.test.", Type: "A", TTL: 300, Data: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	mustCode(zone.RecordInput{Name: "www.rules.test.", Type: "CNAME", TTL: 300, Data: "other.example."}, "cname_conflict")
	mustCode(zone.RecordInput{Name: "www.example.org.", Type: "A", TTL: 300, Data: "192.0.2.1"}, "out_of_zone")
	mustCode(zone.RecordInput{Name: "x.rules.test.", Type: "HINFO", TTL: 300, Data: "a b"}, "unsupported_type")
	mustCode(zone.RecordInput{Name: "rules.test.", Type: "SOA", TTL: 300, Data: "a. b. 1 2 3 4 5"}, "unsupported_type")
	mustCode(zone.RecordInput{Name: "x.rules.test.", Type: "A", TTL: 300, Data: "not-an-ip"}, "invalid_rdata")
	recs, _, _ := s.ListRecords(ctx, z.ID, "rules.test.", "NS", "", 10)
	var ve *zone.ValidationError
	if err := s.DeleteRecord(ctx, actor, z.ID, recs[0].ID, recs[0].Revision); !errors.As(err, &ve) || ve.Code != "last_apex_ns" {
		t.Fatalf("deleting the last apex NS: got %v, want last_apex_ns", err)
	}
}

func TestSerialWrapsAroundRFC1982(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	z := createZone(t, s, "wrap.test.")
	if _, err := s.Store.Pool.Exec(ctx, `UPDATE zones SET serial = 4294967295 WHERE id = $1`, z.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "a.wrap.test.", Type: "A", TTL: 300, Data: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	z2, _ := s.GetZone(ctx, z.ID)
	if z2.Serial != 0 {
		t.Fatalf("serial after 4294967295 = %d, want 0", z2.Serial)
	}
	var from, to int64
	_ = s.Store.Pool.QueryRow(ctx, `SELECT from_serial, to_serial FROM zone_journal WHERE zone_id=$1 ORDER BY seq DESC LIMIT 1`, z.ID).Scan(&from, &to)
	if from != 4294967295 || to != 0 {
		t.Fatalf("journal %d->%d", from, to)
	}
}

func TestRebuildWithoutChangesKeepsSerial(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	z := createZone(t, s, "same.test.")
	tx, _ := s.Store.Pool.Begin(ctx)
	defer tx.Rollback(ctx)
	changed, err := zone.Rebuild(ctx, tx, nil, z, zone.RebuildOptions{}, time.Now())
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
}
