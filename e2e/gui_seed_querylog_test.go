package e2e

import (
	"net/url"
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
	s.Vars["NEXORA_E2E_ALLOW_QUERY_NAME"] = strings.TrimSuffix(name, ".")
	// The engine exports query log batches best-effort: a batch sent while the management channel
	// is unavailable (the config apply just before this query re-establishes it) is dropped and
	// never retried, and a single query then leaves 26-querylog-reason.spec.ts with no row to
	// filter by source. Query again until the attributed record is in the log.
	var ql struct {
		Records []struct {
			Name     string `json:"name"`
			Source   string `json:"source"`
			ListName string `json:"list_name"`
			Rule     string `json:"rule"`
		} `json:"records"`
	}
	query := "/query-log?limit=50&source=allowlist&name=" + url.QueryEscape(strings.TrimSuffix(name, "."))
	logged := func() bool {
		s.Admin.Must("GET", query, nil, &ql, 200)
		return len(ql.Records) > 0
	}
	deadline := time.Now().Add(30 * time.Second)
	for !logged() {
		if a := firstA(harness.MustQuery(s.T, s.Engine.DNS, name, dns.TypeA, harness.QueryOpts{})); a != "192.0.2.1" {
			s.T.Fatalf("allowlisted %s answered %q", name, a)
		}
		if time.Now().After(deadline) {
			s.T.Fatalf("allowlisted %s never reached the query log with source=allowlist", name)
		}
		time.Sleep(time.Second)
	}

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
