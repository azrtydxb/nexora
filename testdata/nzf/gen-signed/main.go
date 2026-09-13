// Command gen-signed writes the pre-signed NZF1 test zones the engine's DNSSEC answer tests load:
// signed-nsec-full.nzf and signed-nsec3-full.nzf (SHA-1, 0 iterations, empty salt, no opt-out),
// plus signed-anchor.key, the KSK DNSKEY a validator (delv) trusts. The signing keys are Ed25519
// keys derived from fixture seeds, so the output is byte-for-byte reproducible. Test-only: the
// management-plane signer (M4 Task 12) produces the real served images.
//
//	go run ./testdata/nzf/gen-signed
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/miekg/dns"
)

const (
	origin    = "example.test."
	serial    = 2026091301
	soaTTL    = 3600
	negTTL    = 300
	inception = 1767225600 // 2026-01-01T00:00:00Z
	expire    = 2082758400 // 2036-01-01T00:00:00Z
)

var zoneText = []string{
	fmt.Sprintf("example.test. %d IN SOA ns1.example.test. hostmaster.example.test. %d 7200 3600 1209600 %d", soaTTL, serial, negTTL),
	"example.test. 3600 IN NS ns1.example.test.",
	"example.test. 3600 IN MX 10 mail.example.test.",
	"ns1.example.test. 3600 IN A 192.0.2.1",
	"mail.example.test. 3600 IN A 192.0.2.25",
	"www.example.test. 300 IN A 192.0.2.10",
	"www.example.test. 300 IN A 192.0.2.11",
	"alias.example.test. 300 IN CNAME www.example.test.",
	"*.wild.example.test. 300 IN TXT \"wildcard\"",
	"a.b.c.example.test. 300 IN A 192.0.2.20",
	"sub.example.test. 3600 IN NS ns.sub.example.test.",
	"sub.example.test. 3600 IN DS 60485 13 2 D4B7D520E7BB5F0F67674A0CCEB1E3E0614B93C4F9E99B8383F6A1E4469DA50A",
	"ns.sub.example.test. 3600 IN A 192.0.2.53",
	"insecure.example.test. 3600 IN NS ns.insecure.example.test.",
	"ns.insecure.example.test. 3600 IN A 192.0.2.54",
	"dn.example.test. 300 IN DNAME example.net.",
}

type key struct {
	pub  *dns.DNSKEY
	priv ed25519.PrivateKey
}

func fixtureKey(label string, flags uint16) key {
	seed := sha256.Sum256([]byte("fixture-nexora-m4-t13-" + label))
	priv := ed25519.NewKeyFromSeed(seed[:])
	pub := &dns.DNSKEY{
		Hdr:       dns.RR_Header{Name: origin, Rrtype: dns.TypeDNSKEY, Class: dns.ClassINET, Ttl: soaTTL},
		Flags:     flags,
		Protocol:  3,
		Algorithm: dns.ED25519,
	}
	pub.PublicKey = base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	return key{pub: pub, priv: priv}
}

func mustRR(s string) dns.RR {
	rr, err := dns.NewRR(s)
	if err != nil {
		log.Fatalf("%s: %v", s, err)
	}
	return rr
}

type rrKey struct {
	owner string
	rtype uint16
}

func labels(name string) []string { return dns.SplitDomainName(name) }

// isBelow reports whether name lies strictly below parent.
func isBelow(name, parent string) bool {
	return name != parent && dns.IsSubDomain(parent, name)
}

type zone struct {
	rrs  []dns.RR
	ksk  key
	zsk  key
	cuts map[string]bool
}

func newZone() *zone {
	z := &zone{ksk: fixtureKey("ksk", 257), zsk: fixtureKey("zsk", 256), cuts: map[string]bool{}}
	for _, s := range zoneText {
		z.rrs = append(z.rrs, mustRR(s))
	}
	z.rrs = append(z.rrs, z.ksk.pub, z.zsk.pub)
	cds := z.ksk.pub.ToDS(dns.SHA256).ToCDS()
	cds.Hdr = dns.RR_Header{Name: origin, Rrtype: dns.TypeCDS, Class: dns.ClassINET, Ttl: soaTTL}
	cdnskey := z.ksk.pub.ToCDNSKEY()
	cdnskey.Hdr = dns.RR_Header{Name: origin, Rrtype: dns.TypeCDNSKEY, Class: dns.ClassINET, Ttl: soaTTL}
	z.rrs = append(z.rrs, cds, cdnskey)
	for _, rr := range z.rrs {
		if rr.Header().Rrtype == dns.TypeNS && rr.Header().Name != origin {
			z.cuts[rr.Header().Name] = true
		}
	}
	return z
}

// occluded: strictly below a delegation point (glue).
func (z *zone) occluded(name string) bool {
	for c := range z.cuts {
		if isBelow(name, c) {
			return true
		}
	}
	return false
}

// authoritative names (cuts included, glue excluded) and every empty non-terminal between them
// and the apex.
func (z *zone) names() (auth map[string]bool, ents map[string]bool) {
	auth, ents = map[string]bool{}, map[string]bool{}
	for _, rr := range z.rrs {
		n := rr.Header().Name
		if !z.occluded(n) {
			auth[n] = true
		}
	}
	for n := range auth {
		ls := labels(n)
		for i := 1; i < len(ls)-len(labels(origin)); i++ {
			anc := dns.Fqdn(strings.Join(ls[i:], "."))
			if !auth[anc] {
				ents[anc] = true
			}
		}
	}
	return auth, ents
}

func (z *zone) typesAt(name string) []uint16 {
	seen := map[uint16]bool{}
	for _, rr := range z.rrs {
		if rr.Header().Name == name {
			seen[rr.Header().Rrtype] = true
		}
	}
	out := make([]uint16, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// signable: authoritative RRsets, except NS at delegation points.
func (z *zone) signable(rr dns.RR) bool {
	h := rr.Header()
	if z.occluded(h.Name) || h.Rrtype == dns.TypeRRSIG {
		return false
	}
	return !(z.cuts[h.Name] && h.Rrtype == dns.TypeNS)
}

func canonLess(a, b string) bool {
	return bytes.Compare(canonKey(a), canonKey(b)) < 0
}

func canonKey(name string) []byte {
	ls := labels(strings.ToLower(name))
	var k []byte
	for i := len(ls) - 1; i >= 0; i-- {
		buf := make([]byte, 256)
		n, err := dns.PackDomainName(ls[i]+".", buf, 0, nil, false)
		if err != nil {
			log.Fatal(err)
		}
		for _, c := range buf[1 : n-1] {
			switch c {
			case 0:
				k = append(k, 1, 1)
			case 1:
				k = append(k, 1, 2)
			default:
				k = append(k, c)
			}
		}
		k = append(k, 0)
	}
	return k
}

func (z *zone) addNSEC() {
	auth, _ := z.names()
	var owners []string
	for n := range auth {
		owners = append(owners, n)
	}
	sort.Slice(owners, func(i, j int) bool { return canonLess(owners[i], owners[j]) })
	for i, n := range owners {
		types := append(z.typesAt(n), dns.TypeNSEC, dns.TypeRRSIG)
		sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
		z.rrs = append(z.rrs, &dns.NSEC{
			Hdr:        dns.RR_Header{Name: n, Rrtype: dns.TypeNSEC, Class: dns.ClassINET, Ttl: negTTL},
			NextDomain: owners[(i+1)%len(owners)],
			TypeBitMap: types,
		})
	}
}

func (z *zone) addNSEC3() {
	param := &dns.NSEC3PARAM{
		Hdr:  dns.RR_Header{Name: origin, Rrtype: dns.TypeNSEC3PARAM, Class: dns.ClassINET, Ttl: 0},
		Hash: dns.SHA1, Flags: 0, Iterations: 0, SaltLength: 0, Salt: "",
	}
	z.rrs = append(z.rrs, param)
	auth, ents := z.names()
	type hashed struct{ hash, name string }
	var hs []hashed
	for n := range auth {
		hs = append(hs, hashed{strings.ToLower(dns.HashName(n, dns.SHA1, 0, "")), n})
	}
	for n := range ents {
		hs = append(hs, hashed{strings.ToLower(dns.HashName(n, dns.SHA1, 0, "")), n})
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i].hash < hs[j].hash })
	for i, h := range hs {
		var types []uint16
		if auth[h.name] {
			types = z.typesAt(h.name)
			signed := false
			for _, t := range types {
				if !(z.cuts[h.name] && t == dns.TypeNS) {
					signed = true
				}
			}
			if signed {
				types = append(types, dns.TypeRRSIG)
			}
			sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
		}
		z.rrs = append(z.rrs, &dns.NSEC3{
			Hdr:        dns.RR_Header{Name: h.hash + "." + origin, Rrtype: dns.TypeNSEC3, Class: dns.ClassINET, Ttl: negTTL},
			Hash:       dns.SHA1,
			Iterations: 0,
			SaltLength: 0,
			Salt:       "",
			HashLength: 20,
			NextDomain: strings.ToUpper(hs[(i+1)%len(hs)].hash),
			TypeBitMap: types,
		})
	}
}

func (z *zone) sign() {
	sets := map[rrKey][]dns.RR{}
	var order []rrKey
	for _, rr := range z.rrs {
		if !z.signable(rr) {
			continue
		}
		k := rrKey{rr.Header().Name, rr.Header().Rrtype}
		if _, ok := sets[k]; !ok {
			order = append(order, k)
		}
		sets[k] = append(sets[k], rr)
	}
	for _, k := range order {
		signer := z.zsk
		if k.rtype == dns.TypeDNSKEY {
			signer = z.ksk
		}
		sig := &dns.RRSIG{
			Hdr:        dns.RR_Header{Name: k.owner, Rrtype: dns.TypeRRSIG, Class: dns.ClassINET, Ttl: sets[k][0].Header().Ttl},
			Algorithm:  dns.ED25519,
			Expiration: expire,
			Inception:  inception,
			KeyTag:     signer.pub.KeyTag(),
			SignerName: origin,
		}
		if err := sig.Sign(signer.priv, sets[k]); err != nil {
			log.Fatalf("sign %v: %v", k, err)
		}
		z.rrs = append(z.rrs, sig)
	}
}

type record struct {
	owner []byte
	rtype uint16
	ttl   uint32
	rdata []byte
}

func wireRecord(rr dns.RR) record {
	buf := make([]byte, 65535)
	n, err := dns.PackRR(rr, buf, 0, nil, false)
	if err != nil {
		log.Fatalf("pack %s: %v", rr, err)
	}
	b := buf[:n]
	nameLen := 0
	for b[nameLen] != 0 {
		nameLen += int(b[nameLen]) + 1
	}
	nameLen++
	h := b[nameLen:]
	return record{
		owner: append([]byte(nil), b[:nameLen]...),
		rtype: binary.BigEndian.Uint16(h[0:]),
		ttl:   binary.BigEndian.Uint32(h[4:]),
		rdata: append([]byte(nil), h[10:]...),
	}
}

// encodeFull is the NZF1 full-image layout of mgmt/internal/nzf.EncodeFull.
func encodeFull(rrs []dns.RR) []byte {
	recs := make([]record, 0, len(rrs))
	for _, rr := range rrs {
		recs = append(recs, wireRecord(rr))
	}
	sort.SliceStable(recs, func(i, j int) bool {
		ki, kj := canonKey(dnsName(recs[i].owner)), canonKey(dnsName(recs[j].owner))
		if c := bytes.Compare(ki, kj); c != 0 {
			return c < 0
		}
		if recs[i].rtype != recs[j].rtype {
			return recs[i].rtype < recs[j].rtype
		}
		return bytes.Compare(recs[i].rdata, recs[j].rdata) < 0
	})
	originWire := wireRecord(mustRR(origin + " 0 IN A 192.0.2.1")).owner
	out := []byte("NZF1")
	out = append(out, 1, 0, byte(len(originWire)))
	out = append(out, originWire...)
	out = binary.BigEndian.AppendUint32(out, serial)
	out = binary.BigEndian.AppendUint32(out, 0)
	out = binary.BigEndian.AppendUint32(out, uint32(len(recs)))
	out = binary.BigEndian.AppendUint32(out, 0)
	for _, r := range recs {
		out = append(out, byte(len(r.owner)))
		out = append(out, r.owner...)
		out = binary.BigEndian.AppendUint16(out, r.rtype)
		out = binary.BigEndian.AppendUint16(out, dns.ClassINET)
		out = binary.BigEndian.AppendUint32(out, r.ttl)
		out = binary.BigEndian.AppendUint16(out, uint16(len(r.rdata)))
		out = append(out, r.rdata...)
	}
	return out
}

func dnsName(wire []byte) string {
	name, _, err := dns.UnpackDomainName(wire, 0)
	if err != nil {
		log.Fatal(err)
	}
	return name
}

func main() {
	dir := filepath.Join("testdata", "nzf")
	for file, chain := range map[string]func(*zone){
		"signed-nsec-full.nzf":  (*zone).addNSEC,
		"signed-nsec3-full.nzf": (*zone).addNSEC3,
	} {
		z := newZone()
		chain(z)
		z.sign()
		if err := os.WriteFile(filepath.Join(dir, file), encodeFull(z.rrs), 0o644); err != nil {
			log.Fatal(err)
		}
	}
	anchor := newZone().ksk.pub.String() + "\n"
	if err := os.WriteFile(filepath.Join(dir, "signed-anchor.key"), []byte(anchor), 0o644); err != nil {
		log.Fatal(err)
	}
}
