package authhier

import (
	"bytes"
	"crypto"
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/miekg/dns"
)

type rrKey struct {
	name   string
	rrtype uint16
}

// zone is one built (and optionally signed) zone.
type zone struct {
	spec   ZoneSpec
	origin string
	// sets holds every record by lowercase owner and type: authoritative data, delegation NS and
	// DS, and glue (occluded or out-of-zone names, only ever returned as additional data).
	sets map[rrKey][]dns.RR
	// sigs holds RRSIGs by owner and covered type.
	sigs map[rrKey][]dns.RR
	cuts map[string]bool
	// exists holds the authoritative owner names and empty non-terminals.
	exists map[string]bool
	// denial is the NSEC chain in canonical order or the NSEC3 chain in hash order.
	denial []dns.RR
	soa    *dns.SOA
	ksk    *dns.DNSKEY
}

const dnskeyTTL = 300

// buildZone parses spec's records plus extra (DS records for signed children), and signs the zone
// when spec.Signed.
func buildZone(spec ZoneSpec, extra []dns.RR, now time.Time) (*zone, error) {
	z := &zone{spec: spec, origin: dns.CanonicalName(spec.Origin), sets: map[rrKey][]dns.RR{},
		sigs: map[rrKey][]dns.RR{}, cuts: map[string]bool{}, exists: map[string]bool{}}
	add := func(rr dns.RR) {
		h := rr.Header()
		h.Name = dns.CanonicalName(h.Name)
		k := rrKey{h.Name, h.Rrtype}
		z.sets[k] = append(z.sets[k], rr)
	}
	for _, s := range spec.Records {
		rr, err := dns.NewRR(s)
		if err != nil || rr == nil {
			return nil, fmt.Errorf("zone %s: bad record %q: %v", z.origin, s, err)
		}
		add(rr)
	}
	for _, rr := range extra {
		add(rr)
	}
	soa := z.sets[rrKey{z.origin, dns.TypeSOA}]
	if len(soa) != 1 {
		return nil, fmt.Errorf("zone %s: needs exactly one apex SOA", z.origin)
	}
	z.soa = soa[0].(*dns.SOA)
	for k := range z.sets {
		if k.rrtype == dns.TypeNS && k.name != z.origin && dns.IsSubDomain(z.origin, k.name) {
			z.cuts[k.name] = true
		}
	}
	for k := range z.sets {
		if !z.inZone(k.name) {
			continue
		}
		for n := k.name; ; n = parentName(n) {
			z.exists[n] = true
			if n == z.origin {
				break
			}
		}
	}
	if !spec.Signed {
		return z, nil
	}
	if err := z.sign(now); err != nil {
		return nil, fmt.Errorf("zone %s: %w", z.origin, err)
	}
	return z, nil
}

// inZone reports whether name is at or below the origin and not below a delegation (the cut
// itself counts: its DS and denial records are authoritative).
func (z *zone) inZone(name string) bool {
	return dns.IsSubDomain(z.origin, name) && (z.cutAbove(name) == "" || z.cutAbove(name) == name)
}

// cutAbove returns the topmost delegation point at or above name, or "".
func (z *zone) cutAbove(name string) string {
	labels := dns.SplitDomainName(name)
	for i := len(labels) - dns.CountLabel(z.origin) - 1; i >= 0; i-- {
		if c := dns.Fqdn(strings.Join(labels[i:], ".")); z.cuts[c] {
			return c
		}
	}
	return ""
}

// signable reports whether the RRset k is authoritative data that carries signatures: everything
// in the zone except delegation NS and glue.
func (z *zone) signable(k rrKey) bool {
	if !z.inZone(k.name) {
		return false
	}
	return !z.cuts[k.name] || k.rrtype == dns.TypeDS
}

func (z *zone) sign(now time.Time) error {
	ksk, kskPriv, err := newKey(z.origin, 257)
	if err != nil {
		return err
	}
	zsk, zskPriv, err := newKey(z.origin, 256)
	if err != nil {
		return err
	}
	z.ksk = ksk
	z.sets[rrKey{z.origin, dns.TypeDNSKEY}] = []dns.RR{ksk, zsk}
	nsec3 := z.spec.NSEC3Iterations >= 0
	if nsec3 {
		z.sets[rrKey{z.origin, dns.TypeNSEC3PARAM}] = []dns.RR{&dns.NSEC3PARAM{
			Hdr:  dns.RR_Header{Name: z.origin, Rrtype: dns.TypeNSEC3PARAM, Class: dns.ClassINET},
			Hash: dns.SHA1, Iterations: uint16(z.spec.NSEC3Iterations)}}
	}
	sign := func(k rrKey, rrset []dns.RR, key *dns.DNSKEY, priv crypto.Signer) error {
		sig := &dns.RRSIG{
			Hdr:        dns.RR_Header{Ttl: rrset[0].Header().Ttl},
			Algorithm:  key.Algorithm,
			KeyTag:     key.KeyTag(),
			SignerName: z.origin,
			Inception:  uint32(now.Add(-time.Hour).Unix()),
			Expiration: uint32(now.Add(24 * time.Hour).Unix()),
			OrigTtl:    rrset[0].Header().Ttl,
		}
		if err := sig.Sign(priv, rrset); err != nil {
			return fmt.Errorf("sign %s %s: %w", k.name, dns.TypeToString[k.rrtype], err)
		}
		if z.spec.BreakSignatures && (k.rrtype == dns.TypeA || k.rrtype == dns.TypeAAAA || k.rrtype == dns.TypeTXT) {
			raw, err := base64.StdEncoding.DecodeString(sig.Signature)
			if err != nil {
				return err
			}
			raw[len(raw)-1] ^= 0xFF
			sig.Signature = base64.StdEncoding.EncodeToString(raw)
		}
		z.sigs[k] = []dns.RR{sig}
		return nil
	}
	for k, rrset := range z.sets {
		if !z.signable(k) {
			continue
		}
		key, priv := zsk, zskPriv
		if k.rrtype == dns.TypeDNSKEY {
			key, priv = ksk, kskPriv
		}
		if err := sign(k, rrset, key, priv); err != nil {
			return err
		}
	}
	if nsec3 {
		z.buildNSEC3()
	} else {
		z.buildNSEC()
	}
	for _, rr := range z.denial {
		h := rr.Header()
		if err := sign(rrKey{dns.CanonicalName(h.Name), h.Rrtype}, []dns.RR{rr}, zsk, zskPriv); err != nil {
			return err
		}
	}
	return nil
}

func newKey(origin string, flags uint16) (*dns.DNSKEY, crypto.Signer, error) {
	k := &dns.DNSKEY{Hdr: dns.RR_Header{Name: origin, Rrtype: dns.TypeDNSKEY, Class: dns.ClassINET, Ttl: dnskeyTTL},
		Flags: flags, Protocol: 3, Algorithm: dns.ECDSAP256SHA256}
	priv, err := k.Generate(256)
	if err != nil {
		return nil, nil, err
	}
	return k, priv.(crypto.Signer), nil
}

// typesAt lists the types a denial record's bitmap names for an owner (delegation points: NS and
// DS only).
func (z *zone) typesAt(name string) []uint16 {
	var types []uint16
	for k := range z.sets {
		if k.name == name && z.inZone(name) && (!z.cuts[name] || k.rrtype == dns.TypeNS || k.rrtype == dns.TypeDS) {
			types = append(types, k.rrtype)
		}
	}
	return types
}

// owners lists the zone's authoritative owner names (delegation points included, empty
// non-terminals excluded).
func (z *zone) owners() []string {
	var out []string
	for n := range z.exists {
		if len(z.typesAt(n)) > 0 {
			out = append(out, n)
		}
	}
	return out
}

func (z *zone) negativeTTL() uint32 {
	return min(z.soa.Hdr.Ttl, z.soa.Minttl)
}

func (z *zone) buildNSEC() {
	names := z.owners()
	slices.SortFunc(names, func(a, b string) int {
		if canonicalLess(a, b) {
			return -1
		}
		return 1
	})
	for i, n := range names {
		types := append(z.typesAt(n), dns.TypeRRSIG, dns.TypeNSEC)
		slices.Sort(types)
		z.denial = append(z.denial, &dns.NSEC{
			Hdr:        dns.RR_Header{Name: n, Rrtype: dns.TypeNSEC, Class: dns.ClassINET, Ttl: z.negativeTTL()},
			NextDomain: names[(i+1)%len(names)], TypeBitMap: types})
	}
}

func (z *zone) buildNSEC3() {
	type hashed struct {
		hash, name string
	}
	var hs []hashed
	for n := range z.exists {
		hs = append(hs, hashed{z.hash(n), n})
	}
	slices.SortFunc(hs, func(a, b hashed) int { return strings.Compare(a.hash, b.hash) })
	for i, h := range hs {
		types := z.typesAt(h.name)
		if len(types) > 0 && (!z.cuts[h.name] || slices.Contains(types, dns.TypeDS)) {
			types = append(types, dns.TypeRRSIG)
		}
		slices.Sort(types)
		z.denial = append(z.denial, &dns.NSEC3{
			Hdr:  dns.RR_Header{Name: h.hash + "." + z.origin, Rrtype: dns.TypeNSEC3, Class: dns.ClassINET, Ttl: z.negativeTTL()},
			Hash: dns.SHA1, Iterations: uint16(z.spec.NSEC3Iterations), HashLength: 20,
			NextDomain: hs[(i+1)%len(hs)].hash, TypeBitMap: types})
	}
}

func (z *zone) hash(name string) string {
	return dns.HashName(name, dns.SHA1, uint16(z.spec.NSEC3Iterations), "")
}

// dsText renders a DS as "<tag> <alg> <digest type> <HEX digest>".
func dsText(ds *dns.DS) string {
	return strconv.Itoa(int(ds.KeyTag)) + " " + strconv.Itoa(int(ds.Algorithm)) + " " +
		strconv.Itoa(int(ds.DigestType)) + " " + strings.ToUpper(ds.Digest)
}

func parentName(name string) string {
	if name == "." {
		return "."
	}
	if i, end := dns.NextLabel(name, 0); !end {
		return name[i:]
	}
	return "."
}

// canonicalLess orders names as RFC 4034 section 6.1 does.
func canonicalLess(a, b string) bool {
	la, lb := dns.SplitDomainName(strings.ToLower(a)), dns.SplitDomainName(strings.ToLower(b))
	for i, j := len(la)-1, len(lb)-1; i >= 0 && j >= 0; i, j = i-1, j-1 {
		x, y := unescape(la[i]), unescape(lb[j])
		if c := bytes.Compare(x, y); c != 0 {
			return c < 0
		}
	}
	return len(la) < len(lb)
}

// unescape turns the \DDD and \X escapes of a presentation-format label into raw octets.
func unescape(label string) []byte {
	out := make([]byte, 0, len(label))
	for i := 0; i < len(label); i++ {
		if label[i] != '\\' || i+1 >= len(label) {
			out = append(out, label[i])
			continue
		}
		if i+3 < len(label) && isDigit(label[i+1]) && isDigit(label[i+2]) && isDigit(label[i+3]) {
			v, _ := strconv.Atoi(label[i+1 : i+4])
			out = append(out, byte(v))
			i += 3
			continue
		}
		out = append(out, label[i+1])
		i++
	}
	return out
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
