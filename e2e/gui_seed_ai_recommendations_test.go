package e2e

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/piwi3910/nexora/e2e/harness"
)

func init() { registerGUISeed(seedAIRecommendations) }

// insertProposalSQL writes one open proposal the way an agent's persist step would; the GUI seeds
// take this route because no API creates proposals.
const insertProposalSQL = `INSERT INTO ai_proposals
  (id, source, fingerprint, status, title, description, priority, impact, evidence, actions)
VALUES ($1, $2, $3, 'open', $4, $5, $6, $7::jsonb, $8::jsonb, $9::jsonb)`

// seedAIRecommendations gives 53-ai-recommendations.spec.ts the two open proposals it acts on: a
// filter recommendation it applies (malware is already enabled by TestGUICoverage, so the replay
// answers 200 and changes nothing) and an upstream prediction it dismisses. A third proposal stays
// open for the specs that only read the open list.
func seedAIRecommendations(s guiSeedEnv) {
	marshal := func(v any) string {
		out, err := json.Marshal(v)
		if err != nil {
			s.T.Fatalf("marshal proposal payload: %v", err)
		}
		return string(out)
	}

	malware := s.Admin.FilterCategory("malware")
	enable := marshal([]map[string]any{{
		"operation_id": "updateFilterCategory",
		"path_params":  map[string]string{"key": "malware"},
		"body":         map[string]any{"enabled": true, "revision": malware.Revision},
		"explanation":  "Enabling the malware category blocks the 30 malware domains this fleet still resolves.",
	}})
	applyID := uuid.NewString()
	harness.PGExec(s.T, s.PGURL, insertProposalSQL, applyID, "filter_recommendations", "gui:filter_recommendations:malware",
		"Enable malware for all clients",
		"30 of the last 110 blocked-worthy lookups are malware domains the category covers.",
		"high", `{"additional_blocked":30,"total_candidates":110,"coverage_percent":27.3}`,
		`{"sample_domains":["malware.gui.test"],"window_hours":24}`, enable)

	// The upstream prediction switches the resolver strategy: its body is the live settings with
	// strategy replaced, the way the upstream prediction agent builds it.
	var settings map[string]any
	s.Admin.Must(http.MethodGet, "/resolver-settings", nil, &settings, http.StatusOK)
	settings["strategy"] = "fastest"
	switchStrategy := marshal([]map[string]any{{
		"operation_id": "updateResolverSettings",
		"path_params":  map[string]string{},
		"body":         settings,
		"explanation":  "The fastest strategy answers from whichever upstream replies first.",
	}})
	dismissID := uuid.NewString()
	harness.PGExec(s.T, s.PGURL, insertProposalSQL, dismissID, "upstream_prediction", "gui:upstream_prediction:strategy",
		"Switch to fastest",
		"Upstream fixture is trending 8 ms slower per hour; the fastest strategy avoids it while it degrades.",
		"medium", `{"upstream":"fixture","trend":"degrading"}`,
		`{"samples":48,"slope_ms_per_hour":8.1}`, switchStrategy)

	// A third proposal nobody acts on, so an open proposal is still listed after 53 applies one and
	// dismisses the other: 60-ai-viewer.spec.ts reads the open list as a viewer.
	settings["strategy"] = "ordered"
	cache, ok := settings["cache_max_bytes"].(float64)
	if !ok {
		s.T.Fatalf("resolver settings cache_max_bytes = %v", settings["cache_max_bytes"])
	}
	settings["cache_max_bytes"] = int64(cache) * 2 // an integer, not the float64 JSON decoded
	grow := marshal([]map[string]any{{
		"operation_id": "updateResolverSettings",
		"path_params":  map[string]string{},
		"body":         settings,
		"explanation":  "The cache evicts before its TTLs expire at the current limit.",
	}})
	harness.PGExec(s.T, s.PGURL, insertProposalSQL, uuid.NewString(), "capacity_forecast", "gui:capacity_forecast:cache",
		"Double the cache limit",
		"The cache reaches its limit in 9 days at the current growth.",
		"low", `{"days_to_limit":9}`, `{"samples":288}`, grow)

	s.Vars["NEXORA_E2E_AI_PROPOSAL_APPLY"] = applyID
	s.Vars["NEXORA_E2E_AI_PROPOSAL_DISMISS"] = dismissID
}
