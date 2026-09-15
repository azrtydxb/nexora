package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/piwi3910/nexora/mgmt/internal/ai/proposal"
	"github.com/piwi3910/nexora/mgmt/internal/api"
	"github.com/piwi3910/nexora/mgmt/internal/catalog"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
)

func upsertProposal(t *testing.T, e *apiEnv, source string, actions ...proposal.Action) string {
	t.Helper()
	id, created, err := proposal.Upsert(e.ctx, e.st, proposal.Draft{Source: source, Title: "t", Description: "d", Priority: "low", Actions: actions})
	if err != nil || !created {
		t.Fatalf("upsert: %v %v", created, err)
	}
	return id.String()
}

func groupAction(g group, description string) proposal.Action {
	body, _ := json.Marshal(map[string]any{"name": g.Name, "cidrs": g.CIDRs, "description": description, "revision": g.Revision})
	return proposal.Action{OperationID: "updatePolicyGroup", PathParams: map[string]string{"id": g.ID}, Body: body}
}

func rpzRuleAction(record string) proposal.Action {
	body := fmt.Sprintf(`{"rules":[{"record":%q,"policy":"nxdomain","category":"c2","reason":"beaconing","confidence":0.9}]}`, record)
	return proposal.Action{OperationID: proposal.OpAppendAiRpzRules, PathParams: map[string]string{}, Body: json.RawMessage(body)}
}

func (e *apiEnv) auditCount(t *testing.T, action, where string, args ...any) int {
	t.Helper()
	var n int
	if err := e.st.Pool.QueryRow(e.ctx, "select count(*) from audit_log where action = '"+action+"'"+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func applyIDs(t *testing.T, c *client, ack bool, ids ...string) api.AiApplyResponse {
	t.Helper()
	var out api.AiApplyResponse
	if code := c.do(http.MethodPost, "/ai/proposals/apply", map[string]any{"ids": ids, "acknowledge_license": ack}, &out); code != http.StatusOK {
		t.Fatalf("apply %v -> %d", ids, code)
	}
	return out
}

// TestApplyAiProposalsHandler catches an apply that bypasses RBAC, changes configuration without the
// normal audited API call, applies a proposal twice, hides a stale revision, uploads RPZ rules per
// proposal instead of once, or drops the operator's license acknowledgement.
func TestApplyAiProposalsHandler(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	op, viewer, e := roleClientsWith(t, func(d *api.Deps) {
		d.Catalog = cat
		d.AI = &api.AIRuntime{Proposals: &proposal.Validator{Store: d.Store}}
	})
	if _, err := catalog.Sync(e.ctx, e.st, snapshot.BuildConfig{}, cat, catalog.Raw()); err != nil {
		t.Fatal(err)
	}
	var g group
	if code := op.do(http.MethodPost, "/policy-groups", map[string]any{"name": "guests", "cidrs": []string{"10.9.0.0/16"}}, &g); code != http.StatusCreated {
		t.Fatalf("create group -> %d", code)
	}

	applyID := upsertProposal(t, e, "filter_recommendations", groupAction(g, "suggested by AI"))
	var denied apiErr
	if code := viewer.do(http.MethodPost, "/ai/proposals/apply", map[string]any{"ids": []string{applyID}}, &denied); code != http.StatusForbidden || denied.Code != "forbidden" {
		t.Fatalf("viewer apply -> %d %+v", code, denied)
	}

	var got api.AiProposal
	if code := viewer.do(http.MethodGet, "/ai/proposals/"+applyID, nil, &got); code != http.StatusOK || len(got.Actions) != 1 || got.Actions[0].Current == nil {
		t.Fatalf("get proposal -> %d %+v", code, got)
	}
	if cur := *got.Actions[0].Current; cur["id"] != g.ID || cur["name"] != "guests" || cur["revision"] != float64(g.Revision) {
		t.Fatalf("current is not the live group: %v", cur)
	}

	res := applyIDs(t, op, true, applyID)
	if len(res.Results) != 1 || res.Results[0].Status != "applied" || len(res.Results[0].Actions) != 1 || res.Results[0].Actions[0].HttpStatus != http.StatusOK {
		t.Fatalf("apply result %+v", res)
	}
	var changed group
	op.do(http.MethodGet, "/policy-groups/"+g.ID, nil, &changed)
	if changed.Revision != g.Revision+1 {
		t.Fatalf("group not changed: %+v", changed)
	}
	if e.auditCount(t, "updatePolicyGroup", " and actor_name = 'opal' and target_id = $1", g.ID) != 1 ||
		e.auditCount(t, "applyAiProposals", " and actor_name = 'opal' and target_type = 'ai_proposal' and target_id = $1", applyID) != 1 {
		t.Fatal("missing updatePolicyGroup or applyAiProposals audit row")
	}
	var list []api.AiProposal
	if code := viewer.do(http.MethodGet, "/ai/proposals?status=applied", nil, &list); code != http.StatusOK || len(list) != 1 || list[0].ReviewedBy != "opal" {
		t.Fatalf("list applied -> %d %+v", code, list)
	}

	res = applyIDs(t, op, false, applyID)
	if r := res.Results[0]; r.Status != "proposal_not_open" || len(r.Actions) != 1 || r.Actions[0].Code != "proposal_not_open" {
		t.Fatalf("second apply %+v", res)
	}

	staleID := upsertProposal(t, e, "config_assistant", groupAction(g, "stale suggestion")) // g.Revision is now behind
	res = applyIDs(t, op, false, staleID)
	if r := res.Results[0]; r.Status != "stale" || r.Actions[0].HttpStatus != http.StatusConflict {
		t.Fatalf("stale apply %+v", res)
	}

	first, second := upsertProposal(t, e, "rpz_suggestions", rpzRuleAction("c2.evil.example")), upsertProposal(t, e, "rpz_suggestions", rpzRuleAction("*.bad.example"))
	res = applyIDs(t, op, false, first, second)
	for _, r := range res.Results {
		if r.Status != "applied" {
			t.Fatalf("rpz apply %+v", res)
		}
	}
	if e.auditCount(t, "createRpzZone", "") != 1 || e.auditCount(t, "uploadRpzZoneFile", "") != 1 {
		t.Fatal("RPZ proposals are not one zone creation and one upload")
	}
	applied, err := proposal.AppliedRules(e.ctx, e.st.Pool)
	if err != nil || len(applied) != 2 {
		t.Fatalf("ai_rpz_rules %+v %v", applied, err)
	}
	third := upsertProposal(t, e, "rpz_suggestions", rpzRuleAction("x.evil.example"))
	if res = applyIDs(t, op, false, third); res.Results[0].Status != "applied" || e.auditCount(t, "createRpzZone", "") != 1 || e.auditCount(t, "uploadRpzZoneFile", "") != 2 {
		t.Fatalf("second RPZ apply %+v", res)
	}
	if applied, _ = proposal.AppliedRules(e.ctx, e.st.Pool); len(applied) != 3 {
		t.Fatalf("rules not accumulated: %+v", applied)
	}

	ads := findCategory(t, op, "ads-tracking")
	licenseID := upsertProposal(t, e, "filter_recommendations", proposal.Action{OperationID: "updateFilterCategory",
		PathParams: map[string]string{"key": "ads-tracking"}, Body: json.RawMessage(fmt.Sprintf(`{"enabled":true,"revision":%d}`, ads.Revision))})
	res = applyIDs(t, op, false, licenseID)
	if r := res.Results[0]; r.Status != "open" || r.Actions[0].Code != "license_acknowledgement_required" {
		t.Fatalf("apply without acknowledgement %+v", res)
	}
	if res = applyIDs(t, op, true, licenseID); res.Results[0].Status != "applied" || !findCategory(t, op, "ads-tracking").Enabled {
		t.Fatalf("apply with acknowledgement %+v", res)
	}

	dismissID := upsertProposal(t, e, "capacity_forecast", groupAction(changed, "dismiss me"))
	var dismissed api.AiDismissResponse
	if code := op.do(http.MethodPost, "/ai/proposals/dismiss", map[string]any{"ids": []string{dismissID, uuid.NewString()}, "reason": "no"}, &dismissed); code != http.StatusOK ||
		len(dismissed.Results) != 2 {
		t.Fatalf("dismiss -> %d %+v", code, dismissed)
	}
	for _, r := range dismissed.Results {
		if want := r.Id.String() == dismissID; want != (r.Status == "dismissed" && r.Code == "") {
			t.Fatalf("dismiss results %+v", dismissed)
		}
	}
	if e.auditCount(t, "dismissAiProposals", " and target_id = $1", dismissID) != 1 {
		t.Fatal("missing dismissAiProposals audit row")
	}
}
