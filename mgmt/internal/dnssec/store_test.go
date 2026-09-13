package dnssec_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/dnssec"
	"github.com/piwi3910/nexora/mgmt/internal/dynupdate"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

var actor = auth.Actor{Type: "user", ID: "t", Name: "t"}

func kekBox(t *testing.T) *secrets.Box {
	t.Helper()
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
	return box
}

func TestEnableSignsAndEditsAreResigned(t *testing.T) {
	ctx := context.Background()
	box := kekBox(t)
	st := storetest.New(t)
	zs := &zone.Service{Store: st, Signer: &dnssec.Store{Box: box}, Now: time.Now}
	z, err := zs.CreateZone(ctx, actor, zone.CreateZoneInput{Name: "signed.test.", Kind: "primary", DefaultTTL: 300,
		SOA: zone.SOA{MName: "ns1.signed.test.", RName: "h.signed.test."}, Nameservers: []string{"ns1.signed.test."}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = zs.Mutate(ctx, z.ID, func(tx pgx.Tx, z *zone.Zone) (string, any, any, zone.RebuildOptions, error) {
		err := dnssec.Enable(ctx, tx, box, z.ID, dnssec.Settings{Algorithm: 13, NSECMode: "nsec3", KeyBackend: secrets.BackendKEK, PropagationDelay: time.Hour, ParentDSTTL: 24 * time.Hour, ZSKLifetimeDays: 90})
		z.DNSSECEnabled = true
		return "updateZoneDnssec", nil, nil, zone.RebuildOptions{Force: true}, err
	}, actor)
	if err != nil {
		t.Fatal(err)
	}
	var keys int
	st.Pool.QueryRow(ctx, `SELECT count(*) FROM dnssec_keys WHERE zone_id=$1 AND state='active' AND private_envelope IS NOT NULL`, z.ID).Scan(&keys)
	if keys != 2 {
		t.Fatalf("active enveloped keys: %d", keys)
	}
	if _, err := zs.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "www.signed.test.", Type: "A", TTL: 300, Data: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	tx, _ := st.Pool.Begin(ctx)
	defer tx.Rollback(ctx)
	fresh, _ := zs.GetZone(ctx, z.ID)
	if !fresh.DNSSECEnabled {
		t.Fatal("zone does not report DNSSEC enabled")
	}
	served, err := zone.LoadServed(ctx, tx, fresh)
	if err != nil {
		t.Fatal(err)
	}
	var dnskeys []dns.RR
	var wwwA []dns.RR
	var wwwSig, soaSig *dns.RRSIG
	var soa *dns.SOA
	for _, rr := range served {
		switch v := rr.(type) {
		case *dns.DNSKEY:
			dnskeys = append(dnskeys, v)
		case *dns.A:
			if v.Hdr.Name == "www.signed.test." {
				wwwA = append(wwwA, v)
			}
		case *dns.SOA:
			soa = v
		case *dns.RRSIG:
			if v.Hdr.Name == "www.signed.test." && v.TypeCovered == dns.TypeA {
				wwwSig = v
			}
			if v.TypeCovered == dns.TypeSOA {
				soaSig = v
			}
		}
	}
	if wwwSig == nil || soaSig == nil || soa.Serial != fresh.Serial {
		t.Fatalf("served set not re-signed: wwwSig=%v soaSig=%v", wwwSig, soaSig)
	}
	for _, sig := range []*dns.RRSIG{wwwSig} {
		ok := false
		for _, k := range dnskeys {
			if k.(*dns.DNSKEY).KeyTag() == sig.KeyTag && sig.Verify(k.(*dns.DNSKEY), wwwA) == nil {
				ok = true
			}
		}
		if !ok {
			t.Fatal("www A RRSIG does not verify")
		}
	}
	if err := soaSig.Verify(findKey(dnskeys, soaSig.KeyTag), []dns.RR{soa}); err != nil {
		t.Fatalf("SOA signature is stale after the serial bump: %v", err)
	}
}

func findKey(keys []dns.RR, tag uint16) *dns.DNSKEY {
	for _, k := range keys {
		if d := k.(*dns.DNSKEY); d.KeyTag() == tag {
			return d
		}
	}
	return nil
}

// signedZone creates primary zone dyn.test. with one A record and enables signing on it.
func signedZone(t *testing.T, box *secrets.Box, st dnssec.Settings) (*zone.Service, *zone.Zone) {
	t.Helper()
	ctx := context.Background()
	zs := &zone.Service{Store: storetest.New(t), Signer: &dnssec.Store{Box: box}, Now: time.Now}
	z, err := zs.CreateZone(ctx, actor, zone.CreateZoneInput{Name: "dyn.test.", Kind: "primary", DefaultTTL: 300,
		SOA: zone.SOA{MName: "ns1.dyn.test.", RName: "h.dyn.test."}, Nameservers: []string{"ns1.dyn.test."}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zs.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "old.dyn.test.", Type: "A", TTL: 300, Data: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	if z, err = zs.Mutate(ctx, z.ID, func(tx pgx.Tx, z *zone.Zone) (string, any, any, zone.RebuildOptions, error) {
		return "updateZoneDnssec", nil, nil, zone.RebuildOptions{Force: true}, dnssec.Enable(ctx, tx, box, z.ID, st)
	}, actor); err != nil {
		t.Fatal(err)
	}
	return zs, z
}

// servedVerified loads the served set of zoneID and verifies every RRSIG in it against the served
// DNSKEYs at now; it returns the served set.
func servedVerified(t *testing.T, zs *zone.Service, zoneID uuid.UUID, now time.Time) []dns.RR {
	t.Helper()
	ctx := context.Background()
	z, err := zs.GetZone(ctx, zoneID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := zs.Store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	served, err := zone.LoadServed(ctx, tx, z)
	if err != nil {
		t.Fatal(err)
	}
	type key struct {
		owner string
		typ   uint16
	}
	sets := map[key][]dns.RR{}
	var keys []dns.RR
	var sigs []*dns.RRSIG
	for _, rr := range served {
		if s, ok := rr.(*dns.RRSIG); ok {
			sigs = append(sigs, s)
			continue
		}
		if rr.Header().Rrtype == dns.TypeDNSKEY {
			keys = append(keys, rr)
		}
		k := key{dns.CanonicalName(rr.Header().Name), rr.Header().Rrtype}
		sets[k] = append(sets[k], rr)
	}
	if len(sigs) == 0 {
		t.Fatal("served set has no signatures")
	}
	for _, s := range sigs {
		k := findKey(keys, s.KeyTag)
		if k == nil {
			t.Fatalf("RRSIG %s/%d: no DNSKEY with tag %d", s.Hdr.Name, s.TypeCovered, s.KeyTag)
		}
		if err := s.Verify(k, sets[key{dns.CanonicalName(s.Hdr.Name), s.TypeCovered}]); err != nil {
			t.Fatalf("RRSIG %s/%s: %v", s.Hdr.Name, dns.TypeToString[s.TypeCovered], err)
		}
		if !s.ValidityPeriod(now) {
			t.Fatalf("RRSIG %s/%s not valid at %v", s.Hdr.Name, dns.TypeToString[s.TypeCovered], now)
		}
	}
	return served
}

type sigRow struct {
	owner    string
	typ, tag int32
}

func signatureRows(t *testing.T, st *store.Store, zoneID uuid.UUID) map[sigRow]string {
	t.Helper()
	rows, err := st.Pool.Query(context.Background(), "SELECT owner, type_covered, key_tag, encode(rrsig, 'hex') FROM zone_signatures WHERE zone_id = $1", zoneID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[sigRow]string{}
	for rows.Next() {
		var r sigRow
		var sig string
		if err := rows.Scan(&r.owner, &r.typ, &r.tag, &sig); err != nil {
			t.Fatal(err)
		}
		out[r] = sig
	}
	return out
}

func TestDynamicUpdateToSignedZoneIsResignedIncrementally(t *testing.T) {
	box := kekBox(t)
	zs, z := signedZone(t, box, dnssec.Settings{NSECMode: "nsec3", KeyBackend: secrets.BackendKEK, ZSKLifetimeDays: 90})
	before := signatureRows(t, zs.Store, z.ID)
	m := new(dns.Msg)
	m.SetUpdate("dyn.test.")
	host, err := dns.NewRR("host.dyn.test. 300 IN A 192.0.2.20")
	if err != nil {
		t.Fatal(err)
	}
	m.Insert([]dns.RR{host})
	wire, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	a := &dynupdate.Applier{Zones: zs, Now: time.Now}
	res := a.Apply(context.Background(), "engine-1", &controlv1.UpdateRequest{RequestId: "r1", Zone: "dyn.test.", Client: "127.0.0.1:5000", Message: wire, TsigKey: "ddns-key."})
	if res.Rcode != dns.RcodeSuccess {
		t.Fatalf("update rcode %d: %s", res.Rcode, res.Detail)
	}
	served := servedVerified(t, zs, z.ID, time.Now())
	signedHost := false
	for _, rr := range served {
		if s, ok := rr.(*dns.RRSIG); ok && s.Hdr.Name == "host.dyn.test." && s.TypeCovered == dns.TypeA {
			signedHost = true
		}
	}
	if !signedHost {
		t.Fatal("updated A RRset is not signed")
	}
	after := signatureRows(t, zs.Store, z.ID)
	changed := 0
	for k, sig := range after {
		if before[k] != sig {
			changed++
		}
	}
	// host A, its NSEC3, the preceding NSEC3 and the SOA; everything else keeps its signature.
	if changed == 0 || changed > 6 || len(after) < len(before) {
		t.Fatalf("update re-signed %d of %d signatures (%d before)", changed, len(after), len(before))
	}
}

func TestPKCS11KeysSignInsideTheToken(t *testing.T) {
	tok := harness.InitSoftHSM(t, "nexora-dnssec")
	box, err := secrets.Open(secrets.Config{PKCS11Module: tok.Module, PKCS11TokenLabel: tok.Label, PKCS11PinFile: tok.PinFile})
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	for _, alg := range []uint8{dns.ECDSAP256SHA256, dns.RSASHA256} {
		zs, z := signedZone(t, box, dnssec.Settings{Algorithm: alg, NSECMode: "nsec", KeyBackend: secrets.BackendPKCS11})
		servedVerified(t, zs, z.ID, time.Now())
		rows, err := zs.Store.Pool.Query(context.Background(), "SELECT key_ref FROM dnssec_keys WHERE zone_id = $1 AND backend = 'pkcs11' AND private_envelope IS NULL AND algorithm = $2", z.ID, int16(alg))
		if err != nil {
			t.Fatal(err)
		}
		refs, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
		if err != nil || len(refs) != 2 {
			t.Fatalf("alg %d: token keys %d (%v)", alg, len(refs), err)
		}
		for _, ref := range refs {
			extractable, sensitive, err := box.PKCS11KeyAttributes(ref)
			if err != nil || extractable || !sensitive {
				t.Fatalf("alg %d: extractable=%v sensitive=%v err=%v", alg, extractable, sensitive, err)
			}
		}
		if _, err := zs.Mutate(context.Background(), z.ID, func(tx pgx.Tx, z *zone.Zone) (string, any, any, zone.RebuildOptions, error) {
			return "updateZoneDnssec", nil, nil, zone.RebuildOptions{Force: true}, dnssec.Disable(context.Background(), tx, z.ID)
		}, actor); err != nil {
			t.Fatal(err)
		}
		if err := dnssec.DestroyPending(context.Background(), zs.Store, box); err != nil {
			t.Fatal(err)
		}
		for _, ref := range refs {
			if _, _, err := box.PKCS11KeyAttributes(ref); err == nil {
				t.Fatalf("alg %d: disabled zone's key is still in the token", alg)
			}
		}
		fresh, _ := zs.GetZone(context.Background(), z.ID)
		tx, _ := zs.Store.Pool.Begin(context.Background())
		served, err := zone.LoadServed(context.Background(), tx, fresh)
		_ = tx.Rollback(context.Background())
		if err != nil || fresh.DNSSECEnabled {
			t.Fatalf("disabled zone: enabled=%v err=%v", fresh.DNSSECEnabled, err)
		}
		for _, rr := range served {
			if rr.Header().Rrtype == dns.TypeRRSIG || rr.Header().Rrtype == dns.TypeDNSKEY || rr.Header().Rrtype == dns.TypeNSEC {
				t.Fatalf("disabled zone still serves %s", rr)
			}
		}
	}
}

func TestEnableRefusesWithoutKeyStorageOrOnSecondaries(t *testing.T) {
	ctx := context.Background()
	zs := &zone.Service{Store: storetest.New(t), Now: time.Now}
	sec, err := zs.CreateZone(ctx, actor, zone.CreateZoneInput{Name: "sec.test.", Kind: "secondary", Primaries: []zone.Endpoint{{Address: "192.0.2.1:53"}}})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := zs.Store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := dnssec.Enable(ctx, tx, &secrets.Box{}, sec.ID, dnssec.Settings{}); err != secrets.ErrUnconfigured {
		t.Fatalf("unconfigured box: %v", err)
	}
	box := kekBox(t)
	if err := dnssec.Enable(ctx, tx, box, sec.ID, dnssec.Settings{KeyBackend: secrets.BackendPKCS11}); err != secrets.ErrBackendUnavailable {
		t.Fatalf("missing backend: %v", err)
	}
	if err := dnssec.Enable(ctx, tx, box, sec.ID, dnssec.Settings{}); err != dnssec.ErrNotPrimary {
		t.Fatalf("secondary zone: %v", err)
	}
}

func TestMaintainerRefreshesSignaturesUnderAdvisoryLock(t *testing.T) {
	ctx := context.Background()
	box := kekBox(t)
	zs, z := signedZone(t, box, dnssec.Settings{NSECMode: "nsec", KeyBackend: secrets.BackendKEK})
	var nextAt time.Time
	if err := zs.Store.Pool.QueryRow(ctx, "SELECT next_maintenance_at FROM zone_dnssec WHERE zone_id = $1", z.ID).Scan(&nextAt); err != nil {
		t.Fatal(err)
	}
	if d := time.Until(nextAt); d < dnssec.Validity-dnssec.RefreshBefore-2*time.Hour || d > dnssec.Validity-dnssec.RefreshBefore {
		t.Fatalf("next maintenance in %v", d)
	}
	// Another instance holds the zone's signing lock.
	lock, err := zs.Store.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if _, err := lock.Exec(ctx, "SELECT pg_advisory_lock(hashtext($1))", "dnssec:"+z.ID.String()); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(8 * 24 * time.Hour)
	zs.Now = func() time.Time { return later }
	if _, err := zs.Store.Pool.Exec(ctx, "UPDATE zone_dnssec SET next_maintenance_at = now() - interval '1 second' WHERE zone_id = $1", z.ID); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		_ = (&dnssec.Maintainer{Store: zs.Store, Service: &dnssec.Service{Store: zs.Store, Box: box, Zones: zs}, Tick: 50 * time.Millisecond}).Run(runCtx)
		close(done)
	}()
	defer func() { cancel(); <-done }()
	time.Sleep(500 * time.Millisecond)
	if cur, _ := zs.GetZone(ctx, z.ID); cur.Serial != z.Serial {
		t.Fatal("zone re-signed while another instance held its lock")
	}
	if _, err := lock.Exec(ctx, "SELECT pg_advisory_unlock(hashtext($1))", "dnssec:"+z.ID.String()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if cur, _ := zs.GetZone(ctx, z.ID); cur.Serial != z.Serial {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("maintainer did not re-sign the due zone")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	for _, rr := range servedVerified(t, zs, z.ID, later) {
		if s, ok := rr.(*dns.RRSIG); ok && time.Unix(int64(s.Expiration), 0).Before(later.Add(dnssec.Validity-2*time.Hour)) {
			t.Fatalf("signature %s/%s not refreshed: expires %v", s.Hdr.Name, dns.TypeToString[s.TypeCovered], time.Unix(int64(s.Expiration), 0))
		}
	}
}
