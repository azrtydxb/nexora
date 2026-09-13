package dynupdate_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/miekg/dns"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/dynupdate"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"github.com/piwi3910/nexora/mgmt/internal/tsigkey"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

var actor = auth.Actor{Type: "user", ID: "t", Name: "t"}

func rr(t *testing.T, s string) dns.RR {
	t.Helper()
	r, err := dns.NewRR(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func setup(t *testing.T) (*dynupdate.Applier, *zone.Service, *zone.Zone) {
	t.Helper()
	ctx := context.Background()
	zs := &zone.Service{Store: storetest.New(t), Now: time.Now}
	z, err := zs.CreateZone(ctx, actor, zone.CreateZoneInput{Name: "dyn.test.", Kind: "primary", DefaultTTL: 300,
		SOA: zone.SOA{MName: "ns1.dyn.test.", RName: "h.dyn.test."}, Nameservers: []string{"ns1.dyn.test."}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zs.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "old.dyn.test.", Type: "A", TTL: 300, Data: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	return &dynupdate.Applier{Zones: zs, Now: time.Now, TSIGCheck: false}, zs, z
}

func apply(t *testing.T, a *dynupdate.Applier, build func(m *dns.Msg)) uint32 {
	t.Helper()
	m := new(dns.Msg)
	m.SetUpdate("dyn.test.")
	build(m)
	wire, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	res := a.Apply(context.Background(), "engine-1", &controlv1.UpdateRequest{RequestId: "r1", Zone: "dyn.test.", Client: "127.0.0.1:5000", Message: wire, TsigKey: "ddns-key."})
	return res.Rcode
}

func count(t *testing.T, zs *zone.Service, z *zone.Zone, name, typ string) int {
	recs, _, _ := zs.ListRecords(context.Background(), z.ID, name, typ, "", 100)
	return len(recs)
}

func TestPrerequisitesRFC2136(t *testing.T) {
	a, zs, z := setup(t)
	cases := []struct {
		name  string
		build func(m *dns.Msg)
		rcode int
	}{
		{"rrset exists passes", func(m *dns.Msg) {
			m.RRsetUsed([]dns.RR{rr(t, "old.dyn.test. 0 IN A 0.0.0.0")})
			m.Insert([]dns.RR{rr(t, "p1.dyn.test. 300 IN A 192.0.2.10")})
		}, dns.RcodeSuccess},
		{"rrset exists fails", func(m *dns.Msg) {
			m.RRsetUsed([]dns.RR{rr(t, "none.dyn.test. 0 IN A 0.0.0.0")})
			m.Insert([]dns.RR{rr(t, "p2.dyn.test. 300 IN A 192.0.2.11")})
		}, dns.RcodeNXRrset},
		{"name not in use fails", func(m *dns.Msg) { m.NameNotUsed([]dns.RR{rr(t, "old.dyn.test. 0 IN A 0.0.0.0")}) }, dns.RcodeYXDomain},
		{"name in use fails", func(m *dns.Msg) { m.NameUsed([]dns.RR{rr(t, "none.dyn.test. 0 IN A 0.0.0.0")}) }, dns.RcodeNameError},
		{"rrset not used fails", func(m *dns.Msg) { m.RRsetNotUsed([]dns.RR{rr(t, "old.dyn.test. 0 IN A 0.0.0.0")}) }, dns.RcodeYXRrset},
		{"value-dependent mismatch", func(m *dns.Msg) { m.Used([]dns.RR{rr(t, "old.dyn.test. 0 IN A 192.0.2.99")}) }, dns.RcodeNXRrset},
		{"out of zone", func(m *dns.Msg) { m.Insert([]dns.RR{rr(t, "x.example.org. 300 IN A 192.0.2.1")}) }, dns.RcodeNotZone},
		{"dnssec type refused", func(m *dns.Msg) { m.Insert([]dns.RR{rr(t, "dyn.test. 300 IN DNSKEY 257 3 13 AAAA")}) }, dns.RcodeRefused},
	}
	for _, c := range cases {
		if got := apply(t, a, c.build); got != uint32(c.rcode) {
			t.Errorf("%s: rcode %s, want %s", c.name, dns.RcodeToString[int(got)], dns.RcodeToString[c.rcode])
		}
	}
	if count(t, zs, z, "p1.dyn.test.", "A") != 1 || count(t, zs, z, "p2.dyn.test.", "A") != 0 {
		t.Fatal("prerequisite failure must not apply the update section")
	}
}

func TestUpdateSectionAndSerial(t *testing.T) {
	a, zs, z := setup(t)
	ctx := context.Background()
	before, _ := zs.GetZone(ctx, z.ID)
	if rc := apply(t, a, func(m *dns.Msg) {
		m.Insert([]dns.RR{rr(t, "host.dyn.test. 300 IN A 192.0.2.20"), rr(t, "host.dyn.test. 300 IN A 192.0.2.21")})
		m.Remove([]dns.RR{rr(t, "old.dyn.test. 300 IN A 192.0.2.1")})
	}); rc != dns.RcodeSuccess {
		t.Fatalf("rcode %d", rc)
	}
	after, _ := zs.GetZone(ctx, z.ID)
	if count(t, zs, z, "host.dyn.test.", "A") != 2 || count(t, zs, z, "old.dyn.test.", "A") != 0 || after.Serial != zone.SerialNext(before.Serial) {
		t.Fatalf("update not applied atomically: serial %d->%d", before.Serial, after.Serial)
	}
	apply(t, a, func(m *dns.Msg) { m.RemoveRRset([]dns.RR{rr(t, "dyn.test. 0 IN NS ns1.dyn.test.")}) })
	if count(t, zs, z, "dyn.test.", "NS") != 1 {
		t.Fatal("apex NS RRset must not be deletable")
	}
	apply(t, a, func(m *dns.Msg) { m.Insert([]dns.RR{rr(t, "host.dyn.test. 300 IN CNAME elsewhere.example.")}) })
	if count(t, zs, z, "host.dyn.test.", "CNAME") != 0 {
		t.Fatal("CNAME added to a name with other data (RFC 2136 §3.4.2.2 ignores it)")
	}
	apply(t, a, func(m *dns.Msg) { m.RemoveName([]dns.RR{rr(t, "host.dyn.test. 0 IN A 0.0.0.0")}) })
	if count(t, zs, z, "host.dyn.test.", "") != 0 {
		t.Fatal("delete all RRsets from a name")
	}
}

func TestTSIGAndUpdatePolicy(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	keys := &tsigkey.Service{Store: st, Box: kekBox(t)}
	allowed, err := keys.Create(ctx, actor, "ddns-key.", "hmac-sha256", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := keys.Create(ctx, actor, "other-key.", "hmac-sha256", "")
	if err != nil {
		t.Fatal(err)
	}
	zs := &zone.Service{Store: st, Now: time.Now}
	z, err := zs.CreateZone(ctx, actor, zone.CreateZoneInput{Name: "dyn.test.", Kind: "primary", DefaultTTL: 300,
		SOA: zone.SOA{MName: "ns1.dyn.test.", RName: "h.dyn.test."}, Nameservers: []string{"ns1.dyn.test."}, UpdateTSIGKeyIDs: []uuid.UUID{allowed.ID}})
	if err != nil {
		t.Fatal(err)
	}
	a := &dynupdate.Applier{Zones: zs, TSIG: keys, Now: time.Now, TSIGCheck: true}
	wire := func(owner, keyName, secret string) []byte {
		m := new(dns.Msg)
		m.SetUpdate("dyn.test.")
		m.Insert([]dns.RR{rr(t, owner+" 300 IN A 192.0.2.30")})
		if keyName == "" {
			b, err := m.Pack()
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
		m.SetTsig(keyName, dns.HmacSHA256, 300, time.Now().Unix())
		b, _, err := dns.TsigGenerate(m, secret, "", false)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	wrongSecret := base64.StdEncoding.EncodeToString([]byte("not-the-secret-of-ddns-key-at-all"))
	cases := []struct {
		name, owner, engineKey string
		msg                    []byte
		rcode                  int
	}{
		{"unsigned", "u.dyn.test.", "ddns-key.", wire("u.dyn.test.", "", ""), dns.RcodeRefused},
		{"key not in the zone policy", "o.dyn.test.", "other-key.", wire("o.dyn.test.", "other-key.", other.Secret), dns.RcodeRefused},
		{"wrong secret", "w.dyn.test.", "ddns-key.", wire("w.dyn.test.", "ddns-key.", wrongSecret), dns.RcodeNotAuth},
		{"key differs from the engine's", "m.dyn.test.", "other-key.", wire("m.dyn.test.", "ddns-key.", allowed.Secret), dns.RcodeNotAuth},
		{"allowed key", "ok.dyn.test.", "ddns-key.", wire("ok.dyn.test.", "ddns-key.", allowed.Secret), dns.RcodeSuccess},
	}
	for _, c := range cases {
		res := a.Apply(ctx, "engine-1", &controlv1.UpdateRequest{RequestId: c.name, Zone: "dyn.test.", Client: "127.0.0.1:5000", Message: c.msg, TsigKey: c.engineKey})
		if res.Rcode != uint32(c.rcode) || res.RequestId != c.name {
			t.Errorf("%s: rcode %s (%s), want %s", c.name, dns.RcodeToString[int(res.Rcode)], res.Detail, dns.RcodeToString[c.rcode])
		}
		want := 0
		if c.rcode == dns.RcodeSuccess {
			want = 1
		}
		if n := count(t, zs, z, c.owner, "A"); n != want {
			t.Errorf("%s: %d records at %s, want %d", c.name, n, c.owner, want)
		}
	}
}

func kekBox(t *testing.T) *secrets.Box {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.Open(secrets.Config{KEKFile: p})
	if err != nil {
		t.Fatal(err)
	}
	return box
}
