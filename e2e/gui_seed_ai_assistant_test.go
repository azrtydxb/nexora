package e2e

func init() { registerGUISeed(seedAIAssistant) }

// seedAIAssistant scripts the configuration assistant answer 54-ai-assistant.spec.ts plans with: one
// createPolicyGroup action the spec reviews, applies and then finds on /policies. The last scripted
// response repeats, so this one entry answers every turn the spec takes.
func seedAIAssistant(s guiSeedEnv) {
	s.AI.ScriptJSON(s.T, "config_assistant", map[string]any{
		"reply":   "I will create guest-wifi-gui.",
		"summary": "Create guest-wifi-gui",
		"actions": []any{map[string]any{
			"operation_id": "createPolicyGroup",
			"path_params":  map[string]string{},
			"body":         map[string]any{"name": "guest-wifi-gui", "cidrs": []string{"10.99.0.0/24"}, "category_keys": []string{"malware"}},
			"explanation":  "New group",
		}},
	})
}
