package mcpserver

import (
	"encoding/json"
	"regexp"
	"slices"
	"testing"

	apispec "github.com/piwi3910/nexora/mgmt/api"
)

// TestToolsMapToOperations catches a tool bound to an operation the API does not have, a tool name MCP
// clients reject or that collides, and an input schema that loses path parameters, the required body or
// the request schema behind a $ref/allOf.
func TestToolsMapToOperations(t *testing.T) {
	ops := apispec.Operations()
	nameRE := regexp.MustCompile(`^nexora_[a-z0-9_]+$`)
	seen := map[string]bool{}
	for _, tool := range Tools {
		if _, ok := ops[tool.OperationID]; !ok {
			t.Errorf("%s: operation %s does not exist", tool.Name, tool.OperationID)
		}
		if !nameRE.MatchString(tool.Name) {
			t.Errorf("tool name %q is not ^nexora_[a-z0-9_]+$", tool.Name)
		}
		if seen[tool.Name] {
			t.Errorf("tool name %q is not unique", tool.Name)
		}
		seen[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("%s has no description", tool.Name)
		}
	}
	if len(Tools) != 35 {
		t.Errorf("len(Tools) = %d, want the 35 tools of #52", len(Tools))
	}

	raw, err := json.Marshal(inputSchema(ops["updatePolicyGroup"]))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Type       string   `json:"type"`
		Required   []string `json:"required"`
		Properties struct {
			ID   struct{ Type string } `json:"id"`
			Body struct {
				Required   []string `json:"required"`
				Properties struct {
					CIDRs struct {
						Type  string `json:"type"`
						Items struct {
							Type string `json:"type"`
						} `json:"items"`
					} `json:"cidrs"`
					Revision struct {
						Type string `json:"type"`
					} `json:"revision"`
				} `json:"properties"`
			} `json:"body"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" || !slices.Contains(schema.Required, "id") || !slices.Contains(schema.Required, "body") {
		t.Fatalf("nexora_policy_groups_update schema = %s", raw)
	}
	if schema.Properties.ID.Type != "string" || schema.Properties.Body.Properties.CIDRs.Type != "array" ||
		schema.Properties.Body.Properties.CIDRs.Items.Type != "string" || schema.Properties.Body.Properties.Revision.Type != "integer" ||
		!slices.Contains(schema.Properties.Body.Required, "revision") || !slices.Contains(schema.Properties.Body.Required, "cidrs") {
		t.Fatalf("nexora_policy_groups_update body schema lost the allOf parts: %s", raw)
	}

	raw, err = json.Marshal(inputSchema(ops["listPolicyGroups"]))
	if err != nil {
		t.Fatal(err)
	}
	var list map[string]any
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if list["type"] != "object" || list["required"] != nil {
		t.Fatalf("a parameterless GET schema = %s, want an object without required", raw)
	}
}
