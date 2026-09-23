// Package catzone encodes and decodes RFC 9432 catalog zones (schema version 2).
package catzone

import (
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/miekg/dns"
)

// Version is the only catalog schema version produced and accepted.
const Version = "2"

// Member is one member zone of a catalog: its label under zones.<catalog> and
// the absolute, lowercase zone name.
type Member struct{ Label, Zone string }

// BrokenError reports a catalog that RFC 9432 says a consumer must not apply.
type BrokenError struct{ Reason string }

func (e *BrokenError) Error() string { return "broken catalog: " + e.Reason }

func broken(format string, args ...any) error {
	return &BrokenError{Reason: fmt.Sprintf(format, args...)}
}

// Label is the stable member label of a zone: its id as 32 lowercase hex digits.
func Label(zoneID uuid.UUID) string { return hex.EncodeToString(zoneID[:]) }

// Build returns the catalog's apex NS, version TXT and one PTR per member
// (sorted by label), all with TTL 0. The SOA is the caller's.
func Build(catalog string, members []Member) []dns.RR {
	catalog = dns.CanonicalName(catalog)
	sorted := slices.Clone(members)
	slices.SortFunc(sorted, func(a, b Member) int { return strings.Compare(a.Label, b.Label) })
	hdr := func(name string, t uint16) dns.RR_Header {
		return dns.RR_Header{Name: name, Rrtype: t, Class: dns.ClassINET}
	}
	out := make([]dns.RR, 0, len(sorted)+2)
	out = append(out,
		&dns.NS{Hdr: hdr(catalog, dns.TypeNS), Ns: "invalid."},
		&dns.TXT{Hdr: hdr("version."+catalog, dns.TypeTXT), Txt: []string{Version}},
	)
	for _, m := range sorted {
		out = append(out, &dns.PTR{Hdr: hdr(m.Label+".zones."+catalog, dns.TypePTR), Ptr: m.Zone})
	}
	return out
}

// Parse extracts the members of a catalog zone's records, sorted by label, or
// returns a *BrokenError. Properties other than version, member PTRs and coo
// are ignored.
func Parse(catalog string, rrs []dns.RR) ([]Member, error) {
	catalog = dns.CanonicalName(catalog)
	versionOwner, zonesSuffix := "version."+catalog, ".zones."+catalog
	var versions []string
	ptrs := map[string][]string{} // member label -> lowercase targets
	coos := map[string]int{}      // member label -> coo PTR count
	for _, rr := range rrs {
		h := rr.Header()
		if h.Class != dns.ClassINET {
			continue
		}
		owner := strings.ToLower(h.Name)
		if owner == versionOwner {
			if txt, ok := rr.(*dns.TXT); ok {
				versions = append(versions, strings.Join(txt.Txt, ""))
			}
			continue
		}
		ptr, ok := rr.(*dns.PTR)
		if !ok || !strings.HasSuffix(owner, zonesSuffix) {
			continue
		}
		switch labels := dns.SplitDomainName(strings.TrimSuffix(owner, zonesSuffix) + "."); {
		case len(labels) == 1:
			ptrs[labels[0]] = append(ptrs[labels[0]], dns.CanonicalName(ptr.Ptr))
		case len(labels) == 2 && labels[0] == "coo":
			coos[labels[1]]++
		}
	}
	switch {
	case len(versions) == 0:
		return nil, broken("version property missing")
	case len(versions) > 1:
		return nil, broken("version property has %d records", len(versions))
	case versions[0] != Version:
		return nil, broken("unsupported catalog version %q", versions[0])
	}
	members := make([]Member, 0, len(ptrs))
	for _, label := range slices.Sorted(maps.Keys(ptrs)) {
		targets := ptrs[label]
		if len(targets) != 1 {
			return nil, broken("member node %s has %d PTR records", label, len(targets))
		}
		members = append(members, Member{Label: label, Zone: targets[0]})
	}
	labelsPerZone := map[string]int{}
	for _, m := range members {
		if labelsPerZone[m.Zone]++; labelsPerZone[m.Zone] == 2 {
			return nil, broken("member %s listed under 2 labels", m.Zone)
		}
	}
	for _, label := range slices.Sorted(maps.Keys(coos)) {
		if n := coos[label]; n > 1 {
			return nil, broken("coo property of %s has %d records", label, n)
		}
	}
	return members, nil
}
