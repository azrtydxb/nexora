package zonefile

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/mgmt/internal/nzf"
)

// Export writes a zone as a BIND master file with a stable layout: $ORIGIN and $TTL, the SOA,
// the apex NS records, then every other record in canonical order (owner, type, RDATA), one
// "owner<TAB>ttl<TAB>IN<TAB>TYPE<TAB>rdata" line each with owners relative to origin.
func Export(w io.Writer, origin string, defaultTTL uint32, soa *dns.SOA, records []dns.RR) error {
	origin = dns.CanonicalName(origin)
	type entry struct {
		rr   dns.RR
		key  []byte
		wire nzf.Record
		apex bool
	}
	entries := make([]entry, 0, len(records))
	for _, rr := range records {
		wr, err := nzf.FromRR(rr)
		if err != nil {
			return err
		}
		apexNS := rr.Header().Rrtype == dns.TypeNS && strings.EqualFold(rr.Header().Name, origin)
		entries = append(entries, entry{rr: rr, key: nzf.CanonicalKey(wr.Owner), wire: wr, apex: apexNS})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.apex != b.apex {
			return a.apex
		}
		if c := bytes.Compare(a.key, b.key); c != 0 {
			return c < 0
		}
		if a.wire.Type != b.wire.Type {
			return a.wire.Type < b.wire.Type
		}
		return bytes.Compare(a.wire.RData, b.wire.RData) < 0
	})
	zw := NewWriter(w, origin, defaultTTL)
	if soa != nil {
		if err := zw.SOA(soa); err != nil {
			return err
		}
	}
	for _, e := range entries {
		if err := zw.Record(e.rr); err != nil {
			return err
		}
	}
	return zw.Flush()
}

// Writer writes a BIND master file line by line: NewWriter writes $ORIGIN and $TTL, SOA and
// Record write one "owner<TAB>ttl<TAB>IN<TAB>TYPE<TAB>rdata" line with the owner relative to
// origin, in the order they are called. Flush must be called at the end.
type Writer struct {
	bw     *bufio.Writer
	origin string
}

// NewWriter starts a master file for origin on w.
func NewWriter(w io.Writer, origin string, defaultTTL uint32) *Writer {
	origin = dns.CanonicalName(origin)
	zw := &Writer{bw: bufio.NewWriter(w), origin: origin}
	fmt.Fprintf(zw.bw, "$ORIGIN %s\n$TTL %d\n", origin, defaultTTL)
	return zw
}

// SOA writes the SOA line; call it once, before any Record.
func (zw *Writer) SOA(soa dns.RR) error { return zw.Record(soa) }

// Record writes the line of rr.
func (zw *Writer) Record(rr dns.RR) error {
	h := rr.Header()
	_, err := fmt.Fprintf(zw.bw, "%s\t%d\tIN\t%s\t%s\n", relative(h.Name, zw.origin), h.Ttl, typeName(h.Rrtype), rdataText(rr))
	return err
}

// Flush writes what is buffered to the underlying writer.
func (zw *Writer) Flush() error { return zw.bw.Flush() }

func typeName(t uint16) string {
	if s, ok := dns.TypeToString[t]; ok {
		return s
	}
	return fmt.Sprintf("TYPE%d", t)
}

func rdataText(rr dns.RR) string {
	parts := strings.SplitN(rr.String(), "\t", 5)
	if len(parts) < 5 {
		return ""
	}
	return parts[4]
}

// relative writes name relative to origin ("@" for the apex); names outside origin stay absolute.
func relative(name, origin string) string {
	n, o := dns.CountLabel(name), dns.CountLabel(origin)
	if n < o || !dns.IsSubDomain(origin, name) {
		return name
	}
	if n == o {
		return "@"
	}
	idx := dns.Split(name)
	return name[:idx[n-o]-1]
}
