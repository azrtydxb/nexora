package zone_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/mgmt/internal/dnssec"
	"github.com/piwi3910/nexora/mgmt/internal/nzf"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
	"github.com/piwi3910/nexora/mgmt/internal/zonemd"
)

// TestRebuildAddsVerifiableZonemd catches a zonemd_generate zone whose served versions lack a
// ZONEMD that verifies against the served data, carry a stale serial or TTL, leave the ZONEMD
// unsigned or out of the NSEC bitmap, publish deltas that keep the old ZONEMD, or keep serving it
// once generation is turned off.
func TestRebuildAddsVerifiableZonemd(t *testing.T) {
	for _, signed := range []bool{false, true} {
		t.Run(fmt.Sprintf("signed=%v", signed), func(t *testing.T) {
			ctx := context.Background()
			svc, st := newZonemdService(t, signed)
			z := createPrimary(t, svc, "zmd.test.")
			if _, err := st.Pool.Exec(ctx, `update zones set zonemd_generate = true where id = $1`, z.ID); err != nil {
				t.Fatal(err)
			}
			if signed {
				enableSigning(t, svc, z.ID)
			}
			for i := 0; i < 2; i++ {
				if _, err := svc.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: fmt.Sprintf("r%d.zmd.test.", i), Type: "A", TTL: 60, Data: "192.0.2.1"}); err != nil {
					t.Fatal(err)
				}
				served := loadServed(t, st, z.ID)
				if r := zonemd.Verify("zmd.test.", served, zonemd.ModeRequired); r.Status != zonemd.StatusVerified {
					t.Fatalf("version %d: %+v", i, r)
				}
				soa, md := apexSOA(served), apexZONEMD(served)
				if md == nil || md.Serial != soa.Serial || md.Hash != zonemd.HashSHA384 || md.Hdr.Ttl != soa.Hdr.Ttl {
					t.Fatalf("version %d: ZONEMD %v SOA %v", i, md, soa)
				}
				if signed {
					if !nsecBitmapHas(served, "zmd.test.", dns.TypeZONEMD) {
						t.Fatal("apex NSEC bitmap lacks ZONEMD")
					}
					if !rrsigValidates(t, served, "zmd.test.", dns.TypeZONEMD) {
						t.Fatal("ZONEMD RRSIG missing or invalid")
					}
				}
			}
			deltaDeletesOldZonemd(t, st, z.ID)
			// Turning zonemd_generate off forces a rebuild, as UpdateZone does (Task 14).
			if _, err := svc.Mutate(ctx, z.ID, func(tx pgx.Tx, z *zone.Zone) (string, any, any, zone.RebuildOptions, error) {
				_, err := tx.Exec(ctx, `update zones set zonemd_generate = false where id = $1`, z.ID)
				return "updateZone", nil, nil, zone.RebuildOptions{Force: true}, err
			}, actor); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "off.zmd.test.", Type: "A", TTL: 60, Data: "192.0.2.2"}); err != nil {
				t.Fatal(err)
			}
			if apexZONEMD(loadServed(t, st, z.ID)) != nil {
				t.Fatal("ZONEMD kept after zonemd_generate was turned off")
			}
		})
	}
}

// newZonemdService returns a zone service on a fresh database, with a KEK-backed DNSSEC signer
// when signed.
func newZonemdService(t *testing.T, signed bool) (*zone.Service, *store.Store) {
	t.Helper()
	st := storetest.New(t)
	svc := &zone.Service{Store: st, Now: time.Now}
	if signed {
		kek := make([]byte, 32)
		if _, err := rand.Read(kek); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(t.TempDir(), "kek")
		if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(kek)), 0o600); err != nil {
			t.Fatal(err)
		}
		box, err := secrets.Open(secrets.Config{KEKFile: p})
		if err != nil {
			t.Fatal(err)
		}
		svc.Signer = &dnssec.Store{Box: box}
	}
	return svc, st
}

// createPrimary creates primary zone name with nameserver ns.<name> and one A record.
func createPrimary(t *testing.T, svc *zone.Service, name string) *zone.Zone {
	t.Helper()
	ctx := context.Background()
	z, err := svc.CreateZone(ctx, actor, zone.CreateZoneInput{Name: name, Kind: "primary", DefaultTTL: 300,
		SOA: zone.SOA{MName: "ns." + name, RName: "h." + name}, Nameservers: []string{"ns." + name}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "ns." + name, Type: "A", TTL: 300, Data: "192.0.2.53"}); err != nil {
		t.Fatal(err)
	}
	return z
}

// enableSigning turns on NSEC signing for zoneID with KEK keys, as the dnssec store tests do.
func enableSigning(t *testing.T, svc *zone.Service, zoneID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	box := svc.Signer.(*dnssec.Store).Box
	if _, err := svc.Mutate(ctx, zoneID, func(tx pgx.Tx, z *zone.Zone) (string, any, any, zone.RebuildOptions, error) {
		return "updateZoneDnssec", nil, nil, zone.RebuildOptions{Force: true},
			dnssec.Enable(ctx, tx, box, z.ID, dnssec.Settings{NSECMode: "nsec", KeyBackend: secrets.BackendKEK, ZSKLifetimeDays: 90})
	}, actor); err != nil {
		t.Fatal(err)
	}
}

// loadServed returns the served RR set of zoneID, read in a transaction that is rolled back.
func loadServed(t *testing.T, st *store.Store, zoneID uuid.UUID) []dns.RR {
	t.Helper()
	ctx := context.Background()
	z, err := (&zone.Service{Store: st}).GetZone(ctx, zoneID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := st.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	served, err := zone.LoadServed(ctx, tx, z)
	if err != nil {
		t.Fatal(err)
	}
	return served
}

func apexSOA(rrs []dns.RR) *dns.SOA {
	for _, rr := range rrs {
		if s, ok := rr.(*dns.SOA); ok {
			return s
		}
	}
	return nil
}

func apexZONEMD(rrs []dns.RR) *dns.ZONEMD {
	for _, rr := range rrs {
		if z, ok := rr.(*dns.ZONEMD); ok && dns.CanonicalName(z.Hdr.Name) == "zmd.test." {
			return z
		}
	}
	return nil
}

func nsecBitmapHas(rrs []dns.RR, owner string, rtype uint16) bool {
	for _, rr := range rrs {
		if n, ok := rr.(*dns.NSEC); ok && dns.CanonicalName(n.Hdr.Name) == owner {
			for _, b := range n.TypeBitMap {
				if b == rtype {
					return true
				}
			}
		}
	}
	return false
}

// rrsigValidates reports whether an RRSIG over the owner/rtype RRset verifies with an apex ZSK
// DNSKEY and is valid now.
func rrsigValidates(t *testing.T, rrs []dns.RR, owner string, rtype uint16) bool {
	t.Helper()
	var set []dns.RR
	var zsks []*dns.DNSKEY
	var sigs []*dns.RRSIG
	for _, rr := range rrs {
		name := dns.CanonicalName(rr.Header().Name)
		switch v := rr.(type) {
		case *dns.DNSKEY:
			if name == owner && v.Flags == 256 {
				zsks = append(zsks, v)
			}
		case *dns.RRSIG:
			if name == owner && v.TypeCovered == rtype {
				sigs = append(sigs, v)
			}
		}
		if name == owner && rr.Header().Rrtype == rtype {
			set = append(set, rr)
		}
	}
	if len(sigs) == 0 || len(set) == 0 {
		return false
	}
	for _, s := range sigs {
		ok := false
		for _, k := range zsks {
			if k.KeyTag() == s.KeyTag && s.Verify(k, set) == nil && s.ValidityPeriod(time.Now()) {
				ok = true
			}
		}
		if !ok {
			t.Logf("RRSIG %s does not verify", s)
			return false
		}
	}
	return true
}

// deltaDeletesOldZonemd checks that the newest journal delta of zoneID deletes the previous
// version's ZONEMD and adds a different one.
func deltaDeletesOldZonemd(t *testing.T, st *store.Store, zoneID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var data []byte
	if err := st.Pool.QueryRow(ctx, `SELECT b.data FROM zone_journal j JOIN blobs b ON b.sha256 = j.blob_sha256
		WHERE j.zone_id = $1 ORDER BY j.seq DESC LIMIT 1`, zoneID).Scan(&data); err != nil {
		t.Fatal(err)
	}
	raw, err := nzf.Decompress(data, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	_, _, d, err := nzf.Decode(raw)
	if err != nil || d == nil {
		t.Fatalf("newest journal blob: delta %v, err %v", d, err)
	}
	zonemds := func(rs []nzf.Record) []string {
		var out []string
		for _, r := range rs {
			if r.Type == dns.TypeZONEMD {
				out = append(out, fmt.Sprintf("%x", r.RData))
			}
		}
		return out
	}
	del, add := zonemds(d.Deleted), zonemds(d.Added)
	if len(del) != 1 || len(add) != 1 || del[0] == add[0] {
		t.Fatalf("delta %d->%d: deleted ZONEMD %v, added ZONEMD %v", d.FromSerial, d.ToSerial, del, add)
	}
	if !strings.HasPrefix(add[0], fmt.Sprintf("%08x", d.ToSerial)) {
		t.Fatalf("added ZONEMD %s does not carry serial %d", add[0], d.ToSerial)
	}
}
