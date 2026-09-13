package zonefile

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/miekg/dns"
)

// Options restrict what a zone file may contain.
type Options struct {
	AllowedTypes map[uint16]bool // record types accepted besides the apex SOA
	MaxRecords   int
}

// Result is a parsed zone: its SOA separately, every other record in file order.
type Result struct {
	Origin     string
	DefaultTTL uint32 // the first $TTL; 0 when the file has none
	SOA        *dns.SOA
	Records    []dns.RR
}

// LineError is a problem at a line of the file (Line 0: the file as a whole).
type LineError struct {
	Line    int
	Message string
}

// Errors are all problems found in a file.
type Errors []LineError

func (e Errors) Error() string {
	parts := make([]string, len(e))
	for i, le := range e {
		if le.Line > 0 {
			parts[i] = fmt.Sprintf("line %d: %s", le.Line, le.Message)
		} else {
			parts[i] = le.Message
		}
	}
	return strings.Join(parts, "; ")
}

const (
	maxErrors = 100
	maxTTL    = 2147483647
)

var ttlUnits = map[byte]uint64{'s': 1, 'm': 60, 'h': 3600, 'd': 86400, 'w': 604800}

// parseTTL accepts seconds or BIND unit notation (1w2d3h4m5s); ok is false when s is no TTL.
func parseTTL(s string) (ttl uint32, ok bool, err error) {
	if s == "" || s[0] < '0' || s[0] > '9' {
		return 0, false, nil
	}
	var total, num uint64
	digits := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			num = num*10 + uint64(c-'0')
			digits = true
		case digits && ttlUnits[c|0x20] > 0:
			total += num * ttlUnits[c|0x20]
			num, digits = 0, false
		default:
			return 0, false, nil
		}
		if num > maxTTL || total > maxTTL {
			return 0, true, fmt.Errorf("ttl %s exceeds %d", s, maxTTL)
		}
	}
	total += num
	if total > maxTTL {
		return 0, true, fmt.Errorf("ttl %s exceeds %d", s, maxTTL)
	}
	return uint32(total), true, nil
}

// absolute makes name absolute against origin: "@" is the origin; a name not ending in an
// unescaped dot gets ".origin" appended.
func absolute(name, origin string) string {
	if name == "@" {
		return origin
	}
	if strings.HasSuffix(name, ".") {
		slashes := 0
		for i := len(name) - 2; i >= 0 && name[i] == '\\'; i-- {
			slashes++
		}
		if slashes%2 == 0 {
			return name
		}
	}
	if origin == "." {
		return name + "."
	}
	return name + "." + origin
}

var classes = map[string]bool{"IN": true, "CH": true, "CHAOS": true, "HS": true, "CS": true, "ANY": true, "NONE": true}

// Parse reads a BIND master file for zone origin. $INCLUDE and $GENERATE are refused; every record
// must lie inside origin, be of class IN and of an allowed type; exactly one SOA sits at the apex
// and the apex has NS records. All problems are returned together as Errors.
func Parse(r io.Reader, origin string, opts Options) (*Result, error) {
	origin = dns.CanonicalName(origin)
	lines, err := splitLines(r)
	if err != nil {
		return nil, err
	}
	res := &Result{Origin: origin}
	var errs Errors
	fail := func(line int, format string, args ...any) {
		if len(errs) < maxErrors {
			errs = append(errs, LineError{Line: line, Message: fmt.Sprintf(format, args...)})
		}
	}
	cur := origin
	var dollarTTL, lastTTL *uint32
	prevOwner := ""
	apexNS := 0
	for _, ll := range lines {
		f := ll.fields
		if strings.HasPrefix(f[0], "$") && !ll.ownerBlank {
			switch strings.ToUpper(f[0]) {
			case "$ORIGIN":
				if len(f) != 2 {
					fail(ll.line, "$ORIGIN takes one name")
					continue
				}
				next := absolute(f[1], cur)
				if _, ok := dns.IsDomainName(next); !ok || !dns.IsSubDomain(origin, next) {
					fail(ll.line, "$ORIGIN %s is outside %s", next, origin)
					continue
				}
				cur = next
			case "$TTL":
				ttl, ok, err := parseTTL(strings.Join(f[1:], ""))
				if len(f) != 2 || !ok || err != nil {
					fail(ll.line, "$TTL takes one TTL value")
					continue
				}
				dollarTTL = &ttl
				if res.DefaultTTL == 0 {
					res.DefaultTTL = ttl
				}
			case "$INCLUDE":
				fail(ll.line, "$INCLUDE is not supported")
			case "$GENERATE":
				fail(ll.line, "$GENERATE is not supported")
			default:
				fail(ll.line, "unknown directive %s", f[0])
			}
			continue
		}

		var owner string
		if ll.ownerBlank {
			if prevOwner == "" {
				fail(ll.line, "no previous owner")
				continue
			}
			owner = prevOwner
		} else {
			owner, f = absolute(f[0], cur), f[1:]
			if _, ok := dns.IsDomainName(owner); !ok {
				fail(ll.line, "invalid owner name %s", owner)
				continue
			}
			prevOwner = owner
		}

		var ttl *uint32
		classOK := true
		for k := 0; k < 2 && len(f) > 0; k++ {
			if v, ok, err := parseTTL(f[0]); ok {
				if err != nil {
					fail(ll.line, "%s", err.Error())
					classOK = false
					break
				}
				ttl, f = &v, f[1:]
				continue
			}
			if up := strings.ToUpper(f[0]); classes[up] {
				if up != "IN" {
					fail(ll.line, "only class IN is supported")
					classOK = false
					break
				}
				f = f[1:]
				continue
			}
			break
		}
		if !classOK {
			continue
		}
		if len(f) == 0 {
			fail(ll.line, "missing record type")
			continue
		}
		typName := strings.ToUpper(f[0])
		typ, ok := dns.StringToType[typName]
		if !ok && strings.HasPrefix(typName, "TYPE") {
			if n, err := strconv.ParseUint(typName[4:], 10, 16); err == nil {
				typ, ok = uint16(n), true
			}
		}
		if !ok {
			fail(ll.line, "unknown record type %s", f[0])
			continue
		}
		if typ != dns.TypeSOA && !opts.AllowedTypes[typ] {
			fail(ll.line, "unsupported record type %s", typName)
			continue
		}
		if !dns.IsSubDomain(origin, owner) {
			fail(ll.line, "%s is outside %s", owner, origin)
			continue
		}
		switch {
		case ttl != nil:
			lastTTL = ttl
		case dollarTTL != nil:
			ttl = dollarTTL
		case lastTTL != nil:
			ttl = lastTTL
		default:
			fail(ll.line, "no TTL")
			continue
		}

		text := fmt.Sprintf("$ORIGIN %s\n%s %d IN %s %s\n", cur, owner, *ttl, dns.TypeToString[typ], strings.Join(f[1:], " "))
		if dns.TypeToString[typ] == "" {
			text = fmt.Sprintf("$ORIGIN %s\n%s %d IN %s %s\n", cur, owner, *ttl, typName, strings.Join(f[1:], " "))
		}
		zp := dns.NewZoneParser(strings.NewReader(text), cur, "")
		rr, ok := zp.Next()
		if !ok {
			msg := "invalid record data"
			if err := zp.Err(); err != nil {
				msg = err.Error()
			}
			fail(ll.line, "%s", msg)
			continue
		}
		if _, more := zp.Next(); more {
			fail(ll.line, "record data spans more than one record")
			continue
		}

		if soa, isSOA := rr.(*dns.SOA); isSOA {
			switch {
			case !strings.EqualFold(owner, origin):
				fail(ll.line, "SOA must be at %s", origin)
			case res.SOA != nil:
				fail(ll.line, "duplicate SOA")
			default:
				res.SOA = soa
			}
			continue
		}
		if len(res.Records) >= opts.MaxRecords {
			return nil, append(errs, LineError{Line: ll.line, Message: fmt.Sprintf("zone file has more than %d records", opts.MaxRecords)})
		}
		if rr.Header().Rrtype == dns.TypeNS && strings.EqualFold(owner, origin) {
			apexNS++
		}
		res.Records = append(res.Records, rr)
	}
	if res.SOA == nil {
		fail(0, "no SOA record at %s", origin)
	}
	if apexNS == 0 {
		fail(0, "no NS records at %s", origin)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return res, nil
}
