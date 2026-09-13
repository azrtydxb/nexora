package e2e

import (
	"net/http"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/piwi3910/nexora/e2e/harness"
)

func TestFleetAPI(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{})
	api := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)

	g := api.CreateEngineGroup(map[string]any{"name": "edge-a", "description": "first", "extra_acl_cidrs": []string{"198.51.100.0/24"}})
	if g.Revision != 1 || g.RolloutsPaused || g.RolloutStrategy != "all_at_once" || g.UpstreamMode != "inherit" {
		t.Fatalf("created engine group %+v", g)
	}
	stable := api.WaitEngineGroupStable(g.ID, 0, 15*time.Second)
	var groups []harness.EngineGroupView
	api.Must(http.MethodGet, "/engine-groups", nil, &groups, http.StatusOK)
	if len(groups) != 2 {
		t.Fatalf("engine groups = %+v, want default and edge-a", groups)
	}
	if code, c := api.ErrorCode(http.MethodPost, "/engine-groups", map[string]any{"name": "edge-a"}); code != http.StatusConflict || c != "name_taken" {
		t.Fatalf("duplicate name: %d %s", code, c)
	}
	if code, c := api.ErrorCode(http.MethodPost, "/engine-groups", map[string]any{"name": "bad", "rollout_strategy": "canary"}); code != http.StatusBadRequest || c != "invalid_rollout_params" {
		t.Fatalf("canary without size: %d %s", code, c)
	}
	upd := map[string]any{"name": "edge-a", "revision": g.Revision, "rollout_strategy": "canary", "canary_percent": 34, "health_window_seconds": 20}
	api.Must(http.MethodPut, "/engine-groups/"+g.ID, upd, nil, http.StatusOK)
	if code, c := api.ErrorCode(http.MethodPut, "/engine-groups/"+g.ID, upd); code != http.StatusConflict || c != "conflict" {
		t.Fatalf("stale revision: %d %s", code, c)
	}
	if code, c := api.ErrorCode(http.MethodDelete, "/engine-groups/"+harness.DefaultEngineGroupID+"?revision=1", nil); code != http.StatusConflict || c != "engine_group_protected" {
		t.Fatalf("delete default: %d %s", code, c)
	}
	g = api.EngineGroup(g.ID)
	api.Must(http.MethodPut, "/engine-groups/"+g.ID, map[string]any{"name": "edge-a", "revision": g.Revision, "rollout_strategy": "all_at_once"}, nil, http.StatusOK)

	api.Must(http.MethodPost, "/rewrites", map[string]any{"name": "scoped.fleet.test", "type": "A", "value": "192.0.2.7", "engine_group_id": g.ID}, nil, http.StatusCreated)
	g = api.EngineGroup(g.ID)
	if code, c := api.ErrorCode(http.MethodDelete, "/engine-groups/"+g.ID+"?revision="+itoa(g.Revision), nil); code != http.StatusConflict || c != "engine_group_not_empty" {
		t.Fatalf("delete non-empty engine group: %d %s", code, c)
	}
	if code, c := api.ErrorCode(http.MethodPost, "/upstreams", map[string]any{"name": "nowhere", "protocol": "udp", "address": "192.0.2.53:53",
		"timeout_ms": 250, "enabled": true, "position": 0, "engine_group_id": "11111111-1111-1111-1111-111111111111"}); code != http.StatusUnprocessableEntity || c != "engine_group_not_found" {
		t.Fatalf("unknown engine group: %d %s", code, c)
	}
	var pg1 struct {
		ID string `json:"id"`
	}
	api.Must(http.MethodPost, "/policy-groups", map[string]any{"name": "fleet-policy", "cidrs": []string{"10.9.0.0/16"}}, &pg1, http.StatusCreated)
	if code, c := api.ErrorCode(http.MethodPost, "/rewrites", map[string]any{"group_id": pg1.ID, "engine_group_id": g.ID, "name": "p.fleet.test", "type": "A", "value": "192.0.2.8"}); code != http.StatusUnprocessableEntity || c != "engine_group_scope" {
		t.Fatalf("policy group rewrite with its own engine group: %d %s", code, c)
	}
	after := api.WaitEngineGroupStable(g.ID, stable, 15*time.Second)

	var created struct {
		Token     string `json:"token"`
		JoinToken struct {
			ID string `json:"id"`
		} `json:"join_token"`
	}
	api.Must(http.MethodPost, "/join-tokens", map[string]any{"name": "edge", "ttl_seconds": 600, "engine_group_id": g.ID, "max_uses": 2,
		"labels": map[string]string{"site": "lab"}}, &created, http.StatusCreated)
	if !regexp.MustCompile(`^nxj1\.[A-Z2-7]+\.[0-9a-f]{64}$`).MatchString(created.Token) {
		t.Fatalf("join token format %q", created.Token)
	}
	var listed []map[string]any
	api.Must(http.MethodGet, "/join-tokens", nil, &listed, http.StatusOK)
	if len(listed) != 1 || listed[0]["state"] != "active" || listed[0]["engine_group_name"] != "edge-a" || listed[0]["max_uses"] != float64(2) {
		t.Fatalf("listed join tokens %+v", listed)
	}
	if _, has := listed[0]["token"]; has {
		t.Fatal("listJoinTokens must never return the token")
	}
	api.Must(http.MethodDelete, "/join-tokens/"+created.JoinToken.ID, nil, nil, http.StatusNoContent)
	api.Must(http.MethodGet, "/join-tokens", nil, &listed, http.StatusOK)
	if listed[0]["state"] != "revoked" {
		t.Fatalf("revoked token listing %+v", listed)
	}

	if code, c := api.ErrorCode(http.MethodPost, "/engine-groups/"+g.ID+"/rollback", map[string]any{"to_version": after + 1000}); code != http.StatusNotFound || c != "version_not_found" {
		t.Fatalf("rollback to an unknown version: %d %s", code, c)
	}
	if code, c := api.ErrorCode(http.MethodPost, "/engine-groups/"+g.ID+"/rollback", map[string]any{"to_version": api.LatestVersion()}); code != http.StatusBadRequest || c != "not_older" {
		t.Fatalf("rollback to the newest version: %d %s", code, c)
	}
	var rb harness.RolloutView
	api.Must(http.MethodPost, "/engine-groups/"+g.ID+"/rollback", map[string]any{"to_version": stable}, &rb, http.StatusAccepted)
	if rb.Kind != "rollback" || rb.Version <= after || rb.FromVersion == nil || *rb.FromVersion != stable {
		t.Fatalf("rollback rollout %+v", rb)
	}
	api.WaitRollout(g.ID, rb.Version, 15*time.Second, "completed")
	if !api.EngineGroup(g.ID).RolloutsPaused {
		t.Fatal("rollback must pause change rollouts")
	}
	var resumed harness.RolloutView
	api.Must(http.MethodPost, "/engine-groups/"+g.ID+"/resume-rollouts", nil, &resumed, http.StatusAccepted)
	if resumed.Kind != "change" || resumed.Version <= rb.Version {
		t.Fatalf("resume rollout %+v", resumed)
	}
	if code, c := api.ErrorCode(http.MethodPost, "/engine-groups/"+g.ID+"/resume-rollouts", nil); code != http.StatusConflict || c != "not_paused" {
		t.Fatalf("resume twice: %d %s", code, c)
	}

	var sum struct {
		EnginesTotal int `json:"engines_total"`
		EngineGroups []struct {
			Name string `json:"name"`
		} `json:"engine_groups"`
	}
	api.Must(http.MethodGet, "/fleet/summary", nil, &sum, http.StatusOK)
	if sum.EnginesTotal != 0 || len(sum.EngineGroups) != 2 {
		t.Fatalf("summary %+v", sum)
	}
	var detail map[string]any
	api.Must(http.MethodGet, "/rollouts/"+rb.ID, nil, &detail, http.StatusOK)
	if detail["state"] != "completed" || detail["engines"] == nil {
		t.Fatalf("rollout detail %+v", detail)
	}
	if code, _ := api.ErrorCode(http.MethodPatch, "/engines/"+harness.DefaultEngineGroupID, map[string]any{"revision": 1}); code != http.StatusNotFound {
		t.Fatalf("patch unknown engine: %d", code)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
