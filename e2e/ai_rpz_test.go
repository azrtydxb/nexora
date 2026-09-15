package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

// aiRpzFamilyName is the i-th random-looking name of the fam.ars.test family.
func aiRpzFamilyName(i int) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "airpz-%d", i))
	return hex.EncodeToString(sum[:])[:48] + ".fam.ars.test."
}

func aiRpzRule(record string) map[string]any {
	return map[string]any{"record": record, "policy": "nxdomain", "category": "malware",
		"reason": "scripted suggestion for " + record, "confidence": 0.95}
}

type aiProposalView struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Title  string `json:"title"`
	Source string `json:"source"`
}

type aiApplyResult struct {
	Results []struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Actions []struct {
			OperationID string `json:"operation_id"`
			HTTPStatus  int    `json:"http_status"`
			Message     string `json:"message"`
		} `json:"actions"`
	} `json:"results"`
}

// openRpzProposals returns the open rpz_suggestions proposals by title.
func openRpzProposals(t *testing.T, admin *harness.API) map[string]string {
	t.Helper()
	var list []aiProposalView
	admin.Must(http.MethodGet, "/ai/proposals?status=open&source=rpz_suggestions", nil, &list, http.StatusOK)
	out := map[string]string{}
	for _, p := range list {
		out[p.Title] = p.ID
	}
	return out
}

// applyRpz applies ids in one request and fails unless every proposal was applied.
func applyRpz(t *testing.T, admin *harness.API, ids ...string) {
	t.Helper()
	var res aiApplyResult
	admin.Must(http.MethodPost, "/ai/proposals/apply", map[string]any{"ids": ids}, &res, http.StatusOK)
	if len(res.Results) != len(ids) {
		t.Fatalf("apply %v -> %+v", ids, res)
	}
	for _, r := range res.Results {
		if r.Status != "applied" {
			t.Fatalf("apply %v -> %+v", ids, res)
		}
	}
}

// wantNXDOMAIN waits until the engine answers NXDOMAIN for every name.
func wantNXDOMAIN(t *testing.T, addr string, names ...string) {
	t.Helper()
	c := &dns.Client{Timeout: 2 * time.Second}
	for _, name := range names {
		harness.EventuallyTrue(t, 30*time.Second, func() bool {
			m, _, err := c.Exchange(question(name, dns.TypeA), addr)
			return err == nil && m.Rcode == dns.RcodeNameError
		}, "the engine answers NXDOMAIN for "+name)
	}
}

// TestAIRpzSuggestionsApply catches suggestions that never reach the zone, an apply that loses the rules
// of earlier applies, and a deleted ai-suggested.rpz zone that is not recreated with everything applied so
// far. The agent only suggests: the zone exists only after an operator applies.
func TestAIRpzSuggestionsApply(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	fx := env.StartOpenAIFixture()
	dnsFx := env.StartDNSFixture()
	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{ExtraEnv: harness.AIEnv(fx)})
	admin := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
	admin.DisableForwardedValidation()
	admin.Must(http.MethodPost, "/upstreams", map[string]any{"name": "fixture", "protocol": "udp", "address": dnsFx.UDP,
		"timeout_ms": 250, "enabled": true, "position": 0}, nil, http.StatusCreated)
	eng := env.StartManagedEngine("ai-rpz-1", []string{mg.GRPCURL}, admin.CreateJoinToken())
	waitLatestApplied(t, admin, "ai-rpz-1")

	// Positive path: the names resolve before any rule is suggested.
	c := &dns.Client{Timeout: 2 * time.Second}
	queried := []string{"bad.ars.test.", "third.ars.test."}
	for i := range 6 {
		queried = append(queried, aiRpzFamilyName(i))
	}
	for _, name := range queried {
		m, _, err := c.Exchange(question(name, dns.TypeA), eng.DNS)
		if err != nil || m.Rcode != dns.RcodeSuccess || len(m.Answer) == 0 {
			t.Fatalf("query %s before the rules: %v %v", name, m, err)
		}
	}
	for _, name := range []string{"bad.ars.test", "third.ars.test"} {
		harness.PGExec(t, pg.URL, `insert into ai_domain_verdicts(name, is_threat, categories, confidence, reasoning, checked_at, expires_at)
			values ($1, true, '{malware}', 0.9, 'seeded threat verdict', now(), now() + interval '7 days')`, name)
	}
	harness.EventuallyTrue(t, 30*time.Second, func() bool {
		var page struct {
			Records []struct{} `json:"records"`
		}
		admin.Must(http.MethodGet, "/query-log?limit=100&name=ars.test", nil, &page, http.StatusOK)
		return len(page.Records) >= len(queried)
	}, "the seeded queries reach the query log")

	// 1. The first run suggests the threat name and the family wildcard; applying both creates the zone.
	fx.ScriptJSON(t, "rpz_suggestions", map[string]any{"rules": []any{aiRpzRule("bad.ars.test"), aiRpzRule("*.fam.ars.test")}})
	admin.Must(http.MethodPost, "/ai/agents/rpz_suggestions/run", nil, nil, http.StatusAccepted)
	var props map[string]string
	harness.EventuallyTrue(t, 60*time.Second, func() bool {
		props = openRpzProposals(t, admin)
		return props["Block bad.ars.test"] != "" && props["Block *.fam.ars.test"] != ""
	}, "the agent opens one proposal per suggested rule")
	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	admin.Must(http.MethodGet, "/rpz-zones", nil, &zones, http.StatusOK)
	if len(zones) != 0 {
		t.Fatalf("suggesting wrote RPZ zones before any apply: %+v", zones)
	}
	applyRpz(t, admin, props["Block bad.ars.test"], props["Block *.fam.ars.test"])
	wantNXDOMAIN(t, eng.DNS, "bad.ars.test.", "x1.fam.ars.test.")

	// 2. A later apply keeps the applied rules and adds a third.
	fx.ScriptJSON(t, "rpz_suggestions", map[string]any{"rules": []any{aiRpzRule("third.ars.test")}})
	admin.Must(http.MethodPost, "/ai/agents/rpz_suggestions/run", nil, nil, http.StatusAccepted)
	harness.EventuallyTrue(t, 60*time.Second, func() bool {
		props = openRpzProposals(t, admin)
		return props["Block third.ars.test"] != ""
	}, "the second run opens the third proposal")
	if props["Block bad.ars.test"] != "" {
		t.Fatal("an already applied record was suggested again")
	}
	applyRpz(t, admin, props["Block third.ars.test"])
	wantNXDOMAIN(t, eng.DNS, "bad.ars.test.", "x1.fam.ars.test.", "third.ars.test.")

	// 3. Deleting the zone and applying a fourth rule recreates it with every rule applied so far.
	var zone struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Revision int64  `json:"revision"`
	}
	admin.Must(http.MethodGet, "/rpz-zones", nil, &zones, http.StatusOK)
	if len(zones) != 1 || zones[0].Name != "ai-suggested.rpz." {
		t.Fatalf("rpz zones = %+v, want only ai-suggested.rpz.", zones)
	}
	admin.Must(http.MethodGet, "/rpz-zones/"+zones[0].ID, nil, &zone, http.StatusOK)
	admin.Must(http.MethodDelete, fmt.Sprintf("/rpz-zones/%s?revision=%d", zone.ID, zone.Revision), nil, nil, http.StatusNoContent)
	waitLatestApplied(t, admin, "ai-rpz-1")

	harness.PGExec(t, pg.URL, `insert into ai_proposals(source, fingerprint, title, description, priority, actions)
		values ('rpz_suggestions', 'e2e-fourth', 'Block fourth.ars.test', 'seeded proposal', 'high',
			'[{"operation_id":"appendAiRpzRules","path_params":{},"body":{"rules":[{"record":"fourth.ars.test","policy":"nxdomain",
			"category":"malware","reason":"seeded","confidence":0.9}]}}]'::jsonb)`)
	props = openRpzProposals(t, admin)
	if props["Block fourth.ars.test"] == "" {
		t.Fatalf("seeded proposal not open: %+v", props)
	}
	applyRpz(t, admin, props["Block fourth.ars.test"])
	wantNXDOMAIN(t, eng.DNS, "bad.ars.test.", "x1.fam.ars.test.", "third.ars.test.", "fourth.ars.test.")
}
