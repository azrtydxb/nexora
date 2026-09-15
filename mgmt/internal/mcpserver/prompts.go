package mcpserver

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
)

// maxPromptMinutes bounds the nexora_query_log window to one week.
const maxPromptMinutes = 7 * 24 * 60

type prompt struct {
	Name, Description, Text string
	Arguments               []map[string]any
}

var prompts = []prompt{
	{Name: "nexora_status", Description: "Summarise the Nexora fleet status.",
		Text: "Summarise the Nexora fleet status. Use nexora_fleet_summary and nexora_dashboard_get."},
	{Name: "nexora_query_log", Description: "Show recent Nexora query log entries.",
		Text:      "Show Nexora query log entries from the last {minutes} minutes using nexora_query_log_query.",
		Arguments: []map[string]any{{"name": "minutes", "description": "window in minutes (default 15)", "required": false}}},
	{Name: "nexora_filter_health", Description: "Report stale or failing filter lists.",
		Text: "Report stale or failing filter lists using nexora_filter_lists_list and nexora_filter_categories_list."},
}

func (s *server) listPrompts(*http.Request, auth.Principal, json.RawMessage) (any, *rpcError) {
	out := make([]map[string]any, len(prompts))
	for i, p := range prompts {
		out[i] = map[string]any{"name": p.Name, "description": p.Description}
		if p.Arguments != nil {
			out[i]["arguments"] = p.Arguments
		}
	}
	return map[string]any{"prompts": out}, nil
}

func (s *server) getPrompt(_ *http.Request, _ auth.Principal, params json.RawMessage) (any, *rpcError) {
	var in struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	if err := decodeParams(params, &in); err != nil {
		return nil, err
	}
	for _, p := range prompts {
		if p.Name != in.Name {
			continue
		}
		text := p.Text
		if p.Name == "nexora_query_log" {
			minutes := 15
			if v, ok := in.Arguments["minutes"]; ok {
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 || n > maxPromptMinutes {
					return nil, &rpcError{codeInvalidParams, "minutes must be an integer from 1 to " + strconv.Itoa(maxPromptMinutes)}
				}
				minutes = n
			}
			text = strings.ReplaceAll(text, "{minutes}", strconv.Itoa(minutes))
		}
		return map[string]any{"description": p.Description, "messages": []map[string]any{{
			"role": "user", "content": map[string]string{"type": "text", "text": text},
		}}}, nil
	}
	return nil, &rpcError{codeInvalidParams, "unknown prompt: " + in.Name}
}
