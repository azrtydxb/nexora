package api_test

import (
	"encoding/json"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/api"
)

func TestMergedContractPreservesNullableAIAndM8(t *testing.T) {
	spec, err := api.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"last_started_at", "last_finished_at", "next_run_at"} {
		field := spec.Components.Schemas["AiAgentState"].Value.Properties[name].Value
		if !field.Type.Includes("null") || !field.Type.Includes("string") {
			t.Fatalf("AI %s lost nullable timestamp: %v", name, field.Type)
		}
	}
	var state api.AiAgentState
	if err := json.Unmarshal([]byte(`{"last_started_at":null,"last_finished_at":null,"next_run_at":null}`), &state); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"last_started_at", "last_finished_at", "next_run_at"} {
		value, present := fields[name]
		if !present || value != nil {
			t.Fatalf("AI timestamp %s must serialize explicit null: %s", name, data)
		}
	}
	for schema, names := range map[string][]string{
		"EngineGroup": {"mdns"},
		"Zone":        {"zonemd_generate", "zonemd_verify", "catalog_zone_id"},
	} {
		for _, name := range names {
			if spec.Components.Schemas[schema].Value.Properties[name] == nil {
				t.Fatalf("missing %s.%s", schema, name)
			}
		}
	}
}
