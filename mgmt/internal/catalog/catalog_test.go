package catalog_test

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/catalog"
)

func TestEmbeddedCatalogIsValid(t *testing.T) {
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, cat := range c.Categories {
		keys = append(keys, cat.Key)
	}
	want := "malware,phishing,ads-tracking,adult,gambling,social,crypto-mining,piracy,drugs,fake-news,dating,games"
	if strings.Join(keys, ",") != want {
		t.Fatalf("categories %v, want %s", keys, want)
	}
	defaults := map[string]catalog.Source{}
	for _, cat := range c.Categories {
		for _, s := range cat.Sources {
			if strings.HasPrefix(s.Key, "oisd-") && (s.CommercialUse || !strings.Contains(s.Notice, "not free for commercial use")) {
				t.Errorf("%s must be flagged commercial_use: false with the OISD notice", s.Key)
			}
			if strings.HasPrefix(s.Key, "ut1-") && !strings.HasPrefix(s.ArchiveMember, "blacklists/") {
				t.Errorf("%s needs a blacklists/<category>/domains archive member", s.Key)
			}
			if s.DefaultEnabled {
				defaults[s.Key] = s
			}
		}
	}
	if _, ok := c.Source("ads-tracking", "oisd-big"); !ok || c.Position("malware", "hagezi-tif") != 1 {
		t.Fatal("lookups by category and source")
	}
	// bench/filter/corpus-5m.tsv is the default selection the filter index budgets were measured on.
	f, err := os.Open("../../../bench/filter/corpus-5m.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	seen := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		s, ok := defaults[cols[0]]
		member := cols[2]
		if member == "-" {
			member = ""
		}
		if !ok || s.URL != cols[1] || s.ArchiveMember != member {
			t.Errorf("corpus row %q does not match a default-enabled catalog source", line)
		}
		seen++
	}
	if seen != len(defaults) {
		t.Errorf("corpus lists %d sources, catalog enables %d by default", seen, len(defaults))
	}
}

func TestParseRejectsInvalidCatalogs(t *testing.T) {
	valid := `version: 1
categories:
  - key: gambling
    name: Gambling
    description: Betting sites.
    sources:
      - key: hagezi-gambling
        name: HaGeZi Gambling
        url: https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/gambling-onlydomains.txt
        format: domains
        license: GPL-3.0
        license_url: https://github.com/hagezi/dns-blocklists/blob/main/LICENSE
        attribution: HaGeZi
        commercial_use: true
        default_enabled: true
        refresh_interval_seconds: 43200
`
	if _, err := catalog.Parse([]byte(valid)); err != nil {
		t.Fatalf("valid catalog rejected: %v", err)
	}
	for _, c := range []struct{ from, to, want string }{
		{"version: 1", "version: 2", "catalog version 2"},
		{"key: gambling", "key: Gambling", "category key Gambling"},
		{"url: https://raw", "url: http://raw", "source hagezi-gambling: url must be https"},
		{"format: domains", "format: adblock", "source hagezi-gambling: format adblock"},
		{"license: GPL-3.0", "license: \"\"", "source hagezi-gambling: license, license_url and attribution are required"},
		{"commercial_use: true", "commercial_use: false", "source hagezi-gambling: commercial_use false needs a notice"},
		{"refresh_interval_seconds: 43200", "refresh_interval_seconds: 60", "source hagezi-gambling: refresh_interval_seconds must be at least 3600"},
		{"format: domains", "format: domains\n        archive_member: blacklists/gambling/domains", "source hagezi-gambling: archive_member needs a .tar.gz url"},
	} {
		_, err := catalog.Parse([]byte(strings.Replace(valid, c.from, c.to, 1)))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s -> %v, want %q", c.to, err, c.want)
		}
	}
	dup := strings.Replace(valid, "sources:", "sources:\n      - key: hagezi-gambling\n        name: again\n        url: https://x.test/a.txt\n        format: domains\n        license: MIT\n        license_url: https://x.test/l\n        attribution: x\n        commercial_use: true\n        refresh_interval_seconds: 3600", 1)
	if _, err := catalog.Parse([]byte(dup)); err == nil || !strings.Contains(err.Error(), "duplicate source key hagezi-gambling") {
		t.Errorf("duplicate source -> %v", err)
	}
}
