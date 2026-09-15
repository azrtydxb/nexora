package api

import (
	"context"
	"net/http"
	"net/url"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/config"
)

// GetAiStatus reports the AI layer's state. It is the one AI operation that answers while AI is off, and
// it never reports the API key.
func (h *handlers) GetAiStatus(ctx context.Context, _ GetAiStatusRequestObject) (GetAiStatusResponseObject, error) {
	out := GetAiStatus200JSONResponse{Agents: []AiAgentState{}}
	rt := h.d.AI
	if rt == nil {
		out.Reason = AiStatusReason(h.d.AIDisabledReason)
		return out, nil
	}
	c := rt.Config
	out.Enabled, out.Model, out.StructuredOutput = true, c.Model, c.StructuredOutput
	if u, err := url.Parse(c.BaseURL); err == nil {
		out.EndpointHost = u.Hostname() // never the userinfo
	}
	out.Features.QuerylogSearch = rt.TaskKinds[ai.TaskQueryLogSearch]
	out.Features.ThreatCheck = rt.TaskKinds[ai.TaskThreatCheck]
	out.Features.ConfigAssistant = rt.TaskKinds[ai.TaskAssistantMessage]
	out.Mcp.Enabled, out.Mcp.ReadOnly = c.MCPEnabled, c.MCPReadOnly

	b, err := rt.Service.Budget(ctx)
	if err != nil {
		return nil, err
	}
	out.Budget.Day = openapi_types.Date{Time: b.Day}
	out.Budget.LimitTokens, out.Budget.UsedTokens, out.Budget.BackgroundLimitTokens = b.LimitTokens, b.UsedTokens, b.BackgroundLimitTokens

	states, err := ai.AgentStates(ctx, h.d.Store, rt.registeredAgentsConfig(), rt.InstanceStart)
	if err != nil {
		return nil, err
	}
	for _, s := range states {
		out.Agents = append(out.Agents, AiAgentState{Name: AiAgentName(s.Name), Enabled: s.Enabled, Running: s.Running,
			IntervalSeconds: int64(s.Interval.Seconds()), LastStartedAt: s.LastStarted, LastFinishedAt: s.LastFinished,
			NextRunAt: s.Next, LastOutcome: s.LastOutcome, LastError: s.LastError})
	}
	return out, nil
}

// registeredAgentsConfig is the configuration with every agent that has no registered implementation
// disabled.
func (rt *AIRuntime) registeredAgentsConfig() config.AIConfig {
	c := rt.Config
	c.Agents = make(map[string]config.AgentConfig, len(rt.Config.Agents))
	for name, a := range rt.Config.Agents {
		a.Enabled = a.Enabled && rt.Agents[name]
		c.Agents[name] = a
	}
	return c
}

// RunAiAgent requests an immediate run of an enabled agent; the next scheduler tick on any instance runs it.
func (h *handlers) RunAiAgent(ctx context.Context, req RunAiAgentRequestObject) (RunAiAgentResponseObject, error) {
	rt, err := h.aiRuntime()
	if err != nil {
		return nil, err
	}
	if !req.Agent.Valid() {
		return nil, invalid("unknown agent %q", req.Agent)
	}
	name := string(req.Agent)
	if !rt.registeredAgentsConfig().Agents[name].Enabled {
		return nil, apiError{status: http.StatusNotFound, code: "unknown_agent", msg: "agent " + name + " is not enabled"}
	}
	if err := ai.RequestRun(ctx, h.d.Store, name, PrincipalFrom(ctx).Actor().Name); err != nil {
		return nil, err
	}
	return RunAiAgent202Response{}, nil
}
