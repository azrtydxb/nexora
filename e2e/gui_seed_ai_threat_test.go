package e2e

import (
	"github.com/piwi3910/nexora/e2e/harness"
)

func init() { registerGUISeed(seedAIThreat) }

// threatListName is the block list 57-ai-threat.spec.ts expands to read its AI classification.
const threatListName = "gui-ai"

// seedAIThreat gives 57-ai-threat.spec.ts the model answer of the threat check it runs and one
// classified block list.
//
// The list is created here rather than looked up: at seed time no filter list exists yet
// (04-filtering.spec.ts creates and deletes its own), and ai_list_classifications references
// filter_lists. It subscribes to the same two-name fixture list as 04, so entry_count is 2 and the
// seeded estimates (1 tracking, 1 none out of a 200-name sample) are the ones the agent would write.
func seedAIThreat(s guiSeedEnv) {
	// One call judges up to 20 names, and the fixture repeats its last answer; the second viewport's
	// check is answered from the seven-day verdict cache and calls no model at all.
	s.AI.ScriptJSON(s.T, "threat_check", map[string]any{"verdicts": []map[string]any{{
		"name": "evil.gui-threat.test", "is_threat": true, "categories": []string{"phishing"},
		"confidence": 0.9,
		"reasoning":  "The name imitates a bank sign-in page and was first seen two days ago.",
	}}})

	var list struct {
		ID string `json:"id"`
	}
	s.Admin.Must("POST", "/filter-lists", map[string]any{
		"name": threatListName, "kind": "block", "url": s.Web.URL("gui"),
		"refresh_interval_seconds": 86400, "enabled": true, "engine_group_id": nil,
	}, &list, 201)
	// Fetch it once so the list has an entry count and a content hash to classify.
	s.Admin.Must("POST", "/filter-lists/"+list.ID+"/refresh", nil, nil, 200)

	// Only the classification agent writes this table, and it never runs in the GUI test
	// (NEXORA_AI_AGENT_START_DELAY is 1000h and no spec runs it): seed the row directly.
	harness.PGExec(s.T, s.PGURL,
		`INSERT INTO ai_list_classifications (list_id, blob_sha256, sample_size, breakdown, classified_at)
		 SELECT f.id, coalesce(f.current_blob_sha256, ''), 200,
		        '[{"category":"tracking","sampled":120,"estimated":1},{"category":"none","sampled":80,"estimated":1}]'::jsonb,
		        now()
		 FROM filter_lists f WHERE f.id = $1`, list.ID)

	s.Vars["NEXORA_E2E_AI_LIST_ID"] = list.ID
	s.Vars["NEXORA_E2E_AI_LIST_NAME"] = threatListName
	s.Vars["NEXORA_E2E_AI_THREAT_DOMAIN"] = "evil.gui-threat.test"
}
