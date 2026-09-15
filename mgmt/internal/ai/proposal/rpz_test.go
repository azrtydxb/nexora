package proposal_test

import (
	"strings"
	"testing"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/ai/proposal"
	"github.com/piwi3910/nexora/mgmt/internal/rpz"
)

// TestRpzZoneContent catches a rendered zone the RPZ upload would reject, a wrong policy encoding, a
// missing per-rule comment and an untruncated or multi-line reason.
func TestRpzZoneContent(t *testing.T) {
	now := time.Unix(1_789_000_000, 0)
	rules := []proposal.RPZRule{
		{Record: "c2.evil.example", Policy: "nxdomain", Category: "c2", Reason: "beaconing"},
		{Record: "*.bad.example", Policy: "drop", Category: "malware", Reason: "family"},
	}
	content := proposal.RenderZone(rules, now)
	sum, err := rpz.ValidateZone(proposal.RPZZoneName, content)
	if err != nil {
		t.Fatalf("zone rejected: %v\n%s", err, content)
	}
	if sum.Records != 2 || sum.Serial != uint32(now.Unix()) {
		t.Fatalf("summary %+v\n%s", sum, content)
	}
	if n := strings.Count(content, "; ai proposal"); n != 2 {
		t.Fatalf("%d comment lines\n%s", n, content)
	}
	for _, want := range []string{"$TTL 300\n", "@ SOA localhost. hostmaster.localhost. 1789000000 3600 600 86400 300\n", "@ NS localhost.\n",
		"c2.evil.example CNAME .\n", "*.bad.example CNAME rpz-drop.\n"} {
		if !strings.Contains(content, want) {
			t.Errorf("missing %q\n%s", want, content)
		}
	}
	other := proposal.RenderZone([]proposal.RPZRule{
		{Record: "a.example", Policy: "nodata", Category: "x", Reason: strings.Repeat("r", 300)},
		{Record: "b.example", Policy: "passthru", Category: "x", Reason: "line one\n$INCLUDE /etc/passwd"},
	}, now)
	if !strings.Contains(other, "a.example CNAME *.\n") || !strings.Contains(other, "b.example CNAME rpz-passthru.\n") {
		t.Fatalf("nodata/passthru encoding\n%s", other)
	}
	if strings.Contains(other, strings.Repeat("r", 201)) || !strings.Contains(other, strings.Repeat("r", 200)) {
		t.Fatalf("reason not truncated to 200\n%s", other)
	}
	if _, err := rpz.ValidateZone(proposal.RPZZoneName, other); err != nil {
		t.Fatalf("multi-line reason broke the zone: %v\n%s", err, other)
	}
}
