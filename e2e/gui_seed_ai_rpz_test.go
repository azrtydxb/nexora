package e2e

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/piwi3910/nexora/e2e/harness"
)

func init() { registerGUISeed(seedAIRpz) }

// seedAIRpz gives 58-ai-rpz-suggestions.spec.ts the open RPZ suggestions it reviews record by
// record: two the spec applies into the ai-suggested.rpz zone and one it rejects with a reason.
// The rows go in by SQL, the way the agent's persist step writes them, because no API creates
// proposals.
func seedAIRpz(s guiSeedEnv) {
	rules := []struct {
		record, policy, category, reason string
		confidence                       float64
	}{
		{"c2.gui-rpz.test", "nxdomain", "malware", "Beacons every 60 s from one client with a high-entropy label.", 0.94},
		{"phish.gui-rpz.test", "nxdomain", "phishing", "Looks like a login page of a hosted zone, registered 2 days ago.", 0.81},
		{"keep.gui-rpz.test", "nodata", "tracking", "Third-party tracker on an internal tool the fleet still uses.", 0.62},
	}
	for _, r := range rules {
		actions, err := json.Marshal([]proposalAction{{
			OperationID: "appendAiRpzRules",
			PathParams:  map[string]string{},
			Body: map[string]any{"rules": []map[string]any{{
				"record": r.record, "policy": r.policy, "category": r.category,
				"reason": r.reason, "confidence": r.confidence,
			}}},
			Explanation: fmt.Sprintf("Adds %s to the ai-suggested.rpz zone.", r.record),
		}})
		if err != nil {
			s.T.Fatalf("marshal rpz actions: %v", err)
		}
		harness.PGExec(s.T, s.PGURL, insertProposalSQL, uuid.NewString(), "rpz_suggestions",
			"gui:rpz_suggestions:"+r.record, "Block "+r.record,
			fmt.Sprintf("%s was queried by the fleet and is not blocked by any list.", r.record),
			"high", `{"clients":2,"queries":37}`, `{"kind":"threat","window_hours":24}`, string(actions))
	}
}

// proposalAction is one action of a seeded proposal, in the shape mgmt stores and decodes.
type proposalAction struct {
	OperationID string            `json:"operation_id"`
	PathParams  map[string]string `json:"path_params"`
	Body        map[string]any    `json:"body"`
	Explanation string            `json:"explanation"`
}
