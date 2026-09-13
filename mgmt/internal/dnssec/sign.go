// Package dnssec signs hosted primary zones online: it keeps KSK/ZSK signing keys in
// mgmt/internal/secrets (sealed under the KEK, or inside the PKCS#11 token where signing happens),
// generates the DNSKEY/CDS/CDNSKEY RRsets and the NSEC or NSEC3 chain (RFC 9276: SHA-1, zero
// iterations, empty salt, no opt-out), and signs RRsets, reusing still-fresh signatures of unchanged
// RRsets so an edit re-signs only what it touched. It is distinct from M3's dnssecconf, which
// validates resolver DNSSEC settings.
package dnssec

import (
	"crypto"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// Signature timing: signatures are valid for Validity from InceptionSkew before signing (minus up
// to an hour of per-RRset jitter) and are renewed once less than RefreshBefore remains.
const (
	Validity      = 14 * 24 * time.Hour
	RefreshBefore = 7 * 24 * time.Hour
	InceptionSkew = time.Hour
	// refreshWindow is the jitter range: a pass renews every signature whose refresh point falls
	// within the next hour too, so one maintenance run renews a whole batch of signatures made
	// together instead of one new zone version per jittered expiration.
	refreshWindow = time.Hour
)

// Key is one DNSSEC key as the signer uses it. Signs marks keys whose signatures are generated
// (active keys); every key passed in is published in the DNSKEY RRset; InCDS keys also appear in
// CDS and CDNSKEY.
type Key struct {
	ID        string
	Role      string // ksk|zsk
	Algorithm uint8
	DNSKEY    *dns.DNSKEY
	Signer    crypto.Signer
	Signs     bool
	InCDS     bool
}

// SigKey identifies one cached signature: lowercase owner, covered type and key tag.
type SigKey struct {
	Owner  string
	Type   uint16
	KeyTag uint16
}

// CachedSig is a signature with the digest of the RRset it covers.
type CachedSig struct {
	Digest [32]byte
	RRSIG  *dns.RRSIG
}

// Input is a zone to sign: Records are the unsigned records (SOA included; DNSSEC records in it
// are ignored and regenerated), Cache the signatures of the previous pass.
type Input struct {
	Origin  string
	Records []dns.RR
	Keys    []Key
	NSEC3   bool
	Cache   map[SigKey]CachedSig
	Now     time.Time
}

// Output is the signed zone: Served holds every record to serve, Cache every signature in it,
// NextRefresh the earliest instant a signature needs renewing, and Reused/Created count signatures.
type Output struct {
	Served          []dns.RR
	Cache           map[SigKey]CachedSig
	NextRefresh     time.Time
	Reused, Created int
}

// generatedTypes are produced by the signer; input records of these types are dropped.
var generatedTypes = map[uint16]bool{
	dns.TypeDNSKEY: true, dns.TypeRRSIG: true, dns.TypeNSEC: true, dns.TypeNSEC3: true,
	dns.TypeNSEC3PARAM: true, dns.TypeCDS: true, dns.TypeCDNSKEY: true,
}

type setKey struct {
	owner string
	rtype uint16
}

// zoneSets groups records by (lowercase owner, type), dropping exact duplicates.
type zoneSets struct {
	sets map[setKey][]dns.RR
	seen map[string]bool
}

func (r *zoneSets) add(rr dns.RR) error {
	w, err := packCanonical(rr)
	if err != nil {
		return err
	}
	if r.seen[string(w)] {
		return nil
	}
	r.seen[string(w)] = true
	k := setKey{strings.ToLower(rr.Header().Name), rr.Header().Rrtype}
	r.sets[k] = append(r.sets[k], rr)
	return nil
}

func hdr(name string, rtype uint16, ttl uint32) dns.RR_Header {
	return dns.RR_Header{Name: name, Rrtype: rtype, Class: dns.ClassINET, Ttl: ttl}
}

// Sign signs a zone. Every authoritative RRset is signed except NS at delegations; occluded names
// are never signed; DNSKEY, CDS and CDNSKEY are signed by the active KSKs, everything else by the
// active ZSKs.
func Sign(in Input) (*Output, error) {
	origin := dns.CanonicalName(in.Origin)
	z := &zoneSets{sets: map[setKey][]dns.RR{}, seen: map[string]bool{}}
	var soa *dns.SOA
	for _, rr := range in.Records {
		h := rr.Header()
		if generatedTypes[h.Rrtype] {
			continue
		}
		if !dns.IsSubDomain(origin, strings.ToLower(h.Name)) {
			return nil, fmt.Errorf("record %s is outside zone %s", h.Name, origin)
		}
		if s, ok := rr.(*dns.SOA); ok {
			if strings.ToLower(h.Name) != origin {
				return nil, fmt.Errorf("SOA at %s is not at the apex of %s", h.Name, origin)
			}
			soa = s
		}
		if err := z.add(rr); err != nil {
			return nil, err
		}
	}
	if soa == nil {
		return nil, fmt.Errorf("zone %s has no SOA", origin)
	}
	negTTL := min(soa.Hdr.Ttl, soa.Minttl)
	for _, k := range in.Keys {
		dnskey := *k.DNSKEY
		dnskey.Hdr = hdr(origin, dns.TypeDNSKEY, soa.Hdr.Ttl)
		if err := z.add(&dnskey); err != nil {
			return nil, err
		}
		if !k.InCDS {
			continue
		}
		ds := dnskey.ToDS(dns.SHA256)
		if ds == nil {
			return nil, fmt.Errorf("key %s: no DS for algorithm %d", k.ID, dnskey.Algorithm)
		}
		cds := ds.ToCDS()
		cds.Hdr = hdr(origin, dns.TypeCDS, soa.Hdr.Ttl)
		cdnskey := dnskey.ToCDNSKEY()
		cdnskey.Hdr = hdr(origin, dns.TypeCDNSKEY, soa.Hdr.Ttl)
		if err := z.add(cds); err != nil {
			return nil, err
		}
		if err := z.add(cdnskey); err != nil {
			return nil, err
		}
	}
	if in.NSEC3 {
		if err := z.add(&dns.NSEC3PARAM{Hdr: hdr(origin, dns.TypeNSEC3PARAM, negTTL), Hash: dns.SHA1}); err != nil {
			return nil, err
		}
	}

	cuts := cutAnalysis{origin: origin, cuts: map[string]bool{}}
	for k := range z.sets {
		if k.rtype == dns.TypeNS && k.owner != origin {
			cuts.cuts[k.owner] = true
		}
	}
	// Authoritative types per owner: at a delegation only NS and DS are the zone's data.
	types := map[string][]uint16{}
	for k := range z.sets {
		if cuts.occluded(k.owner) || (cuts.cuts[k.owner] && k.rtype != dns.TypeNS && k.rtype != dns.TypeDS) {
			continue
		}
		types[k.owner] = append(types[k.owner], k.rtype)
	}
	owners := make([]string, 0, len(types))
	for o := range types {
		slices.Sort(types[o])
		owners = append(owners, o)
	}
	if err := sortCanonical(owners); err != nil {
		return nil, err
	}
	if in.NSEC3 {
		if err := addNSEC3(z, origin, owners, types, cuts, negTTL); err != nil {
			return nil, err
		}
	} else {
		for i, o := range owners {
			bitmap := append(slices.Clone(types[o]), dns.TypeRRSIG, dns.TypeNSEC)
			slices.Sort(bitmap)
			if err := z.add(&dns.NSEC{Hdr: hdr(o, dns.TypeNSEC, negTTL), NextDomain: owners[(i+1)%len(owners)], TypeBitMap: bitmap}); err != nil {
				return nil, err
			}
		}
	}

	order := make([]setKey, 0, len(z.sets))
	canon := map[string][]byte{}
	for k := range z.sets {
		if _, ok := canon[k.owner]; !ok {
			c, err := canonKey(k.owner)
			if err != nil {
				return nil, err
			}
			canon[k.owner] = c
		}
		order = append(order, k)
	}
	slices.SortFunc(order, func(a, b setKey) int {
		if c := strings.Compare(string(canon[a.owner]), string(canon[b.owner])); c != 0 {
			return c
		}
		return int(a.rtype) - int(b.rtype)
	})
	out := &Output{Cache: make(map[SigKey]CachedSig, len(in.Cache))}
	for _, k := range order {
		set := z.sets[k]
		out.Served = append(out.Served, set...)
		if cuts.occluded(k.owner) || (cuts.cuts[k.owner] && k.rtype != dns.TypeDS && k.rtype != dns.TypeNSEC) {
			continue
		}
		sigs, err := signSet(origin, k, set, in.Keys, in.Cache, in.Now, out)
		if err != nil {
			return nil, err
		}
		out.Served = append(out.Served, sigs...)
	}
	return out, nil
}

// addNSEC3 adds the NSEC3 chain over the authoritative owners and the empty non-terminals between
// them and the apex.
func addNSEC3(z *zoneSets, origin string, owners []string, types map[string][]uint16, cuts cutAnalysis, negTTL uint32) error {
	names := slices.Clone(owners)
	for _, o := range owners {
		for p := parent(o); len(p) > len(origin); p = parent(p) {
			if _, ok := types[p]; !ok {
				types[p] = nil // empty non-terminal
				names = append(names, p)
			}
		}
	}
	type hashed struct{ hash, name string }
	hs := make([]hashed, 0, len(names))
	for _, n := range names {
		h := strings.ToLower(dns.HashName(n, dns.SHA1, 0, ""))
		if h == "" {
			return fmt.Errorf("NSEC3 hash of %s failed", n)
		}
		hs = append(hs, hashed{h, n})
	}
	slices.SortFunc(hs, func(a, b hashed) int { return strings.Compare(a.hash, b.hash) })
	for i, h := range hs {
		if i > 0 && hs[i-1].hash == h.hash {
			return fmt.Errorf("NSEC3 hash collision between %s and %s", hs[i-1].name, h.name)
		}
		var bitmap []uint16
		if ts := types[h.name]; len(ts) > 0 {
			bitmap = slices.Clone(ts)
			// RRSIG is listed when the owner has a signed RRset: everything but NS at a delegation.
			if !cuts.cuts[h.name] || slices.Contains(ts, dns.TypeDS) {
				bitmap = append(bitmap, dns.TypeRRSIG)
				slices.Sort(bitmap)
			}
		}
		err := z.add(&dns.NSEC3{
			Hdr:  hdr(h.hash+"."+origin, dns.TypeNSEC3, negTTL),
			Hash: dns.SHA1, HashLength: 20,
			NextDomain: strings.ToUpper(hs[(i+1)%len(hs)].hash),
			TypeBitMap: bitmap,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// signSet returns the signatures of set by every signing key of its role, reusing a cached
// signature when it covers the same RRset data and is not due for refresh, and records them in out.
func signSet(origin string, k setKey, set []dns.RR, keys []Key, cache map[SigKey]CachedSig, now time.Time, out *Output) ([]dns.RR, error) {
	role := "zsk"
	if k.rtype == dns.TypeDNSKEY || k.rtype == dns.TypeCDS || k.rtype == dns.TypeCDNSKEY {
		role = "ksk"
	}
	digest, err := rrsetDigest(set)
	if err != nil {
		return nil, err
	}
	var sigs []dns.RR
	for _, key := range keys {
		if !key.Signs || key.Role != role {
			continue
		}
		sk := SigKey{Owner: k.owner, Type: k.rtype, KeyTag: key.DNSKEY.KeyTag()}
		c, ok := cache[sk]
		sig := c.RRSIG
		if ok && sig != nil && c.Digest == digest && sig.Algorithm == key.Algorithm && reusable(sig, now) {
			out.Reused++
		} else {
			if sig, err = newSig(origin, set, key, now); err != nil {
				return nil, err
			}
			out.Created++
		}
		out.Cache[sk] = CachedSig{Digest: digest, RRSIG: sig}
		if refresh := time.Unix(int64(sig.Expiration), 0).Add(-RefreshBefore); out.NextRefresh.IsZero() || refresh.Before(out.NextRefresh) {
			out.NextRefresh = refresh
		}
		sigs = append(sigs, sig)
	}
	if len(sigs) == 0 {
		return nil, fmt.Errorf("zone %s: no active %s to sign %s/%s", origin, strings.ToUpper(role), k.owner, dns.TypeToString[k.rtype])
	}
	return sigs, nil
}

func reusable(sig *dns.RRSIG, now time.Time) bool {
	return time.Unix(int64(sig.Expiration), 0).Sub(now) > RefreshBefore+refreshWindow && !time.Unix(int64(sig.Inception), 0).After(now)
}

func newSig(origin string, set []dns.RR, k Key, now time.Time) (*dns.RRSIG, error) {
	h := fnv.New32a()
	h.Write([]byte(strings.ToLower(set[0].Header().Name)))
	_ = binary.Write(h, binary.BigEndian, set[0].Header().Rrtype)
	jitter := time.Duration(h.Sum32()%3600) * time.Second
	sig := &dns.RRSIG{
		Hdr:        hdr(set[0].Header().Name, dns.TypeRRSIG, set[0].Header().Ttl),
		Algorithm:  k.Algorithm,
		OrigTtl:    set[0].Header().Ttl,
		KeyTag:     k.DNSKEY.KeyTag(),
		SignerName: origin,
		Inception:  uint32(now.Add(-InceptionSkew).Unix()),
		Expiration: uint32(now.Add(Validity - jitter).Unix()),
	}
	if err := sig.Sign(k.Signer, set); err != nil {
		return nil, fmt.Errorf("sign %s/%s with key %d: %w", set[0].Header().Name, dns.TypeToString[set[0].Header().Rrtype], sig.KeyTag, err)
	}
	return sig, nil
}
