package zone

import (
	"fmt"
	"sort"
	"strings"

	"github.com/miekg/dns"
)

const maxTTL = 2147483647

// ParseRecord validates one operator record against zone zoneName and returns the parsed RR and
// the normalised presentation RDATA.
func ParseRecord(zoneName string, in RecordInput) (dns.RR, string, error) {
	name := in.Name
	if _, ok := dns.IsDomainName(name); !ok || !dns.IsFqdn(name) {
		return nil, "", invalid("invalid_name", fmt.Sprintf("%q is not an absolute domain name", name))
	}
	if !dns.IsSubDomain(zoneName, name) {
		return nil, "", invalid("out_of_zone", fmt.Sprintf("%s is outside %s", name, zoneName))
	}
	typ, ok := dns.StringToType[strings.ToUpper(in.Type)]
	if !ok || !ManagedTypes[typ] {
		return nil, "", invalid("unsupported_type", fmt.Sprintf("unsupported record type %s", in.Type))
	}
	if in.TTL > maxTTL {
		return nil, "", invalid("invalid_ttl", fmt.Sprintf("ttl %d exceeds %d", in.TTL, maxTTL))
	}
	if strings.ContainsAny(in.Data, "\n\r;") || strings.TrimSpace(in.Data) == "" {
		return nil, "", invalid("invalid_rdata", "data must be one non-empty line without comments")
	}
	rr, err := dns.NewRR(fmt.Sprintf("%s %d IN %s %s", name, in.TTL, dns.TypeToString[typ], in.Data))
	if err != nil {
		return nil, "", invalid("invalid_rdata", err.Error())
	}
	if rr == nil || rr.Header().Rrtype != typ {
		return nil, "", invalid("invalid_rdata", "data does not form a "+dns.TypeToString[typ]+" record")
	}
	return rr, RDataText(rr), nil
}

// RDataText is the presentation RDATA of rr (rr.String() after the fourth tab).
func RDataText(rr dns.RR) string {
	parts := strings.SplitN(rr.String(), "\t", 5)
	if len(parts) < 5 {
		return ""
	}
	return parts[4]
}

// CheckSet applies the whole-zone rules to the records of zone zoneName (SOA excluded): CNAME
// exclusivity, DNAME occlusion, DS only at delegations and at least one apex NS.
func CheckSet(zoneName string, rrs []dns.RR) error {
	types := ownerTypes{}
	owners := []string{}
	for _, rr := range rrs {
		o := strings.ToLower(rr.Header().Name)
		if types[o] == nil {
			owners = append(owners, o)
		}
		types.add(o, rr.Header().Rrtype)
	}
	sort.Strings(owners)
	apex := strings.ToLower(zoneName)
	if err := checkApexNS(types[apex][dns.TypeNS]); err != nil {
		return err
	}
	for _, o := range owners {
		if err := checkOwner(apex, o, types); err != nil {
			return err
		}
	}
	return nil
}

// ownerTypes counts records by lowercase owner and type.
type ownerTypes map[string]map[uint16]int

func (t ownerTypes) add(owner string, rtype uint16) {
	if t[owner] == nil {
		t[owner] = map[uint16]int{}
	}
	t[owner][rtype]++
}

// checkApexNS enforces at least one apex NS record, given their count.
func checkApexNS(n int) error {
	if n == 0 {
		return invalid("last_apex_ns", "the zone apex must keep at least one NS record")
	}
	return nil
}

// checkOwner applies the rules of owner o (lowercase, holding records) below apex: CNAME
// exclusivity, one DNAME, DS only at delegations and no DNAME above o. types must hold o and every
// ancestor of o up to the apex.
func checkOwner(apex, o string, types ownerTypes) error {
	t := types[o]
	if n := t[dns.TypeCNAME]; n > 0 && (n > 1 || len(t) > 1 || o == apex) {
		return invalid("cname_conflict", fmt.Sprintf("a CNAME at %s cannot coexist with other records", o))
	}
	if t[dns.TypeDNAME] > 1 {
		return invalid("dname_conflict", fmt.Sprintf("%s has more than one DNAME", o))
	}
	if t[dns.TypeDS] > 0 && (o == apex || t[dns.TypeNS] == 0) {
		return invalid("ds_not_at_delegation", fmt.Sprintf("DS at %s requires a delegation (NS) below the apex", o))
	}
	for _, parent := range ancestors(apex, o) {
		if types[parent][dns.TypeDNAME] > 0 {
			return invalid("dname_occludes", fmt.Sprintf("%s is below the DNAME at %s", o, parent))
		}
	}
	return nil
}

// ancestors returns the names above o (lowercase) down to and including apex.
func ancestors(apex, o string) []string {
	var out []string
	for off, end := dns.NextLabel(o, 0); !end && len(o)-off >= len(apex); off, end = dns.NextLabel(o, off) {
		out = append(out, o[off:])
	}
	return out
}
