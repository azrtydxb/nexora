package e2e

import (
	"encoding/json"

	"github.com/piwi3910/nexora/e2e/harness"
)

func init() { registerGUISeed(seedAIAssistant) }

// seedAIAssistant scripts the configuration assistant answer 54-ai-assistant.spec.ts plans with: one
// createPolicyGroup action the spec reviews, applies and then finds on /policies. The last scripted
// response repeats, so this one entry answers every turn the spec takes. The answer is held until the
// spec has seen the task in progress and releases it through NEXORA_E2E_AI_CONTROL_URL, so the
// "Thinking" status never races the fake model.
func seedAIAssistant(s guiSeedEnv) {
	plan, err := json.Marshal(map[string]any{
		"reply":   "I will create guest-wifi-gui.",
		"summary": "Create guest-wifi-gui",
		"actions": []any{map[string]any{
			"operation_id": "createPolicyGroup",
			"path_params":  map[string]string{},
			"body":         map[string]any{"name": "guest-wifi-gui", "cidrs": []string{"10.99.0.0/24"}, "category_keys": []string{"malware"}},
			"explanation":  "New group",
		}},
	})
	if err != nil {
		s.T.Fatal(err)
	}
	s.AI.Script(s.T, "config_assistant", harness.OpenAIResponse{Content: string(plan), Hold: true,
		PromptTokens: 120, CompletionTokens: 80, ReasoningTokens: 40})
	s.Vars["NEXORA_E2E_AI_CONTROL_URL"] = s.AI.ControlURL()
}
