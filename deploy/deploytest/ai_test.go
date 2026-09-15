package deploytest

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestHelmAISecretWiring proves the AI endpoint, model and key reach the management plane only through
// the operator's Secret, and that the chart ships the AI alerts.
func TestHelmAISecretWiring(t *testing.T) {
	base := []string{"--set", "mgmt.ca.existingSecret=ca", "--set", "database.mode=external", "--set", "database.external.existingSecret=pg",
		"--set-json", `engine.groups=[{"name":"default","joinTokenSecret":"jt"}]`}

	docs := render(t, append(base, "--set", "mgmt.ai.existingSecret=nexora-ai", "--set", "mgmt.mcp.enabled=true")...)
	mc := container(t, find(t, docs, "Deployment", "nexora-mgmt"), "mgmt")
	var want []any
	if err := yaml.Unmarshal([]byte(`
- name: NEXORA_AI_BASE_URL
  valueFrom: { secretKeyRef: { name: nexora-ai, key: base-url, optional: true } }
- name: NEXORA_AI_MODEL
  valueFrom: { secretKeyRef: { name: nexora-ai, key: model, optional: true } }
- name: NEXORA_AI_API_KEY
  valueFrom: { secretKeyRef: { name: nexora-ai, key: api-key, optional: true } }
- name: NEXORA_MCP_ENABLED
  value: "true"
- name: NEXORA_MCP_READ_ONLY
  value: "true"
`), &want); err != nil {
		t.Fatal(err)
	}
	for _, w := range want {
		wm := w.(map[string]any)
		if got := env(mc, wm["name"].(string)); !reflect.DeepEqual(map[string]any(got), wm) {
			t.Errorf("mgmt env %s = %v, want %v", wm["name"], got, wm)
		}
	}

	def := container(t, find(t, render(t, base...), "Deployment", "nexora-mgmt"), "mgmt")
	for _, e := range def["env"].([]any) {
		if name, _ := e.(map[string]any)["name"].(string); strings.HasPrefix(name, "NEXORA_AI_") {
			t.Errorf("default values render %s", name)
		}
	}

	// No file under deploy/ may carry the API key as a literal value. Chart templates are not plain
	// YAML; the renders above cover them.
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || (filepath.Ext(path) != ".yaml" && filepath.Ext(path) != ".yml") {
			return err
		}
		f, err := os.Open(path) // #nosec G304 -- walking the repository's own deploy tree
		if err != nil {
			return err
		}
		defer f.Close()
		dec := yaml.NewDecoder(f)
		for {
			var doc any
			if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
				return nil
			} else if err != nil {
				if strings.Contains(filepath.ToSlash(path), "/templates/") {
					return nil
				}
				t.Errorf("%s: %v", path, err)
				return nil
			}
			if literalAPIKey(doc) {
				t.Errorf("%s sets NEXORA_AI_API_KEY with a literal value", path)
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}

	rules := find(t, render(t, append(base, "--set", "metrics.prometheusRule.enabled=true", "--api-versions", "monitoring.coreos.com/v1")...), "PrometheusRule", "nexora")
	alerts := map[string]map[string]any{}
	for _, g := range rules.path("spec", "groups").([]any) {
		for _, r := range g.(map[string]any)["rules"].([]any) {
			if rm := r.(map[string]any); rm["alert"] != nil {
				alerts[rm["alert"].(string)] = rm
			}
		}
	}
	for name, w := range map[string][2]string{
		"NexoraAIAgentFailing":    {`increase(nexora_mgmt_ai_agent_runs_total{outcome="failed"}[1h]) > 0 and (time() - nexora_mgmt_ai_agent_last_success_timestamp_seconds) > 3 * 21600`, "15m"},
		"NexoraAIBudgetExhausted": {`nexora_mgmt_ai_budget_used_ratio >= 1`, "10m"},
	} {
		r := alerts[name]
		if r == nil {
			t.Errorf("alert %s missing", name)
			continue
		}
		if r["expr"] != w[0] || r["for"] != w[1] || obj(r).path("labels", "severity") != "warning" {
			t.Errorf("alert %s = %v, want expr %q for %s severity warning", name, r, w[0], w[1])
		}
	}
}

// literalAPIKey reports whether any mapping at any depth is an env entry naming NEXORA_AI_API_KEY with
// a value key.
func literalAPIKey(n any) bool {
	switch v := n.(type) {
	case map[string]any:
		if _, ok := v["value"]; ok && v["name"] == "NEXORA_AI_API_KEY" {
			return true
		}
		for _, c := range v {
			if literalAPIKey(c) {
				return true
			}
		}
	case []any:
		for _, c := range v {
			if literalAPIKey(c) {
				return true
			}
		}
	}
	return false
}
