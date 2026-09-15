package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/proposal"
	"github.com/piwi3910/nexora/mgmt/internal/catalog"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Feature is the model call feature label of the assistant.
const Feature ai.Feature = "config_assistant"

// conversationMessages is how many messages of a session the model receives.
const conversationMessages = 20

// Plan is the model's ConfigChangePlan: a reply, and optionally a summary and the actions of a proposal.
type Plan struct {
	Reply   string            `json:"reply"`
	Summary string            `json:"summary"`
	Actions []proposal.Action `json:"actions"`
}

// TaskInput is the input of an assistant_message task: the user message to answer.
type TaskInput struct {
	SessionID uuid.UUID `json:"session_id"`
	MessageID int64     `json:"message_id"`
}

// highRisk lists the operations whose change is high-risk before apply (the high-risk types of S-9).
var highRisk = map[string]bool{"updateResolverSettings": true, "updatePolicyGroup": true, "createPolicyGroup": true,
	"updateEngineGroup": true}

const system = `You are the Nexora DNS configuration assistant. You read the conversation and the current configuration and answer with a JSON ConfigChangePlan {reply, summary, actions}.
- reply: your answer to the operator. If the request is ambiguous, ask one clarifying question and return no actions.
- summary: one line naming the change, required when there are actions.
- actions: at most 8 API calls {operation_id, path_params, body, explanation}. Nothing is applied until an operator reviews and applies the plan.
Plans may use only these operations: createPolicyGroup, updatePolicyGroup (path id), updateFilterCategory (path key), updateGlobalSafeSearch, updateAllowlist, updateResolverSettings, updateUpstream (path id), updateEngineGroup (path id).
Every body must be the complete request body of the operation, copied from the current configuration with only the requested change, and must carry the current revision of the resource (createPolicyGroup has no revision). category_keys must be filter category keys from the configuration and cidrs must be valid CIDR prefixes.`

// NewTask returns the assistant_message task: it asks the model for a plan for the user message, stores
// a plan with actions as a config_assistant proposal that supersedes the session's previous open plan,
// and appends the assistant message. The result is {message_id, proposal_id}.
func NewTask(st *store.Store, svc *ai.Service, v *proposal.Validator, now func() time.Time) ai.TaskFunc {
	names := map[string]string{}
	if c, err := catalog.Load(); err == nil {
		for _, cat := range c.Categories {
			names[cat.Key] = cat.Name
		}
	}
	return func(ctx context.Context, t ai.Task) (any, error) {
		var in TaskInput
		if err := json.Unmarshal(t.Input, &in); err != nil {
			return nil, fmt.Errorf("assistant task input: %w", err)
		}
		if _, err := getSession(ctx, st, in.SessionID); err != nil {
			return nil, err
		}
		conv, err := messages(ctx, st, in.SessionID, in.MessageID, conversationMessages)
		if err != nil {
			return nil, err
		}
		cfg, err := configContext(ctx, st, names)
		if err != nil {
			return nil, err
		}
		type turn struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		turns := make([]turn, len(conv))
		for i, m := range conv {
			turns[i] = turn{m.Role, m.Content}
		}
		prompt := fmt.Sprintf("Current time: %s\nConversation (oldest first, the last message is the one to answer):\n%s\nCurrent configuration:\n%s",
			now().UTC().Format(time.RFC3339), ai.DataBlock(turns), ai.DataBlock(cfg))
		res, err := ai.Generate(ctx, svc, ai.Request[Plan]{Feature: Feature, Priority: ai.Interactive, System: system, Prompt: prompt,
			Validate: func(p *Plan) error { return validatePlan(ctx, st, v, p) }})
		if err != nil {
			return nil, err
		}
		plan := res.Value

		reply := Message{SessionID: in.SessionID, Role: "assistant", Content: plan.Reply, TaskID: &t.ID}
		if len(plan.Actions) > 0 {
			id, err := storeProposal(ctx, st, in.SessionID, plan)
			if err != nil {
				return nil, err
			}
			if id != uuid.Nil {
				reply.ProposalID = &id
			}
		}
		msg, err := addMessage(ctx, st, reply, "")
		if err != nil {
			return nil, err
		}
		return map[string]any{"message_id": msg.ID, "proposal_id": reply.ProposalID}, nil
	}
}

// validatePlan checks the reply and summary, the actions with the proposal validator, and the category
// keys and CIDRs the validator does not check.
func validatePlan(ctx context.Context, st *store.Store, v *proposal.Validator, p *Plan) error {
	if strings.TrimSpace(p.Reply) == "" {
		return errors.New("reply is required")
	}
	if len(p.Actions) == 0 {
		return nil
	}
	if strings.TrimSpace(p.Summary) == "" {
		return errors.New("summary is required when there are actions")
	}
	if err := v.Validate(ctx, p.Actions); err != nil {
		return err
	}
	for i, a := range p.Actions {
		var body struct {
			CategoryKeys []string `json:"category_keys"`
			CIDRs        []string `json:"cidrs"`
		}
		if len(a.Body) > 0 {
			if err := json.Unmarshal(a.Body, &body); err != nil {
				return fmt.Errorf("actions[%d] %s: body: %w", i, a.OperationID, err)
			}
		}
		for _, c := range body.CIDRs {
			if _, err := netip.ParsePrefix(c); err != nil {
				return fmt.Errorf("actions[%d] %s: cidr %q is not a valid CIDR prefix", i, a.OperationID, c)
			}
		}
		if len(body.CategoryKeys) == 0 {
			continue
		}
		rows, err := st.Pool.Query(ctx, `select key from filter_categories where key = any($1)`, body.CategoryKeys)
		if err != nil {
			return store.MapError(err)
		}
		known := map[string]bool{}
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				rows.Close()
				return store.MapError(err)
			}
			known[k] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return store.MapError(err)
		}
		for _, k := range body.CategoryKeys {
			if !known[k] {
				return fmt.Errorf("actions[%d] %s: unknown filter category %q", i, a.OperationID, k)
			}
		}
	}
	return nil
}

// storeProposal upserts the plan as a proposal of the session and supersedes the session's other open
// plans. It returns uuid.Nil when the same plan was dismissed recently.
func storeProposal(ctx context.Context, st *store.Store, sessionID uuid.UUID, plan Plan) (uuid.UUID, error) {
	ops := []string{}
	high := false
	seen := map[string]bool{}
	for _, a := range plan.Actions {
		if !seen[a.OperationID] {
			seen[a.OperationID] = true
			ops = append(ops, a.OperationID)
		}
		high = high || highRisk[a.OperationID]
	}
	id, _, err := proposal.Upsert(ctx, st, proposal.Draft{Source: string(Feature), SessionID: &sessionID, Title: plan.Summary,
		Description: plan.Reply, Priority: "medium", Risk: map[string]any{"changed_operations": ops, "high_risk": high},
		Actions: plan.Actions})
	if err != nil || id == uuid.Nil {
		return id, err
	}
	return id, proposal.SupersedeSession(ctx, st, sessionID, id)
}

// configContext is the compact configuration the model plans against. It never holds TSIG keys, CA
// certificates or other secrets.
func configContext(ctx context.Context, st *store.Store, categoryNames map[string]string) (map[string]any, error) {
	groups, err := store.ListPolicyGroups(ctx, st.Pool)
	if err != nil {
		return nil, err
	}
	type groupOut struct {
		ID            uuid.UUID      `json:"id"`
		Name          string         `json:"name"`
		Description   string         `json:"description"`
		EngineGroupID *uuid.UUID     `json:"engine_group_id"`
		CIDRs         []netip.Prefix `json:"cidrs"`
		CategoryKeys  []string       `json:"category_keys"`
		FilterListIDs []uuid.UUID    `json:"filter_list_ids"`
		Allowlist     []string       `json:"allowlist"`
		SafeSearch    map[string]any `json:"safe_search"`
		Revision      int64          `json:"revision"`
	}
	groupsOut := make([]groupOut, len(groups))
	for i, g := range groups {
		groupsOut[i] = groupOut{ID: g.ID, Name: g.Name, Description: g.Description, EngineGroupID: g.EngineGroupID, CIDRs: g.CIDRs,
			CategoryKeys: g.CategoryKeys, FilterListIDs: g.FilterListIDs, Allowlist: g.Allowlist, Revision: g.Revision,
			SafeSearch: map[string]any{"google": g.SafeSearch.Google, "bing": g.SafeSearch.Bing, "duckduckgo": g.SafeSearch.DuckDuckGo,
				"youtube": g.SafeSearch.YouTube}}
	}
	cats, err := store.ListFilterCategories(ctx, st.Pool)
	if err != nil {
		return nil, err
	}
	catsOut := make([]map[string]any, len(cats))
	for i, c := range cats {
		catsOut[i] = map[string]any{"key": c.Key, "name": categoryNames[c.Key], "enabled": c.Enabled, "revision": c.Revision}
	}
	ss, err := store.GetGlobalSafeSearch(ctx, st.Pool)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"policy_groups":     groupsOut,
		"filter_categories": catsOut,
		"global_safe_search": map[string]any{"google": ss.Google, "bing": ss.Bing, "duckduckgo": ss.DuckDuckGo, "youtube": ss.YouTube,
			"revision": ss.Revision},
	}
	for key, sql := range map[string]string{
		"custom_filter_lists": `select coalesce(jsonb_agg(jsonb_build_object('id', id, 'name', name, 'kind', kind,
			'engine_group_id', engine_group_id) order by name), '[]') from filter_lists where not managed_by_catalog`,
		"engine_groups": `select coalesce(jsonb_agg(to_jsonb(g) - 'created_at' - 'updated_at' - 'stable_version' order by name), '[]')
			from engine_groups g`,
		"upstreams": `select coalesce(jsonb_agg(jsonb_build_object('id', id, 'name', name, 'protocol', protocol, 'address', address,
			'tls_server_name', tls_server_name, 'doh_url', doh_url, 'timeout_ms', timeout_ms, 'position', position, 'enabled', enabled,
			'engine_group_id', engine_group_id, 'revision', revision) order by position), '[]') from upstreams`,
		"access_control":    `select to_jsonb(a) - 'singleton' - 'updated_at' from access_control a`,
		"allowlist":         `select to_jsonb(a) - 'singleton' - 'updated_at' from allowlist a`,
		"resolver_settings": `select to_jsonb(r) - 'singleton' - 'updated_at' from resolver_settings r`,
		"rpz_zone_names":    `select coalesce(jsonb_agg(name order by position), '[]') from rpz_zones`,
	} {
		var raw json.RawMessage
		if err := st.Pool.QueryRow(ctx, sql).Scan(&raw); err != nil {
			return nil, fmt.Errorf("assistant context %s: %w", key, store.MapError(err))
		}
		out[key] = raw
	}
	return out, nil
}
