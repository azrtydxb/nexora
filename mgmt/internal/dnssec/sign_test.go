package dnssec

import (
	"crypto"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

var t0 = time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

func testKeys(t *testing.T) []Key { return testKeysAlg(t, dns.ECDSAP256SHA256) }

func testKeysAlg(t *testing.T, alg uint8) []Key {
	t.Helper()
	bits := 256
	if alg == dns.RSASHA256 {
		bits = 2048
	}
	var keys []Key
	for _, role := range []string{"ksk", "zsk"} {
		flags := uint16(256)
		if role == "ksk" {
			flags = 257
		}
		k := &dns.DNSKEY{Hdr: dns.RR_Header{Name: "example.test.", Rrtype: dns.TypeDNSKEY, Class: dns.ClassINET, Ttl: 3600}, Flags: flags, Protocol: 3, Algorithm: alg}
		priv, err := k.Generate(bits)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, Key{ID: role, Role: role, Algorithm: alg, DNSKEY: k, Signer: priv.(crypto.Signer), Signs: true, InCDS: role == "ksk"})
	}
	return keys
}

// basicRecords mirrors nzf.basicZone(2026091301, "before").
func basicRecords(t *testing.T) []dns.RR {
	t.Helper()
	lines := []string{
		"example.test. 3600 IN SOA ns1.example.test. hostmaster.example.test. 2026091301 7200 3600 1209600 300",
		"example.test. 3600 IN NS ns1.example.test.", "example.test. 3600 IN NS ns2.example.test.",
		"example.test. 3600 IN MX 10 mail.example.test.", "ns1.example.test. 3600 IN A 192.0.2.1",
		"ns2.example.test. 3600 IN AAAA 2001:db8::2", "mail.example.test. 3600 IN A 192.0.2.25",
		"www.example.test. 300 IN A 192.0.2.10", "www.example.test. 300 IN A 192.0.2.11",
		"alias.example.test. 300 IN CNAME www.example.test.", "*.wild.example.test. 300 IN TXT \"wildcard\"",
		"a.b.c.example.test. 300 IN A 192.0.2.20", "sub.example.test. 3600 IN NS ns.sub.example.test.",
		"sub.example.test. 3600 IN DS 60485 13 2 D4B7D520E7BB5F0F67674A0CCEB1E3E0614B93C4F9E99B8383F6A1E4469DA50A",
		"ns.sub.example.test. 3600 IN A 192.0.2.53", "insecure.example.test. 3600 IN NS ns.insecure.example.test.",
		"ns.insecure.example.test. 3600 IN A 192.0.2.54", "dn.example.test. 300 IN DNAME example.net.",
	}
	var out []dns.RR
	for _, l := range lines {
		rr, err := dns.NewRR(l)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, rr)
	}
	return out
}

func rrsets(served []dns.RR) map[string][]dns.RR {
	m := map[string][]dns.RR{}
	for _, rr := range served {
		if rr.Header().Rrtype == dns.TypeRRSIG {
			continue
		}
		k := strings.ToLower(rr.Header().Name) + "/" + dns.TypeToString[rr.Header().Rrtype]
		m[k] = append(m[k], rr)
	}
	return m
}

func sigsFor(served []dns.RR, owner string, typ uint16) []*dns.RRSIG {
	var out []*dns.RRSIG
	for _, rr := range served {
		if s, ok := rr.(*dns.RRSIG); ok && strings.EqualFold(s.Hdr.Name, owner) && s.TypeCovered == typ {
			out = append(out, s)
		}
	}
	return out
}

func verifyAll(t *testing.T, out *Output, keys []Key, now time.Time) {
	t.Helper()
	byTag := map[uint16]*dns.DNSKEY{}
	for _, k := range keys {
		byTag[k.DNSKEY.KeyTag()] = k.DNSKEY
	}
	for name, set := range rrsets(out.Served) {
		owner, typ := set[0].Header().Name, set[0].Header().Rrtype
		occluded := strings.HasSuffix(owner, ".sub.example.test.") || strings.HasSuffix(owner, ".insecure.example.test.")
		delegation := (owner == "sub.example.test." || owner == "insecure.example.test.") && typ == dns.TypeNS
		sigs := sigsFor(out.Served, owner, typ)
		if occluded || delegation {
			if len(sigs) != 0 {
				t.Errorf("%s must not be signed", name)
			}
			continue
		}
		if len(sigs) == 0 {
			t.Errorf("%s is unsigned", name)
			continue
		}
		for _, s := range sigs {
			if err := s.Verify(byTag[s.KeyTag], set); err != nil {
				t.Errorf("%s: %v", name, err)
			}
			if !s.ValidityPeriod(now) {
				t.Errorf("%s: signature not valid at %v", name, now)
			}
			wantKSK := typ == dns.TypeDNSKEY || typ == dns.TypeCDS || typ == dns.TypeCDNSKEY
			if (byTag[s.KeyTag].Flags == 257) != wantKSK {
				t.Errorf("%s signed by the wrong key role", name)
			}
		}
	}
}

func TestSignNSECChainAndSignatures(t *testing.T) {
	keys := testKeys(t)
	out, err := Sign(Input{Origin: "example.test.", Records: basicRecords(t), Keys: keys, NSEC3: false, Now: t0})
	if err != nil {
		t.Fatal(err)
	}
	verifyAll(t, out, keys, t0)
	sets := rrsets(out.Served)
	if len(sets["example.test./DNSKEY"]) != 2 || len(sets["example.test./CDS"]) != 1 || len(sets["example.test./CDNSKEY"]) != 1 {
		t.Fatalf("apex key RRsets: %v", sets["example.test./DNSKEY"])
	}
	// NSEC chain: every authoritative owner (no ENTs, no glue) once, closing at the apex
	var owners []string
	for _, rr := range out.Served {
		if n, ok := rr.(*dns.NSEC); ok {
			owners = append(owners, n.Hdr.Name)
			if n.Hdr.Ttl != 300 {
				t.Errorf("NSEC TTL %d, want min(SOA TTL, MINIMUM) = 300", n.Hdr.Ttl)
			}
		}
	}
	if len(owners) != 11 {
		t.Fatalf("NSEC owners %d: %v", len(owners), owners)
	}
	for _, n := range out.Served {
		if nsec, ok := n.(*dns.NSEC); ok && nsec.Hdr.Name == "sub.example.test." {
			if fmt.Sprint(nsec.TypeBitMap) != fmt.Sprint([]uint16{dns.TypeNS, dns.TypeDS, dns.TypeRRSIG, dns.TypeNSEC}) {
				t.Fatalf("delegation NSEC bitmap %v", nsec.TypeBitMap)
			}
		}
		if nsec, ok := n.(*dns.NSEC); ok && nsec.Hdr.Name == "insecure.example.test." {
			if fmt.Sprint(nsec.TypeBitMap) != fmt.Sprint([]uint16{dns.TypeNS, dns.TypeRRSIG, dns.TypeNSEC}) {
				t.Fatalf("insecure delegation NSEC bitmap %v", nsec.TypeBitMap)
			}
		}
	}
}

func TestSignNSEC3IncludesEmptyNonTerminalsWithZeroIterations(t *testing.T) {
	keys := testKeys(t)
	out, err := Sign(Input{Origin: "example.test.", Records: basicRecords(t), Keys: keys, NSEC3: true, Now: t0})
	if err != nil {
		t.Fatal(err)
	}
	verifyAll(t, out, keys, t0)
	hashes := map[string]bool{}
	for _, rr := range out.Served {
		switch v := rr.(type) {
		case *dns.NSEC3:
			if v.Iterations != 0 || v.SaltLength != 0 || v.Flags != 0 || v.Hash != dns.SHA1 {
				t.Fatalf("NSEC3 parameters: %v", v)
			}
			hashes[strings.ToUpper(strings.SplitN(v.Hdr.Name, ".", 2)[0])] = true
		case *dns.NSEC:
			t.Fatal("NSEC present in NSEC3 zone")
		case *dns.NSEC3PARAM:
			if v.Hdr.Name != "example.test." || v.Iterations != 0 || v.Salt != "" {
				t.Fatalf("NSEC3PARAM %v", v)
			}
		}
	}
	for _, name := range []string{"example.test.", "b.c.example.test.", "c.example.test.", "wild.example.test.", "sub.example.test."} {
		if !hashes[dns.HashName(name, dns.SHA1, 0, "")] {
			t.Errorf("no NSEC3 for %s", name)
		}
	}
	if hashes[dns.HashName("ns.sub.example.test.", dns.SHA1, 0, "")] {
		t.Error("occluded glue has an NSEC3")
	}
}

func TestSignatureReuseAndRefresh(t *testing.T) {
	keys := testKeys(t)
	first, err := Sign(Input{Origin: "example.test.", Records: basicRecords(t), Keys: keys, NSEC3: true, Now: t0})
	if err != nil {
		t.Fatal(err)
	}
	day1, _ := Sign(Input{Origin: "example.test.", Records: basicRecords(t), Keys: keys, NSEC3: true, Cache: first.Cache, Now: t0.Add(24 * time.Hour)})
	if day1.Created != 0 || day1.Reused != first.Created {
		t.Fatalf("day 1: created=%d reused=%d (first created %d)", day1.Created, day1.Reused, first.Created)
	}
	if !day1.NextRefresh.Before(t0.Add(Validity-RefreshBefore)) || day1.NextRefresh.Before(t0.Add(Validity-RefreshBefore-time.Hour)) {
		t.Fatalf("next refresh %v", day1.NextRefresh)
	}
	day8, _ := Sign(Input{Origin: "example.test.", Records: basicRecords(t), Keys: keys, NSEC3: true, Cache: first.Cache, Now: t0.Add(8 * 24 * time.Hour)})
	if day8.Reused != 0 {
		t.Fatalf("day 8: %d signatures reused past the 7-day refresh point", day8.Reused)
	}
	changed := append(basicRecords(t), mustRR(t, "new.example.test. 300 IN A 192.0.2.99"))
	edit, _ := Sign(Input{Origin: "example.test.", Records: changed, Keys: keys, NSEC3: true, Cache: first.Cache, Now: t0.Add(time.Hour)})
	if edit.Created == 0 || edit.Created > 6 {
		t.Fatalf("one added record re-signed %d RRsets (want its A, its NSEC3, the neighbouring NSEC3 and none else)", edit.Created)
	}
}

func mustRR(t *testing.T, s string) dns.RR {
	rr, err := dns.NewRR(s)
	if err != nil {
		t.Fatal(err)
	}
	return rr
}
