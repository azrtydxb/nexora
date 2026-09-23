package catzone

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/miekg/dns"
)

func rrs(t *testing.T, text string) []dns.RR {
	t.Helper()
	zp := dns.NewZoneParser(strings.NewReader(text), "cat.test.", "")
	var out []dns.RR
	for rr, ok := zp.Next(); ok; rr, ok = zp.Next() {
		out = append(out, rr)
	}
	if err := zp.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCatalogBuildIsStable(t *testing.T) {
	id := uuid.MustParse("7f3d2c1b-0a09-4876-9543-210fedcba987")
	if Label(id) != "7f3d2c1b0a0948769543210fedcba987" {
		t.Fatalf("label %q", Label(id))
	}
	a := Build("cat.test.", []Member{{Label: "bb", Zone: "b.example."}, {Label: "aa", Zone: "a.example."}})
	b := Build("cat.test.", []Member{{Label: "aa", Zone: "a.example."}, {Label: "bb", Zone: "b.example."}})
	var sa, sb []string
	for i := range a {
		sa, sb = append(sa, a[i].String()), append(sb, b[i].String())
	}
	want := []string{
		"cat.test.\t0\tIN\tNS\tinvalid.",
		"version.cat.test.\t0\tIN\tTXT\t\"2\"",
		"aa.zones.cat.test.\t0\tIN\tPTR\ta.example.",
		"bb.zones.cat.test.\t0\tIN\tPTR\tb.example.",
	}
	if strings.Join(sa, "\n") != strings.Join(want, "\n") || strings.Join(sb, "\n") != strings.Join(want, "\n") {
		t.Fatalf("build:\n%s\n%s", strings.Join(sa, "\n"), strings.Join(sb, "\n"))
	}
	got, err := Parse("cat.test.", a)
	if err != nil || len(got) != 2 || got[0] != (Member{"aa", "a.example."}) {
		t.Fatalf("round trip %v %v", got, err)
	}
}

func TestCatalogParseBrokenRules(t *testing.T) {
	valid := `@ 0 IN SOA invalid. invalid. 1 3600 600 86400 0
@ 0 IN NS invalid.
version 0 IN TXT "2"
m1.zones 0 IN PTR A.Example.
coo.m1.zones 0 IN PTR other.cat.
group.m1.zones 0 IN TXT "blue"
m2.zones 0 IN PTR b.example.
foo.bar.ext 0 IN TXT "custom"
unknown 0 IN A 192.0.2.1
`
	got, err := Parse("cat.test.", rrs(t, valid))
	if err != nil || len(got) != 2 || got[0] != (Member{"m1", "a.example."}) || got[1] != (Member{"m2", "b.example."}) {
		t.Fatalf("valid catalog: %v %v", got, err)
	}
	for _, c := range []struct{ name, text, reason string }{
		{"no version", strings.Replace(valid, `version 0 IN TXT "2"`, "", 1), "version property missing"},
		{"two versions", valid + "version 0 IN TXT \"1\"\n", "version property has 2 records"},
		{"version 1", strings.Replace(valid, `TXT "2"`, `TXT "1"`, 1), `unsupported catalog version "1"`},
		{"version two strings", strings.Replace(valid, `TXT "2"`, `TXT "2" "x"`, 1), `unsupported catalog version "2x"`},
		{"two PTR at a member", valid + "m2.zones 0 IN PTR c.example.\n", "member node m2 has 2 PTR records"},
		{"member under two labels", valid + "m3.zones 0 IN PTR a.example.\n", "member a.example. listed under 2 labels"},
		{"two coo", valid + "coo.m1.zones 0 IN PTR third.cat.\n", "coo property of m1 has 2 records"},
	} {
		_, err := Parse("cat.test.", rrs(t, c.text))
		var broken *BrokenError
		if !errors.As(err, &broken) || broken.Reason != c.reason {
			t.Fatalf("%s: %v, want %q", c.name, err, c.reason)
		}
	}
}
