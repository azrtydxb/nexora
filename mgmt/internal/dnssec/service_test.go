package dnssec_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/piwi3910/nexora/mgmt/internal/dnssec"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

func keysIn(v *dnssec.View, role, state string) []dnssec.KeyView {
	var out []dnssec.KeyView
	for _, k := range v.Keys {
		if k.Role == role && k.State == state {
			out = append(out, k)
		}
	}
	return out
}

func TestServiceRolloversAreScheduledAndGuarded(t *testing.T) {
	ctx := context.Background()
	box := kekBox(t)
	zs, z := signedZone(t, box, dnssec.Settings{NSECMode: "nsec3", KeyBackend: secrets.BackendKEK, PropagationDelay: time.Minute, ParentDSTTL: time.Hour})
	svc := &dnssec.Service{Store: zs.Store, Box: box, Zones: zs}
	v, err := svc.Get(ctx, z.ID)
	if err != nil {
		t.Fatal(err)
	}
	ksk := keysIn(v, "ksk", "active")
	if !v.Enabled || len(ksk) != 1 || len(keysIn(v, "zsk", "active")) != 1 || len(v.DS) != 1 || len(v.DNSKEYs) != 2 {
		t.Fatalf("initial view: %+v", v)
	}

	cur, _ := zs.GetZone(ctx, z.ID)
	if _, err := svc.Update(ctx, actor, z.ID, cur.Revision, true, dnssec.Settings{Algorithm: 8, NSECMode: "nsec3", KeyBackend: secrets.BackendKEK,
		PropagationDelay: time.Minute, ParentDSTTL: time.Hour}); !errors.Is(err, dnssec.ErrAlgorithmRollover) {
		t.Fatalf("algorithm change on a signed zone: %v", err)
	}
	if _, err := svc.Update(ctx, actor, z.ID, cur.Revision-1, true, v.Settings); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale revision: %v", err)
	}

	v, err = svc.StartRollover(ctx, actor, z.ID, "zsk")
	if err != nil {
		t.Fatal(err)
	}
	published := keysIn(v, "zsk", "published")
	if len(published) != 1 || len(v.DNSKEYs) != 3 {
		t.Fatalf("ZSK rollover start: %+v", v.Keys)
	}
	if _, err := svc.StartRollover(ctx, actor, z.ID, "zsk"); !errors.Is(err, dnssec.ErrRolloverInProgress) {
		t.Fatalf("second ZSK rollover: %v", err)
	}
	var next time.Time
	if err := zs.Store.Pool.QueryRow(ctx, "SELECT next_maintenance_at FROM zone_dnssec WHERE zone_id = $1", z.ID).Scan(&next); err != nil {
		t.Fatal(err)
	}
	// SOA TTL defaults to 3600 s: activation is due at publication + DNSKEY TTL + propagation delay.
	if want := published[0].PublishedAt.Add(time.Hour + time.Minute); !next.Equal(want) {
		t.Fatalf("next maintenance %v, want the ZSK activation at %v", next, want)
	}

	if _, err := svc.ConfirmDS(ctx, actor, z.ID, uuid.MustParse(published[0].ID)); !errors.Is(err, dnssec.ErrNotPendingKSK) {
		t.Fatalf("confirm DS of a ZSK: %v", err)
	}
	v, err = svc.StartRollover(ctx, actor, z.ID, "ksk")
	if err != nil {
		t.Fatal(err)
	}
	if len(keysIn(v, "ksk", "active")) != 2 {
		t.Fatalf("KSK rollover start: %+v", v.Keys)
	}
	if _, err := svc.StartRollover(ctx, actor, z.ID, "ksk"); !errors.Is(err, dnssec.ErrRolloverInProgress) {
		t.Fatalf("third KSK: %v", err)
	}
	var newKSK dnssec.KeyView
	for _, k := range keysIn(v, "ksk", "active") {
		if k.ID != ksk[0].ID {
			newKSK = k
		}
	}
	if v, err = svc.ConfirmDS(ctx, actor, z.ID, uuid.MustParse(newKSK.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfirmDS(ctx, actor, z.ID, uuid.MustParse(newKSK.ID)); !errors.Is(err, dnssec.ErrNotPendingKSK) {
		t.Fatalf("confirming twice: %v", err)
	}
	servedVerified(t, zs, z.ID, time.Now())

	if _, err := svc.StartRollover(ctx, actor, z.ID, "csk"); !errors.Is(err, dnssec.ErrInvalidSettings) {
		t.Fatalf("unknown role: %v", err)
	}
}
