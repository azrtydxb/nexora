package blocklist_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/piwi3910/nexora/mgmt/internal/blocklist"
)

func TestParseFormats(t *testing.T) {
	in := strings.Join([]string{
		"# hosts style",
		"0.0.0.0 ads.example.com tracker.example.com",
		"127.0.0.1 localhost",
		"::1 Evil.Example.NET.",
		"[Adblock Plus 2.0]",
		"! adblock comment",
		"||adblock.example.org^",
		"||opts.example.org^$third-party",
		"@@||allowed.example.org^",
		"example.com##.banner",
		"plain.example.io",
		"bücher.example",
		"not a domain",
		"-bad-.example",
		"",
	}, "\n")
	domains, stats, err := blocklist.Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ads.example.com", "tracker.example.com", "evil.example.net", "adblock.example.org", "opts.example.org", "plain.example.io", "xn--bcher-kva.example"}
	if strings.Join(domains, ",") != strings.Join(want, ",") {
		t.Fatalf("domains = %v", domains)
	}
	if stats.Entries != len(want) || stats.Invalid != 4 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestNormalizeAndCompressAreDeterministic(t *testing.T) {
	text := blocklist.Normalize([]string{"b.example", "a.example", "b.example"})
	if string(text) != "a.example\nb.example\n" {
		t.Fatalf("normalized %q", text)
	}
	d1, s1, err := blocklist.Compress(text)
	if err != nil {
		t.Fatal(err)
	}
	_, s2, _ := blocklist.Compress(text)
	if s1 != s2 || len(s1) != 64 {
		t.Fatalf("sha %s vs %s", s1, s2)
	}
	dec, _ := zstd.NewReader(nil)
	out, err := dec.DecodeAll(d1, nil)
	if err != nil || string(out) != string(text) {
		t.Fatalf("round trip: %q %v", out, err)
	}
}

func TestParseStripsWildcardPrefix(t *testing.T) {
	domains, stats, err := blocklist.Parse(strings.NewReader("# oisd\n*.ads.oisd.test\n*.Tracker.OISD.test\n*.\n"))
	if err != nil || stats.Entries != 2 || stats.Invalid != 1 || !slices.Equal(domains, []string{"ads.oisd.test", "tracker.oisd.test"}) {
		t.Fatalf("wildcard: %v %+v %v", domains, stats, err)
	}
}
