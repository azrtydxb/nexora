package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"
	"google.golang.org/protobuf/proto"

	"github.com/piwi3910/nexora/e2e/harness"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
)

// configTables are every table an AI agent could change if it wrote configuration instead of proposing
// it. Their checksums are compared before and after the agent runs.
var configTables = []string{"policy_groups", "filter_categories", "upstreams", "resolver_settings",
	"global_safe_search", "allowlist", "engine_groups", "rpz_zones", "zones"}

// pgText runs a query returning one text value.
func pgText(t *testing.T, url, sql string, args ...any) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var s string
	if err := conn.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
		t.Fatalf("%q: %v", sql, err)
	}
	return s
}

// configFingerprint is the configuration an agent must not touch: the published version, a checksum per
// configuration table and the number of audit entries that are not AI review actions.
type configFingerprint struct {
	Version    string
	Checksums  map[string]string
	AuditCount int
}

func readConfigFingerprint(t *testing.T, url string) configFingerprint {
	t.Helper()
	f := configFingerprint{
		Version:    pgText(t, url, "select coalesce(max(version), 0)::text from config_versions"),
		Checksums:  map[string]string{},
		AuditCount: pgInt(t, url, "select count(*) from audit_log where action not like '%Ai%'"),
	}
	for _, table := range configTables {
		f.Checksums[table] = pgText(t, url,
			fmt.Sprintf("select coalesce(md5(string_agg(t::text, ',' order by t::text)), 'empty') from %s t", table))
	}
	return f
}

// aiAgentState is one agent's state in getAiStatus.
type aiAgentState struct {
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	Running     bool   `json:"running"`
	LastOutcome string `json:"last_outcome"`
	LastError   string `json:"last_error"`
}

func aiAgentStates(a *harness.API) map[string]aiAgentState {
	a.T.Helper()
	var s struct {
		Agents []aiAgentState `json:"agents"`
	}
	a.Must(http.MethodGet, "/ai/status", nil, &s, http.StatusOK)
	out := map[string]aiAgentState{}
	for _, st := range s.Agents {
		out[st.Name] = st
	}
	return out
}

// insertEngineStats writes hours raw engine samples ending at last, one per hour except the newest,
// which follows the one before it by two minutes: the upstream's RTT rises, and the SERVFAIL share
// jumps in the last two samples. The newest pair is what the dashboard insight agent compares against
// the previous 24 h, so it lies inside the agent's 15-minute window when last is recent.
func insertEngineStats(t *testing.T, pgURL, engineID, upstream string, last time.Time, hours int, cacheBytes uint64) {
	t.Helper()
	var queries, servfail uint64
	for i := range hours {
		queries += 1000
		spike := i >= hours-2
		if spike {
			servfail += 300
		} else {
			servfail += 5
		}
		s := &controlv1.Stats{
			QueriesTotal:  queries,
			ServfailTotal: servfail,
			CacheBytes:    cacheBytes,
			QueriesByRcode: map[string]uint64{
				"NOERROR":  queries - servfail,
				"SERVFAIL": servfail,
			},
			ProcessResidentBytes: 200 << 20,
			MemoryLimitBytes:     1 << 30,
			Upstreams: []*controlv1.UpstreamStatus{{
				Name: upstream, Up: true, RttUs: uint32(20000 + 400*i),
				QueriesTotal: queries, FailuresTotal: uint64(i),
			}},
		}
		raw, err := proto.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		at := last
		if i < hours-1 {
			at = last.Add(-2*time.Minute - time.Duration(hours-2-i)*time.Hour)
		}
		harness.PGExec(t, pgURL, `insert into engine_stats(engine_id, at, stats) values ($1, $2, $3)
			on conflict do nothing`, engineID, at, raw)
	}
}

// TestNoAgentWritesConfiguration is the suggest-only proof: with every agent fed data and a model that
// answers with something to propose, the agents must produce proposals and findings and change no
// configuration at all — no new config version, no changed configuration table, no non-AI audit entry.
// It catches an agent that applies its own suggestion, writes configuration directly, or publishes a
// configuration version behind the operator's back.
func TestNoAgentWritesConfiguration(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	aiFx := env.StartOpenAIFixture()
	dnsFx := env.StartDNSFixture()
	lists := env.StartHTTPFixture()
	lists.SetList(t, "hagezi-gambling", "casino.aso.test\n")
	lists.SetList(t, "blp-gambling", "casino.aso.test\n")
	lists.SetList(t, "hagezi-pro", "ads.aso.test\n")

	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{
		ExtraEnv: append(harness.AIEnv(aiFx), harness.CatalogMirrorEnv(lists)),
	})
	admin := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
	admin.DisableForwardedValidation()
	admin.Must(http.MethodPost, "/upstreams", map[string]any{"name": "fixture", "protocol": "udp",
		"address": dnsFx.UDP, "timeout_ms": 250, "enabled": true, "position": 0}, nil, http.StatusCreated)
	const node = "ai-suggest-1"
	eng := env.StartManagedEngine(node, []string{mg.GRPCURL}, admin.CreateJoinToken())
	waitLatestApplied(t, admin, node)

	// One enabled block list (the threat classification agent's input) and one refreshed list of a
	// category that stays disabled (the filter recommendation agent's disabled-category hits).
	if code, reason := admin.SetFilterCategory("gambling", true, nil, false); code != http.StatusOK {
		t.Fatalf("enable gambling -> %d %s", code, reason)
	}
	admin.RefreshCategory("gambling")
	admin.RefreshSource("ads-tracking", "hagezi-pro")
	waitLatestApplied(t, admin, node)

	// Traffic: tunnelling names, a name of the disabled category's list, a tracker, a threat name and a
	// high-entropy family the RPZ agent can suggest a wildcard for.
	c := &dns.Client{Timeout: 2 * time.Second}
	ask := func(name string, qtype uint16) {
		t.Helper()
		if _, _, err := c.Exchange(question(name, qtype), eng.DNS); err != nil {
			t.Fatalf("query %s: %v", name, err)
		}
	}
	for i := range 80 {
		sum := sha256.Sum256(fmt.Appendf(nil, "aso-%d", i))
		ask(hex.EncodeToString(sum[:])[:32]+".tunnel.aso.test.", dns.TypeTXT)
	}
	for i := range 6 {
		sum := sha256.Sum256(fmt.Appendf(nil, "fam-%d", i))
		ask(hex.EncodeToString(sum[:])[:24]+".fam.aso.test.", dns.TypeA)
	}
	for _, name := range []string{"ads.aso.test.", "tracker.aso.test.", "bad.aso.test.", "ok.aso.test."} {
		for range 5 {
			ask(name, dns.TypeA)
		}
	}

	now := time.Now().UTC()
	engineID := pgText(t, pg.URL, "select id::text from engines where node_name = $1", node)
	var resolver map[string]any
	admin.Must(http.MethodGet, "/resolver-settings", nil, &resolver, http.StatusOK)
	cacheMax, ok := resolver["cache_max_bytes"].(float64)
	if !ok || cacheMax <= 0 {
		t.Fatalf("resolver settings cache_max_bytes = %v", resolver["cache_max_bytes"])
	}
	// Eight days of per-hour samples: a rising upstream RTT and a SERVFAIL spike in the newest samples.
	// They end before the engine's own first sample: a live sample between two inserted ones would split
	// their pair (its lower query counter counts as a restart), and the spike would then only show when
	// this test's traffic happened to fall between two live samples.
	firstLive := time.UnixMilli(int64(pgInt(t, pg.URL,
		"select (extract(epoch from coalesce(min(at), now())) * 1000)::bigint from engine_stats where engine_id = $1", engineID)))
	last := firstLive.Add(-time.Second)
	if age := time.Since(last); age > 10*time.Minute {
		t.Fatalf("the engine's first sample is %v old; the inserted spike would fall outside the insight agent's 15-minute window", age)
	}
	insertEngineStats(t, pg.URL, engineID, "fixture", last, 8*24, uint64(cacheMax*0.6))

	// Ten days of capacity samples for the two resources the forecast agent must cover.
	for day := 10; day >= 1; day-- {
		harness.PGExec(t, pg.URL, `insert into ai_capacity_samples(day, resource, value, limit_value)
			values ($1::date, 'cache', $2, $3), ($1::date, 'blocklist_entries', $4, null)
			on conflict do nothing`,
			now.AddDate(0, 0, -day), cacheMax*(0.5+0.03*float64(10-day)), cacheMax, 1000+200*(10-day))
	}

	// One threat verdict, so the RPZ suggestion agent has a candidate name.
	harness.PGExec(t, pg.URL, `insert into ai_domain_verdicts(name, is_threat, categories, confidence, reasoning, checked_at, expires_at)
		values ('bad.aso.test', true, '{c2}', 0.9, 'scripted verdict', now(), now() + interval '7 days')
		on conflict (name) do nothing`)

	// A rollout the risk agent assesses: one policy group update.
	var group struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}
	admin.Must(http.MethodPost, "/policy-groups", map[string]any{"name": "guests", "cidrs": []string{"10.9.0.0/16"}}, &group, http.StatusCreated)
	admin.Must(http.MethodPut, "/policy-groups/"+group.ID, map[string]any{"name": "guests",
		"cidrs": []string{"10.9.0.0/16"}, "description": "operator change", "revision": group.Revision}, &group, http.StatusOK)
	waitLatestApplied(t, admin, node)
	if n := pgInt(t, pg.URL, "select count(*) from rollouts"); n == 0 {
		t.Fatal("the policy group update started no rollout for the risk agent to assess")
	}

	// Every agent gets an answer that yields at least one proposal or finding. The anomaly and insight
	// agents store their detectors' findings whatever the model adds, so they answer with nothing.
	aiFx.ScriptJSON(t, "querylog_anomalies", map[string]any{"anomalies": []any{}})
	aiFx.ScriptJSON(t, "dashboard_insights", map[string]any{"insights": []any{}})
	aiFx.ScriptJSON(t, "filter_recommendations", map[string]any{"recommendations": []any{map[string]any{
		"kind": "block_domains", "policy_group_id": "", "domains": []string{"tracker.aso.test"}, "priority": "medium",
		"title": "Block tracker.aso.test", "description": "A tracker queried by clients in the last day."}}})
	aiFx.ScriptJSON(t, "upstream_prediction", map[string]any{"trend": "degrading", "confidence": 0.8,
		"reasoning": "The p99 round trip time of the upstream rises steadily over the last days.",
		"recommendation": map[string]any{"type": "switch_strategy", "strategy": "parallel",
			"description": "Query the upstreams in parallel while the fixture upstream degrades."}})
	aiFx.ScriptJSON(t, "rollout_risk", map[string]any{"risk_score": 5, "risk_level": "medium",
		"analysis":            "A policy group change reaches every client at once on a fleet with no rollout history.",
		"historical_patterns": []any{}, "recommendation": map[string]any{"strategy": "canary", "canary_count": 1,
			"min_health_queries": 200, "max_servfail_ratio": 0.03, "reasoning": "Prove the change on one engine first."}})
	aiFx.ScriptJSON(t, "threat_classification", map[string]any{"items": []any{
		map[string]any{"name": "casino.aso.test", "category": "none"}}})
	aiFx.ScriptJSON(t, "capacity_forecast", map[string]any{"forecasts": []any{
		map[string]any{"resource": "cache", "confidence": 0.8, "recommendation": "Raise the cache limit before it fills.",
			"cache_max_bytes": int64(cacheMax) * 2},
		map[string]any{"resource": "blocklist_entries", "confidence": 0.6, "recommendation": "Watch the blocklist growth."}}})
	aiFx.ScriptJSON(t, "rpz_suggestions", map[string]any{"rules": []any{map[string]any{
		"record": "bad.aso.test", "policy": "nxdomain", "category": "c2", "reason": "threat verdict", "confidence": 0.9}}})

	// The configuration as the operator left it.
	before := readConfigFingerprint(t, pg.URL)

	states := aiAgentStates(admin)
	for _, name := range []string{"querylog_anomalies", "dashboard_insights", "filter_recommendations",
		"upstream_prediction", "rollout_risk", "threat_classification", "capacity_forecast", "rpz_suggestions"} {
		if !states[name].Enabled {
			t.Fatalf("agent %s is not enabled on a configured instance: %+v", name, states)
		}
		admin.Must(http.MethodPost, "/ai/agents/"+name+"/run", nil, nil, http.StatusAccepted)
	}
	harness.EventuallyTrue(t, 5*time.Minute, func() bool {
		for _, st := range aiAgentStates(admin) {
			if st.Running || st.LastOutcome == "" {
				return false
			}
		}
		return true
	}, "every agent finished a run")
	for name, st := range aiAgentStates(admin) {
		if st.LastOutcome != "ok" && st.LastOutcome != "no_change" {
			t.Fatalf("agent %s finished %q: %s", name, st.LastOutcome, st.LastError)
		}
	}

	// Positive path first: the agents did their work.
	var proposals []struct {
		Source string `json:"source"`
		Status string `json:"status"`
	}
	admin.Must(http.MethodGet, "/ai/proposals?status=open", nil, &proposals, http.StatusOK)
	for _, source := range []string{"filter_recommendations", "upstream_prediction", "rollout_risk",
		"capacity_forecast", "rpz_suggestions"} {
		if !slices.ContainsFunc(proposals, func(p struct {
			Source string `json:"source"`
			Status string `json:"status"`
		}) bool {
			return p.Source == source
		}) {
			t.Fatalf("no open proposal from %s: %+v", source, proposals)
		}
	}
	for _, kind := range []string{"anomaly", "insight"} {
		var findings []struct {
			Kind        string `json:"kind"`
			CandidateID string `json:"candidate_id"`
		}
		admin.Must(http.MethodGet, "/ai/findings?kind="+kind, nil, &findings, http.StatusOK)
		if len(findings) == 0 {
			t.Fatalf("no %s finding after the agent runs", kind)
		}
		// The inserted SERVFAIL spike, not whatever this test's own traffic happened to show.
		if kind == "insight" && !slices.ContainsFunc(findings, func(f struct {
			Kind        string `json:"kind"`
			CandidateID string `json:"candidate_id"`
		}) bool {
			return f.CandidateID == "servfail_spike:"+node
		}) {
			t.Fatalf("no servfail_spike:%s insight: %+v", node, findings)
		}
	}

	// And they changed nothing.
	after := readConfigFingerprint(t, pg.URL)
	if after.Version != before.Version {
		t.Fatalf("the agents published a configuration version: %s -> %s", before.Version, after.Version)
	}
	for _, table := range configTables {
		if after.Checksums[table] != before.Checksums[table] {
			t.Fatalf("the agents changed %s: %s -> %s", table, before.Checksums[table], after.Checksums[table])
		}
	}
	if after.AuditCount != before.AuditCount {
		t.Fatalf("the agents wrote %d non-AI audit entries", after.AuditCount-before.AuditCount)
	}
}
