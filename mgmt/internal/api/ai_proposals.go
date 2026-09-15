package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/piwi3910/nexora/mgmt/internal/ai/proposal"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
)

// Proposals are suggest-only: applyAiProposals is the only writer of AI suggestions into configuration,
// and it replays each action through the API with the reviewing caller's own credentials.

const codeProposalNotOpen = "proposal_not_open"

// licenseOperations accept acknowledge_license; apply merges the caller's acknowledgement into their bodies.
var licenseOperations = map[string]bool{"updateFilterCategory": true, "updatePolicyGroup": true, "createPolicyGroup": true}

// currentOperation is the GET operation whose response is the live resource an action changes; listKey
// names the path parameter that selects the entry of a list response.
var currentOperation = map[string]struct{ op, listKey string }{
	"updatePolicyGroup":      {op: "getPolicyGroup"},
	"updateFilterCategory":   {op: "listFilterCategories", listKey: "key"},
	"updateUpstream":         {op: "listUpstreams", listKey: "id"},
	"updateEngineGroup":      {op: "getEngineGroup"},
	"updateGlobalSafeSearch": {op: "getGlobalSafeSearch"},
	"updateAllowlist":        {op: "getAllowlist"},
	"updateResolverSettings": {op: "getResolverSettings"},
}

// applyActionOut and applyResultOut are the element types of AiApplyResponse.
type (
	applyActionOut = struct {
		Code        string `json:"code"`
		HttpStatus  int    `json:"http_status"`
		Message     string `json:"message"`
		OperationId string `json:"operation_id"`
	}
	applyResultOut = struct {
		Actions []applyActionOut   `json:"actions"`
		Id      openapi_types.UUID `json:"id"`
		Status  string             `json:"status"`
	}
)

func (h *handlers) ListAiProposals(ctx context.Context, req ListAiProposalsRequestObject) (ListAiProposalsResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	f := proposal.Filter{Limit: 100}
	if p := req.Params; p.Limit != nil {
		if *p.Limit < 1 || *p.Limit > 500 {
			return nil, invalid("limit must be between 1 and 500")
		}
		f.Limit = *p.Limit
	}
	if req.Params.Source != nil {
		f.Source = string(*req.Params.Source)
	}
	if req.Params.Status != nil {
		f.Status = string(*req.Params.Status)
	}
	list, err := proposal.List(ctx, h.d.Store, f)
	if err != nil {
		return nil, err
	}
	out := make(ListAiProposals200JSONResponse, 0, len(list))
	for _, p := range list {
		out = append(out, proposalOut(p))
	}
	return out, nil
}

func (h *handlers) GetAiProposal(ctx context.Context, req GetAiProposalRequestObject) (GetAiProposalResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	p, err := proposal.Get(ctx, h.d.Store.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	out := proposalOut(p)
	for i, a := range p.Actions {
		cur, err := h.currentResource(ctx, a)
		if err != nil {
			return nil, err
		}
		out.Actions[i].Current = cur
	}
	return GetAiProposal200JSONResponse(out), nil
}

// currentResource reads the live resource an action changes through its GET operation with the caller's
// credentials; nil for creations and for a resource the caller cannot read or that is gone.
func (h *handlers) currentResource(ctx context.Context, a proposal.Action) (*map[string]any, error) {
	if a.OperationID == proposal.OpAppendAiRpzRules {
		var n int
		if err := h.d.Store.Pool.QueryRow(ctx, "select count(*) from ai_rpz_rules").Scan(&n); err != nil {
			return nil, err
		}
		return &map[string]any{"zone": proposal.RPZZoneName, "applied_rules": n}, nil
	}
	c, ok := currentOperation[a.OperationID]
	if !ok {
		return nil, nil
	}
	call := replayCall{OperationID: c.op, PathParams: a.PathParams}
	if c.listKey != "" {
		call.PathParams = nil
	}
	res, err := h.replay(ctx, call)
	var aerr apiError
	if errors.As(err, &aerr) {
		return nil, nil // a replayed getAiProposal (MCP) cannot replay again: no current
	}
	if err != nil {
		return nil, err
	}
	if res.Status != http.StatusOK {
		return nil, nil
	}
	if c.listKey == "" {
		var m map[string]any
		if json.Unmarshal(res.Body, &m) != nil {
			return nil, nil
		}
		return &m, nil
	}
	var list []map[string]any
	if json.Unmarshal(res.Body, &list) != nil {
		return nil, nil
	}
	for _, m := range list {
		if v, _ := m[c.listKey].(string); v == a.PathParams[c.listKey] {
			return &m, nil
		}
	}
	return nil, nil
}

func (h *handlers) ApplyAiProposals(ctx context.Context, req ApplyAiProposalsRequestObject) (ApplyAiProposalsResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, invalid("body is required")
	}
	ids, err := proposalIDs(req.Body.Ids)
	if err != nil {
		return nil, err
	}
	acknowledge := req.Body.AcknowledgeLicense != nil && *req.Body.AcknowledgeLicense
	actor := PrincipalFrom(ctx).Actor()
	results := map[uuid.UUID]applyResultOut{}
	var rpzClaims []claimedProposal
	for _, id := range ids {
		p, finish, err := proposal.Claim(ctx, h.d.Store, id)
		if errors.Is(err, proposal.ErrNotOpen) {
			results[id] = applyResultOut{Id: id, Status: codeProposalNotOpen, Actions: []applyActionOut{{
				OperationId: "applyAiProposals", HttpStatus: http.StatusConflict, Code: codeProposalNotOpen, Message: proposal.ErrNotOpen.Error()}}}
			continue
		}
		if err != nil {
			h.releaseClaims(ctx, rpzClaims, actor.Name)
			return nil, err
		}
		claim := claimedProposal{p: p, finish: finish}
		if isRPZProposal(p) {
			rpzClaims = append(rpzClaims, claim)
			continue
		}
		status, actions := h.replayActions(ctx, p, acknowledge)
		results[id] = h.finishApply(ctx, actor, claim, status, actions)
	}
	if len(rpzClaims) > 0 {
		for _, r := range h.applyRPZ(ctx, actor, rpzClaims) {
			results[r.Id] = r
		}
	}
	out := ApplyAiProposals200JSONResponse{Results: make([]applyResultOut, 0, len(ids))}
	for _, id := range ids {
		out.Results = append(out.Results, results[id])
	}
	return out, nil
}

type claimedProposal struct {
	p      proposal.Proposal
	finish func(status string, result any, by string) error
}

func isRPZProposal(p proposal.Proposal) bool {
	return len(p.Actions) > 0 && p.Actions[0].OperationID == proposal.OpAppendAiRpzRules
}

// proposalIDs checks 1..100 ids and returns them sorted without duplicates.
func proposalIDs(in []openapi_types.UUID) ([]uuid.UUID, error) {
	if len(in) == 0 || len(in) > 100 {
		return nil, invalid("ids needs 1 to 100 entries")
	}
	ids := slices.Clone(in)
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	return slices.Compact(ids), nil
}

// withLicenseAcknowledged sets acknowledge_license in the body of the operations that accept it.
func withLicenseAcknowledged(operationID string, body json.RawMessage, acknowledge bool) json.RawMessage {
	if !acknowledge || !licenseOperations[operationID] {
		return body
	}
	var m map[string]any
	if json.Unmarshal(body, &m) != nil || m == nil {
		return body
	}
	m["acknowledge_license"] = true
	merged, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return merged
}

// replayActionResult replays one call and records its outcome.
func (h *handlers) replayActionResult(ctx context.Context, operationID string, c replayCall) (replayResult, applyActionOut) {
	res, err := h.replay(ctx, c)
	if err != nil {
		var aerr apiError
		if errors.As(err, &aerr) {
			res = replayResult{Status: aerr.status, Code: aerr.code, Message: aerr.msg}
		} else {
			slog.Error("replay", "operation", c.OperationID, "err", err)
			res = replayResult{Status: http.StatusInternalServerError, Code: "internal", Message: "internal error"}
		}
	}
	return res, applyActionOut{OperationId: operationID, HttpStatus: res.Status, Code: res.Code, Message: res.Message}
}

// applyStatus maps a replayed response to the proposal status: applied, stale (409 conflict), open (the
// caller must acknowledge a license first) or failed.
func applyStatus(res replayResult) string {
	switch {
	case res.Status >= 200 && res.Status < 300:
		return "applied"
	case res.Status == http.StatusConflict && res.Code == "conflict":
		return "stale"
	case res.Code == "license_acknowledgement_required":
		return "open"
	default:
		return "failed"
	}
}

// replayActions replays the actions in order and stops at the first that does not succeed.
func (h *handlers) replayActions(ctx context.Context, p proposal.Proposal, acknowledge bool) (string, []applyActionOut) {
	actions := []applyActionOut{}
	for _, a := range p.Actions {
		res, out := h.replayActionResult(ctx, a.OperationID, replayCall{OperationID: a.OperationID, PathParams: a.PathParams,
			Body: withLicenseAcknowledged(a.OperationID, a.Body, acknowledge)})
		actions = append(actions, out)
		if status := applyStatus(res); status != "applied" {
			return status, actions
		}
	}
	return "applied", actions
}

// finishApply stores the outcome of a claimed proposal and writes its applyAiProposals audit row.
func (h *handlers) finishApply(ctx context.Context, actor auth.Actor, c claimedProposal, status string, actions []applyActionOut) applyResultOut {
	id := c.p.ID
	if err := c.finish(status, map[string]any{"actions": actions}, actor.Name); err != nil {
		slog.Error("apply AI proposal: store outcome", "proposal", id, "status", status, "err", err)
	}
	audited := make([]map[string]any, len(actions))
	for i, a := range actions {
		audited[i] = map[string]any{"operation_id": a.OperationId, "http_status": a.HttpStatus}
	}
	actx := context.WithoutCancel(ctx) // the replays already happened: record them even if the client left
	err := h.d.Store.InTx(actx, func(tx pgx.Tx) error {
		return auth.WriteAudit(actx, tx, actor, auth.Change{Action: "applyAiProposals", TargetType: "ai_proposal", TargetID: id.String(),
			After: map[string]any{"proposal_id": id, "source": c.p.Source, "status": status, "actions": audited}}, nil)
	})
	if err != nil {
		slog.Error("apply AI proposal: audit", "proposal", id, "err", err)
	}
	return applyResultOut{Id: id, Status: status, Actions: actions}
}

// releaseClaims returns claimed proposals to open unchanged.
func (h *handlers) releaseClaims(ctx context.Context, claims []claimedProposal, by string) {
	for _, c := range claims {
		if err := c.finish("open", c.p.Result, by); err != nil {
			slog.Error("apply AI proposal: release", "proposal", c.p.ID, "err", err)
		}
	}
}

type rpzZoneRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Revision int64  `json:"revision"`
}

// applyRPZ compiles the rules of the claimed RPZ proposals and every rule applied before into one upload
// of the file zone ai-suggested.rpz, creating the zone when it is missing. Rules are recorded only after
// the upload succeeds; a failed step fails every proposal of the group.
func (h *handlers) applyRPZ(ctx context.Context, actor auth.Actor, claims []claimedProposal) []applyResultOut {
	var out []applyResultOut
	fail := func(status string, actions []applyActionOut) []applyResultOut {
		for _, c := range claims {
			out = append(out, h.finishApply(ctx, actor, c, status, actions))
		}
		return out
	}
	internal := func(err error) []applyResultOut {
		slog.Error("apply AI RPZ proposals", "err", err)
		return fail("failed", []applyActionOut{{OperationId: proposal.OpAppendAiRpzRules, HttpStatus: http.StatusInternalServerError, Code: "internal", Message: "internal error"}})
	}
	selected := map[uuid.UUID][]proposal.RPZRule{}
	merged := map[string]proposal.RPZRule{}
	applied, err := proposal.AppliedRules(ctx, h.d.Store.Pool)
	if err != nil {
		return internal(err)
	}
	for _, r := range applied {
		merged[r.Record] = r
	}
	for _, c := range claims {
		for _, a := range c.p.Actions {
			rules, err := proposal.DecodeRules(a)
			if err != nil {
				return internal(fmt.Errorf("proposal %s: %w", c.p.ID, err))
			}
			for _, r := range rules {
				r.ProposalID = c.p.ID
				merged[r.Record] = r
				selected[c.p.ID] = append(selected[c.p.ID], r)
			}
		}
	}
	all := make([]proposal.RPZRule, 0, len(merged))
	for _, r := range merged {
		all = append(all, r)
	}
	slices.SortFunc(all, func(a, b proposal.RPZRule) int { return strings.Compare(a.Record, b.Record) })

	actions := []applyActionOut{}
	res, act := h.replayActionResult(ctx, "listRpzZones", replayCall{OperationID: "listRpzZones"})
	if res.Status != http.StatusOK {
		return fail(applyStatus(res), append(actions, act))
	}
	var zones []rpzZoneRef
	if err := json.Unmarshal(res.Body, &zones); err != nil {
		return internal(err)
	}
	var zone *rpzZoneRef
	for i := range zones {
		if strings.TrimSuffix(zones[i].Name, ".") == proposal.RPZZoneName {
			zone = &zones[i]
		}
	}
	if zone == nil {
		body, _ := json.Marshal(map[string]any{"name": proposal.RPZZoneName, "source_type": "file", "min_refresh_seconds": 300, "policy_override": "given"})
		res, act = h.replayActionResult(ctx, "createRpzZone", replayCall{OperationID: "createRpzZone", Body: body})
		actions = append(actions, act)
		if applyStatus(res) != "applied" {
			return fail(applyStatus(res), actions)
		}
		zone = &rpzZoneRef{}
		if err := json.Unmarshal(res.Body, zone); err != nil {
			return internal(err)
		}
	}
	body, _ := json.Marshal(map[string]any{"content": proposal.RenderZone(all, time.Now()), "revision": zone.Revision})
	res, act = h.replayActionResult(ctx, "uploadRpzZoneFile", replayCall{OperationID: "uploadRpzZoneFile", PathParams: map[string]string{"id": zone.ID}, Body: body})
	actions = append(actions, act)
	if status := applyStatus(res); status != "applied" {
		return fail(status, actions)
	}
	for _, c := range claims {
		status := "applied"
		if err := proposal.RecordApplied(context.WithoutCancel(ctx), h.d.Store, selected[c.p.ID], c.p.ID, actor.Name); err != nil {
			slog.Error("apply AI RPZ proposal: record rules", "proposal", c.p.ID, "err", err)
			status = "failed"
		}
		out = append(out, h.finishApply(ctx, actor, c, status, actions))
	}
	return out
}

func (h *handlers) DismissAiProposals(ctx context.Context, req DismissAiProposalsRequestObject) (DismissAiProposalsResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, invalid("body is required")
	}
	ids, err := proposalIDs(req.Body.Ids)
	if err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(deref(req.Body.Reason))
	if len([]rune(reason)) > 500 {
		return nil, invalid("reason must be at most 500 characters")
	}
	actor := PrincipalFrom(ctx).Actor()
	var out DismissAiProposals200JSONResponse
	out.Results = make([]struct {
		Code   string             `json:"code"`
		Id     openapi_types.UUID `json:"id"`
		Status string             `json:"status"`
	}, 0, len(ids))
	for _, id := range ids {
		item := struct {
			Code   string             `json:"code"`
			Id     openapi_types.UUID `json:"id"`
			Status string             `json:"status"`
		}{Id: id, Status: "dismissed"}
		err := proposal.Dismiss(ctx, h.d.Store, id, actor.Name, reason)
		switch {
		case errors.Is(err, proposal.ErrNotOpen):
			item.Status, item.Code = codeProposalNotOpen, codeProposalNotOpen
		case err != nil:
			return nil, err
		default:
			if err := h.d.Store.InTx(context.WithoutCancel(ctx), func(tx pgx.Tx) error {
				return auth.WriteAudit(ctx, tx, actor, auth.Change{Action: "dismissAiProposals", TargetType: "ai_proposal", TargetID: id.String(),
					After: map[string]any{"proposal_id": id, "status": "dismissed", "reason": reason}}, nil)
			}); err != nil {
				return nil, err
			}
		}
		out.Results = append(out.Results, item)
	}
	return out, nil
}

// proposalOut converts a stored proposal to its API form (actions without current).
func proposalOut(p proposal.Proposal) AiProposal {
	out := AiProposal{
		Id: p.ID, Source: AiProposalSource(p.Source), Status: AiProposalStatus(p.Status), Title: p.Title, Description: p.Description,
		Priority: AiProposalPriority(p.Priority), Impact: jsonObject(p.Impact), Evidence: jsonObject(p.Evidence), Risk: jsonObject(p.Risk),
		SessionId: p.SessionID, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, ReviewedBy: p.ReviewedBy, ReviewedAt: p.ReviewedAt,
		DismissReason: p.DismissReason, Actions: make([]AiProposalAction, len(p.Actions)),
	}
	if len(p.Result) > 0 {
		r := jsonObject(p.Result)
		out.Result = &r
	}
	for i, a := range p.Actions {
		params := a.PathParams
		if params == nil {
			params = map[string]string{}
		}
		out.Actions[i] = AiProposalAction{OperationId: a.OperationID, PathParams: params, Explanation: a.Explanation}
		if len(a.Body) > 0 {
			b := jsonObject(a.Body)
			out.Actions[i].Body = &b
		}
	}
	return out
}

// jsonObject decodes a stored JSON object; null or a non-object gives an empty object.
func jsonObject(raw json.RawMessage) map[string]any {
	m := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m
}
