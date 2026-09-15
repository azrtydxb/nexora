package e2e

import (
	"strings"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

func init() { registerGUISeed(seedAIQueryLog) }

// seedAIQueryLog gives 52-ai-querylog.spec.ts one lookup of a name with a cached AI threat verdict,
// and scripts the two model answers of the natural-language query-log search the spec runs. The open
// anomaly its banner shows comes from the Task 23 seed.
func seedAIQueryLog(s guiSeedEnv) {
	name := "threat.aiq-gui.test."
	harness.MustQuery(s.T, s.Engine.DNS, name, dns.TypeA, harness.QueryOpts{})
	// The threat verdict cache is only written by the threat-check task: seed it directly.
	harness.PGExec(s.T, s.PGURL,
		`INSERT INTO ai_domain_verdicts (name, is_threat, categories, confidence, reasoning, checked_at, expires_at)
		 VALUES ($1, true, ARRAY['malware'], 0.95, 'seeded by the GUI coverage test', now(), now() + interval '7 days')
		 ON CONFLICT (name) DO UPDATE SET is_threat = excluded.is_threat, categories = excluded.categories,
		     confidence = excluded.confidence, checked_at = excluded.checked_at, expires_at = excluded.expires_at`,
		strings.TrimSuffix(name, "."))
	// The spec asks once per viewport width, and each ask is a translation call then a summary call:
	// script the pair twice, in the order the specs (one Playwright worker, no retries) make them.
	translation := map[string]any{"name": "threat.aiq-gui", "explanation": "Lookups of threat.aiq-gui.test"}
	summary := map[string]any{"summary": "1 lookup of threat.aiq-gui.test.", "suggestions": []string{"Check the client"}}
	s.AI.ScriptJSON(s.T, "querylog_search", translation, summary, translation, summary)
	s.Vars["NEXORA_E2E_AI_THREAT_NAME"] = strings.TrimSuffix(name, ".")
}
