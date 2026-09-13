package zonefile_test

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/mgmt/internal/zone"
	"github.com/piwi3910/nexora/mgmt/internal/zonefile"
)

var update = flag.Bool("update", false, "rewrite export golden")

func opts() zonefile.Options {
	return zonefile.Options{AllowedTypes: zone.ManagedTypes, MaxRecords: 1000000}
}

func TestParseAllTypes(t *testing.T) {
	f, _ := os.Open("testdata/all-types.zone")
	defer f.Close()
	res, err := zonefile.Parse(f, "example.test.", opts())
	if err != nil {
		t.Fatal(err)
	}
	if res.SOA == nil || res.SOA.Serial != 2026091301 || res.SOA.Ns != "ns1.example.test." || res.SOA.Minttl != 300 {
		t.Fatalf("SOA: %v", res.SOA)
	}
	if res.DefaultTTL != 3600 || len(res.Records) != 24 {
		t.Fatalf("default ttl %d, %d records", res.DefaultTTL, len(res.Records))
	}
	seen := map[uint16]bool{}
	for _, rr := range res.Records {
		seen[rr.Header().Rrtype] = true
	}
	for typ := range zone.ManagedTypes {
		if !seen[typ] {
			t.Errorf("type %s not parsed", dns.TypeToString[typ])
		}
	}
	byName := func(name string, typ uint16) dns.RR {
		for _, rr := range res.Records {
			if rr.Header().Name == name && rr.Header().Rrtype == typ {
				return rr
			}
		}
		t.Fatalf("%s %s missing", name, dns.TypeToString[typ])
		return nil
	}
	if c := byName("www.example.test.", dns.TypeCNAME).(*dns.CNAME); c.Target != "example.test." {
		t.Fatalf("@ in rdata: %q", c.Target)
	}
	if byName("ns2.example.test.", dns.TypeAAAA).Header().Ttl != 300 {
		t.Fatal("explicit TTL ignored")
	}
	if byName("Bin\\000ary.example.test.", dns.TypeA).Header().Ttl != 86400 {
		t.Fatal("1d TTL unit not parsed")
	}
	if txt := byName("example.test.", dns.TypeTXT).(*dns.TXT); len(txt.Txt) != 2 || txt.Txt[1] != "second; string" {
		t.Fatalf("quoted semicolon treated as comment: %q", txt.Txt)
	}
	byName("escaped\\.dot.example.test.", dns.TypeTXT)
}

func TestParseRefusals(t *testing.T) {
	cases := map[string]struct{ file, want string }{
		"include":     {"$ORIGIN example.test.\n$TTL 60\n$INCLUDE other.zone\n", "line 3: $INCLUDE is not supported"},
		"generate":    {"$ORIGIN example.test.\n$TTL 60\n$GENERATE 1-10 h$ A 192.0.2.$\n", "line 3: $GENERATE is not supported"},
		"type":        {"$ORIGIN example.test.\n$TTL 60\nx IN HINFO a b\n", "line 3: unsupported record type HINFO"},
		"dnssec":      {"$ORIGIN example.test.\n$TTL 60\n@ IN DNSKEY 257 3 13 AAAA\n", "line 3: unsupported record type DNSKEY"},
		"class":       {"$ORIGIN example.test.\n$TTL 60\nx CH A 192.0.2.1\n", "line 3: only class IN is supported"},
		"outside":     {"$ORIGIN example.test.\n$TTL 60\nwww.example.org. IN A 192.0.2.1\n", "line 3: www.example.org. is outside example.test."},
		"no ttl":      {"$ORIGIN example.test.\nx IN A 192.0.2.1\n", "line 2: no TTL"},
		"paren":       {"$ORIGIN example.test.\n$TTL 60\nx IN TXT ( \"a\"\n", "unbalanced '('"},
		"bad rdata":   {"$ORIGIN example.test.\n$TTL 60\nx IN A 999.1.1.1\n", "line 3:"},
		"second soa":  {"$ORIGIN example.test.\n$TTL 60\n@ SOA a. b. 1 2 3 4 5\n@ SOA a. b. 2 2 3 4 5\n", "line 4: duplicate SOA"},
		"missing soa": {"$ORIGIN example.test.\n$TTL 60\n@ NS ns1\n", "no SOA record at example.test."},
	}
	for name, c := range cases {
		_, err := zonefile.Parse(strings.NewReader(c.file), "example.test.", opts())
		var le zonefile.Errors
		if err == nil || !strings.Contains(err.Error(), c.want) || (strings.HasPrefix(c.want, "line") && !errors.As(err, &le)) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
}

func TestExportIsStableAndReparses(t *testing.T) {
	f, _ := os.Open("testdata/all-types.zone")
	res, err := zonefile.Parse(f, "example.test.", opts())
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	var a, b bytes.Buffer
	shuffled := append([]dns.RR(nil), res.Records...)
	for i, j := 0, len(shuffled)-1; i < j; i, j = i+1, j-1 {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	}
	if err := zonefile.Export(&a, "example.test.", res.DefaultTTL, res.SOA, res.Records); err != nil {
		t.Fatal(err)
	}
	if err := zonefile.Export(&b, "example.test.", res.DefaultTTL, res.SOA, shuffled); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("export depends on input order")
	}
	if *update {
		if err := os.WriteFile("testdata/all-types.export.golden", a.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	golden, _ := os.ReadFile("testdata/all-types.export.golden")
	if !bytes.Equal(a.Bytes(), golden) {
		t.Fatalf("export differs from golden:\n%s", a.String())
	}
	again, err := zonefile.Parse(bytes.NewReader(a.Bytes()), "example.test.", opts())
	if err != nil || len(again.Records) != len(res.Records) {
		t.Fatalf("re-parse: %v (%d records)", err, len(again.Records))
	}
}
