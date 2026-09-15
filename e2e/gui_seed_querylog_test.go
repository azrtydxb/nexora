package e2e

import (
	"strings"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

func init() { registerGUISeed(seedQueryLog) }

// seedQueryLog makes the A, AAAA and MX queries 25-querylog-filters.spec.ts filters by type and
// response, and an allowlisted query 26-querylog-reason.spec.ts shows the reason of.
func seedQueryLog(s guiSeedEnv) {
	// The fixture upstream serves unsigned answers under the real root anchor: without this every
	// forwarded answer is SERVFAIL. 16-dnssec.spec.ts flips the setting from whatever state it finds.
	s.Admin.DisableForwardedValidation()
	var allow struct {
		Domains  []string `json:"domains"`
		Revision int64    `json:"revision"`
	}
	s.Admin.Must("GET", "/allowlist", nil, &allow, 200)
	// The engine attributes an allowlist match only where a block list also matches (an allowlist
	// entry carves an exception): the malware category blocks malware.gui.test.
	s.Admin.Must("PUT", "/allowlist", map[string]any{"domains": append(allow.Domains, "allow.malware.gui.test"), "revision": allow.Revision}, nil, 200)
	waitLatestApplied(s.T, s.Admin, "gui-engine", "gui-engine-2")
	// An applied version may serve before its filter index is rebuilt: wait until the category blocks.
	harness.EventuallyTrue(s.T, 30*time.Second, func() bool {
		probe := strings.TrimSuffix(harness.UniqueName("probe"), ".example.") + ".malware.gui.test."
		return firstA(harness.MustQuery(s.T, s.Engine.DNS, probe, dns.TypeA, harness.QueryOpts{})) == "0.0.0.0"
	}, "malware category blocks again")
	name := "www.allow.malware.gui.test."
	if a := firstA(harness.MustQuery(s.T, s.Engine.DNS, name, dns.TypeA, harness.QueryOpts{})); a != "192.0.2.1" {
		s.T.Fatalf("allowlisted %s answered %q", name, a)
	}
	s.Vars["NEXORA_E2E_ALLOW_QUERY_NAME"] = strings.TrimSuffix(name, ".")

	prefix := strings.TrimSuffix(harness.UniqueName("qlmulti"), ".example.")
	for _, q := range []struct {
		suffix string
		qtype  uint16
	}{{"-a", dns.TypeA}, {"-aaaa", dns.TypeAAAA}, {"-mx", dns.TypeMX}} {
		if r := harness.MustQuery(s.T, s.Engine.DNS, prefix+q.suffix+".example.", q.qtype, harness.QueryOpts{}); r.Rcode != dns.RcodeSuccess {
			s.T.Fatalf("seed query %s%s: %v", prefix, q.suffix, r)
		}
	}
	s.Vars["NEXORA_E2E_QL_MULTI_PREFIX"] = prefix
}
