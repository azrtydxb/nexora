// Package zonemd computes and verifies RFC 8976 zone digests (ZONEMD, scheme SIMPLE).
package zonemd

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"sort"
	"strings"

	"github.com/miekg/dns"
)

// Mode is the verification setting of a secondary or RPZ transfer zone.
type Mode string

const (
	ModeOff       Mode = "off"
	ModeIfPresent Mode = "if_present"
	ModeRequired  Mode = "required"
)

// Status is the outcome of a verification.
type Status string

const (
	StatusOff      Status = "off"
	StatusAbsent   Status = "absent"
	StatusVerified Status = "verified"
	StatusFailed   Status = "failed"
)

// Result is a verification outcome; Err names the reason when Status is failed.
type Result struct {
	Status Status
	Err    string
}

// ZONEMD scheme and hash algorithm numbers (RFC 8976 §5).
const (
	SchemeSimple uint8 = 1
	HashSHA384   uint8 = 1
	HashSHA512   uint8 = 2
)

// Digest returns the SIMPLE-scheme digest of the zone at origin over rrs with the given hash.
func Digest(origin string, rrs []dns.RR, hashAlg uint8) ([]byte, error) {
	var h hash.Hash
	switch hashAlg {
	case HashSHA384:
		h = sha512.New384()
	case HashSHA512:
		h = sha512.New()
	default:
		return nil, fmt.Errorf("unsupported ZONEMD hash %d", hashAlg)
	}
	wire, err := canonical(origin, rrs)
	if err != nil {
		return nil, err
	}
	for _, w := range wire {
		h.Write(w.packed)
	}
	return h.Sum(nil), nil
}

// Verify checks the apex ZONEMD records of the zone at origin against rrs (RFC 8976 §4).
func Verify(origin string, rrs []dns.RR, mode Mode) Result {
	if mode != ModeIfPresent && mode != ModeRequired {
		return Result{Status: StatusOff}
	}
	origin = dns.CanonicalName(origin)
	var soa *dns.SOA
	var apex []*dns.ZONEMD
	for _, rr := range rrs {
		if dns.CanonicalName(rr.Header().Name) != origin {
			continue
		}
		switch r := rr.(type) {
		case *dns.SOA:
			soa = r
		case *dns.ZONEMD:
			apex = append(apex, r)
		}
	}
	if len(apex) == 0 {
		if mode == ModeRequired {
			return Result{Status: StatusFailed, Err: "no apex ZONEMD"}
		}
		return Result{Status: StatusAbsent}
	}
	if soa == nil {
		return Result{Status: StatusFailed, Err: "no apex SOA"}
	}
	type tuple struct{ scheme, hash uint8 }
	seen := map[tuple]int{}
	for _, z := range apex {
		seen[tuple{z.Scheme, z.Hash}]++
	}
	reason := "no supported ZONEMD"
	digests := map[uint8][]byte{}
	for _, z := range apex {
		if seen[tuple{z.Scheme, z.Hash}] > 1 {
			reason = "duplicate scheme and hash"
			continue
		}
		if z.Serial != soa.Serial {
			reason = "serial mismatch"
			continue
		}
		if z.Scheme != SchemeSimple || (z.Hash != HashSHA384 && z.Hash != HashSHA512) {
			continue
		}
		want, err := hex.DecodeString(z.Digest)
		if err != nil || len(want) < 12 || (z.Hash == HashSHA384 && len(want) != 48) || (z.Hash == HashSHA512 && len(want) != 64) {
			reason = "digest mismatch"
			continue
		}
		got, ok := digests[z.Hash]
		if !ok {
			if got, err = Digest(origin, rrs, z.Hash); err != nil {
				reason = err.Error()
				continue
			}
			digests[z.Hash] = got
		}
		if hmac.Equal(got, want) {
			return Result{Status: StatusVerified}
		}
		reason = "digest mismatch"
	}
	return Result{Status: StatusFailed, Err: reason}
}

// Placeholder returns an apex ZONEMD (SIMPLE, SHA-384) with an all-zero digest, to be filled by Apply.
func Placeholder(origin string, serial, ttl uint32) *dns.ZONEMD {
	return &dns.ZONEMD{
		Hdr:    dns.RR_Header{Name: dns.CanonicalName(origin), Rrtype: dns.TypeZONEMD, Class: dns.ClassINET, Ttl: ttl},
		Serial: serial,
		Scheme: SchemeSimple,
		Hash:   HashSHA384,
		Digest: strings.Repeat("0", 96),
	}
}

// Apply sets the serial (from the apex SOA) and the SHA-384 digest of the apex ZONEMD in rrs, in place.
func Apply(origin string, rrs []dns.RR) error {
	canon := dns.CanonicalName(origin)
	var soa *dns.SOA
	var z *dns.ZONEMD
	for _, rr := range rrs {
		if dns.CanonicalName(rr.Header().Name) != canon {
			continue
		}
		switch r := rr.(type) {
		case *dns.SOA:
			soa = r
		case *dns.ZONEMD:
			if z == nil {
				z = r
			}
		}
	}
	if z == nil {
		return errors.New("zonemd: no apex ZONEMD")
	}
	if soa == nil {
		return errors.New("zonemd: no apex SOA")
	}
	z.Serial, z.Scheme, z.Hash = soa.Serial, SchemeSimple, HashSHA384
	d, err := Digest(origin, rrs, HashSHA384)
	if err != nil {
		return err
	}
	z.Digest = hex.EncodeToString(d)
	return nil
}

type canonRR struct {
	packed []byte
	labels [][]byte // owner labels, root first
	rrtype uint16
	class  uint16
	rdata  []byte
}

// canonical returns the zone's RRs in RFC 8976 §3.3 canonical wire form and order: owners at or
// below origin, apex ZONEMD and apex RRSIG(ZONEMD) excluded, owner and RFC 4034 §6.2 RDATA names
// lowercased, sorted by owner, type and RDATA, duplicates once.
func canonical(origin string, rrs []dns.RR) ([]canonRR, error) {
	origin = dns.CanonicalName(origin)
	out := make([]canonRR, 0, len(rrs))
	buf := make([]byte, 65535+512)
	for _, rr := range rrs {
		owner := dns.CanonicalName(rr.Header().Name)
		if !dns.IsSubDomain(origin, owner) {
			continue
		}
		if owner == origin {
			if rr.Header().Rrtype == dns.TypeZONEMD {
				continue
			}
			if s, ok := rr.(*dns.RRSIG); ok && s.TypeCovered == dns.TypeZONEMD {
				continue
			}
		}
		c := dns.Copy(rr)
		c.Header().Name = owner
		lowerRdataNames(c)
		n, err := dns.PackRR(c, buf, 0, nil, false)
		if err != nil {
			return nil, fmt.Errorf("zonemd: pack %s: %w", c.Header().Name, err)
		}
		packed := append([]byte(nil), buf[:n]...)
		ownerLen, err := wireNameLen(packed)
		if err != nil || ownerLen+10 > n {
			return nil, fmt.Errorf("zonemd: malformed wire form of %s", c.Header().Name)
		}
		// Lowercase the owner octets too, which also covers escaped letters (\065); label length
		// octets are at most 63 and never in 'A'..'Z'.
		for i := 0; i < ownerLen; i++ {
			if packed[i] >= 'A' && packed[i] <= 'Z' {
				packed[i] += 'a' - 'A'
			}
		}
		out = append(out, canonRR{
			packed: packed,
			labels: ownerLabels(packed[:ownerLen]),
			rrtype: c.Header().Rrtype,
			class:  c.Header().Class,
			rdata:  packed[ownerLen+10:],
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if k := compareOwners(a.labels, b.labels); k != 0 {
			return k < 0
		}
		if a.rrtype != b.rrtype {
			return a.rrtype < b.rrtype
		}
		if a.class != b.class {
			return a.class < b.class
		}
		return bytes.Compare(a.rdata, b.rdata) < 0
	})
	uniq := out[:0]
	for i, r := range out {
		if i > 0 {
			p := uniq[len(uniq)-1]
			if compareOwners(p.labels, r.labels) == 0 && p.rrtype == r.rrtype && p.class == r.class && bytes.Equal(p.rdata, r.rdata) {
				continue
			}
		}
		uniq = append(uniq, r)
	}
	return uniq, nil
}

// wireNameLen returns the length of the uncompressed wire name at the start of b.
func wireNameLen(b []byte) (int, error) {
	i := 0
	for i < len(b) {
		l := int(b[i])
		if l == 0 {
			return i + 1, nil
		}
		if l > 63 {
			return 0, errors.New("compressed or invalid label")
		}
		i += 1 + l
	}
	return 0, errors.New("truncated name")
}

// ownerLabels returns the labels of an uncompressed wire name, root first (RFC 4034 §6.1 order).
func ownerLabels(wire []byte) [][]byte {
	var labels [][]byte
	for i := 0; wire[i] != 0; i += 1 + int(wire[i]) {
		labels = append(labels, wire[i+1:i+1+int(wire[i])])
	}
	for i, j := 0, len(labels)-1; i < j; i, j = i+1, j-1 {
		labels[i], labels[j] = labels[j], labels[i]
	}
	return labels
}

// compareOwners orders names by RFC 4034 §6.1: labels compared from the root as unsigned octets, a
// name sorting before the names below it.
func compareOwners(a, b [][]byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := bytes.Compare(a[i], b[i]); c != 0 {
			return c
		}
	}
	return len(a) - len(b)
}

// lowerRdataNames lowercases the RDATA domain names of the RFC 4034 §6.2 types (as corrected by
// RFC 6840 §5.1: not NSEC, not HINFO).
func lowerRdataNames(rr dns.RR) {
	l := strings.ToLower
	switch r := rr.(type) {
	case *dns.NS:
		r.Ns = l(r.Ns)
	case *dns.MD:
		r.Md = l(r.Md)
	case *dns.MF:
		r.Mf = l(r.Mf)
	case *dns.CNAME:
		r.Target = l(r.Target)
	case *dns.SOA:
		r.Ns, r.Mbox = l(r.Ns), l(r.Mbox)
	case *dns.MB:
		r.Mb = l(r.Mb)
	case *dns.MG:
		r.Mg = l(r.Mg)
	case *dns.MR:
		r.Mr = l(r.Mr)
	case *dns.PTR:
		r.Ptr = l(r.Ptr)
	case *dns.MINFO:
		r.Rmail, r.Email = l(r.Rmail), l(r.Email)
	case *dns.MX:
		r.Mx = l(r.Mx)
	case *dns.RP:
		r.Mbox, r.Txt = l(r.Mbox), l(r.Txt)
	case *dns.AFSDB:
		r.Hostname = l(r.Hostname)
	case *dns.RT:
		r.Host = l(r.Host)
	case *dns.SIG:
		r.SignerName = l(r.SignerName)
	case *dns.RRSIG:
		r.SignerName = l(r.SignerName)
	case *dns.PX:
		r.Map822, r.Mapx400 = l(r.Map822), l(r.Mapx400)
	case *dns.NXT:
		r.NextDomain = l(r.NextDomain)
	case *dns.NAPTR:
		r.Replacement = l(r.Replacement)
	case *dns.KX:
		r.Exchanger = l(r.Exchanger)
	case *dns.SRV:
		r.Target = l(r.Target)
	case *dns.DNAME:
		r.Target = l(r.Target)
	}
}
