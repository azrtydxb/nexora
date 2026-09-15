package apispec_test

import (
	"testing"

	apispec "github.com/piwi3910/nexora/mgmt/api"
)

func TestOperationsIncludeM11(t *testing.T) {
	ops := apispec.Operations()
	for id, want := range map[string]string{
		"applyAiProposals":              "POST /ai/proposals/apply",
		"getAiRolloutRisk":              "GET /rollouts/{id}/ai-risk",
		"getAiFilterListClassification": "GET /filter-lists/{id}/ai-classification",
		"updatePolicyGroup":             "PUT /policy-groups/{id}",
	} {
		if got := ops[id].Method + " " + ops[id].Path; got != want {
			t.Errorf("%s = %q, want %q", id, got, want)
		}
	}
	if ops["updatePolicyGroup"].Body == nil {
		t.Fatal("updatePolicyGroup has no request body schema")
	}
}
