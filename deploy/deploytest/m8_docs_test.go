package deploytest

import (
	"os"
	"strings"
	"testing"
)

var m8GuideRequirements = map[string][]string{
	"mDNS gateway and reflection": {"off by default", "hostNetwork", "100–5000", "not hostNetwork", "does not prove LAN multicast"},
	"ZONEMD":                      {"zonemd_generate", "zonemd_verify", "if_present", "last good zone", "SHA-384", "SHA-512", "ldns-verify-zone", "does not DNSSEC-validate"},
	"Oblivious DoH":               {"off by default", "five minutes", "never", "snapshots", "allow list", "recursion ACL", "proxy address", "key timestamps"},
	"Catalog zones":               {"RFC 9432", "catalog_zone_id", "empty catalog", "deletes every member", "rejects incompatible", "Do not rewrite goose history"},
}

func missingM8GuideRequirements(guide string) []string {
	var missing []string
	for title, terms := range m8GuideRequirements {
		_, section, ok := strings.Cut(guide, "\n## "+title+"\n")
		if !ok {
			missing = append(missing, title)
			continue
		}
		section, _, _ = strings.Cut(section, "\n## ")
		// Normalise wrapping so prose reflow is harmless.
		section = strings.Join(strings.Fields(section), " ")
		for _, term := range terms {
			if !strings.Contains(section, term) {
				missing = append(missing, title+": "+term)
			}
		}
	}
	return missing
}

func TestOperationsGuideCoversM8(t *testing.T) {
	b, err := os.ReadFile("../../docs/operations.md")
	if err != nil {
		t.Fatal(err)
	}
	if missing := missingM8GuideRequirements(string(b)); len(missing) != 0 {
		t.Fatalf("missing M8 operations guidance: %v", missing)
	}
	// Negative controls ensure headings alone or a note in another section cannot pass.
	for title := range m8GuideRequirements {
		t.Run("missing-"+title, func(t *testing.T) {
			broken := strings.Replace(string(b), "\n## "+title+"\n", "\n## removed\n", 1)
			if len(missingM8GuideRequirements(broken)) == 0 {
				t.Fatal("missing section passed")
			}
		})
	}
	t.Run("missing-host-network-note", func(t *testing.T) {
		if len(missingM8GuideRequirements(strings.ReplaceAll(string(b), "hostNetwork", "removed"))) == 0 {
			t.Fatal("missing hostNetwork note passed")
		}
	})
}

func TestKwAcceptanceIncludesM8(t *testing.T) {
	b, err := os.ReadFile("../../scripts/kw-acceptance.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	for _, required := range []string{"NEXORA_KW_M8=1", "run=\"${1:-TestKwSmoke|TestKwSmokeM8|", "NEXORA_KW_EXPECTED_ENGINES", "--require-paired"} {
		if !strings.Contains(script, required) {
			t.Errorf("M8 live wiring lacks %q", required)
		}
	}
}
