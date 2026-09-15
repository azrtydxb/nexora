package e2e

import (
	"github.com/piwi3910/nexora/e2e/harness"
)

func init() { registerGUISeed(seedAIInsights) }

// insertFindingSQL writes one finding the way an agent's persist step would; the GUI seeds take this
// route because no API creates findings.
const insertFindingSQL = `INSERT INTO ai_findings
  (kind, candidate_id, type, status, severity, confidence, title, description, detail, explained, first_seen, last_seen)
VALUES ($1, $2, $3, 'open', $4, $5, $6, $7, $8::jsonb, $9, now() - interval '20 minutes', now() - interval '2 minutes')`

// seedAIInsights gives 51-ai-insights.spec.ts the findings it acts on: an explained tunnelling
// anomaly it acknowledges, a second anomaly that stays open (52-ai-querylog.spec.ts counts the open
// anomalies), an insight with possible causes it reads and a second insight it dismisses.
func seedAIInsights(s guiSeedEnv) {
	exec := func(kind, candidateID, typ, severity string, confidence float64, title, description, detail string, explained bool) {
		harness.PGExec(s.T, s.PGURL, insertFindingSQL, kind, candidateID, typ, severity, confidence, title, description, detail, explained)
	}
	exec("anomaly", "dns_tunneling:10.0.1.45", "dns_tunneling", "critical", 0.92,
		"Possible DNS tunnelling from 10.0.1.45",
		"812 TXT lookups of long random labels under tunnel.gui.test in 15 minutes.",
		`{"affected_clients":["10.0.1.45"],"sample_domains":["b3f1c0d2e4a5.tunnel.gui.test","7a9e1f3b8c2d.tunnel.gui.test"],`+
			`"query_count":812,"recommended_actions":["Isolate 10.0.1.45","Block tunnel.gui.test"]}`, true)
	exec("anomaly", "nxdomain_burst:10.0.2.77", "nxdomain_burst", "warning", 0.64,
		"NXDOMAIN burst from 10.0.2.77",
		"310 NXDOMAIN answers in 15 minutes, 88% of this client's queries.",
		`{"affected_clients":["10.0.2.77"],"sample_domains":["ovh1.nx.gui.test","ovh2.nx.gui.test"],"query_count":310,"recommended_actions":[]}`, false)
	exec("insight", "servfail_spike:gui-engine", "servfail_spike", "warning", 0.81,
		"SERVFAIL spike on gui-engine",
		"SERVFAIL answers on gui-engine rose from 0.4% to 7% of queries against the last 24 hours.",
		`{"possible_causes":[{"cause":"The fixture upstream started refusing forwarded names","confidence":0.72,"supporting_candidates":["upstream_degraded:fixture"]},`+
			`{"cause":"DNSSEC validation fails for a forwarded zone","confidence":0.31,"supporting_candidates":[]}],`+
			`"recommended_actions":["Check the fixture upstream"],"related_candidates":["upstream_degraded:fixture"],"engines":["gui-engine"]}`, true)
	exec("insight", "upstream_degraded:fixture", "upstream_degraded", "warning", 0.58,
		"Upstream fixture is slow",
		"Upstream fixture answers in 240 ms against 30 ms over the last 24 hours.",
		`{"possible_causes":[{"cause":"The upstream is rate limiting the fleet","confidence":0.45,"supporting_candidates":[]}],`+
			`"recommended_actions":["Add a second upstream"],"related_candidates":[],"engines":["gui-engine","gui-engine-2"]}`, false)

	s.Vars["NEXORA_E2E_AI_ANOMALY"] = "dns_tunneling:10.0.1.45"
	s.Vars["NEXORA_E2E_AI_ANOMALY_CLIENT"] = "10.0.1.45"
	s.Vars["NEXORA_E2E_AI_ANOMALY_OPEN"] = "nxdomain_burst:10.0.2.77"
	s.Vars["NEXORA_E2E_AI_INSIGHT"] = "servfail_spike:gui-engine"
	s.Vars["NEXORA_E2E_AI_INSIGHT_DISMISS"] = "upstream_degraded:fixture"
}
