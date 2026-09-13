package harness

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// DefaultEngineGroupID is the engine group that always exists.
const DefaultEngineGroupID = "00000000-0000-0000-0000-000000000001"

// EngineGroupView is the part of the API's EngineGroup the tests inspect.
type EngineGroupView struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Description     string  `json:"description"`
	UpstreamMode    string  `json:"upstream_mode"`
	RolloutStrategy string  `json:"rollout_strategy"`
	CanaryCount     int     `json:"canary_count"`
	CanaryPercent   int     `json:"canary_percent"`
	EngineCount     int     `json:"engine_count"`
	RolloutsPaused  bool    `json:"rollouts_paused"`
	StableVersion   *uint64 `json:"stable_version"`
	Revision        int64   `json:"revision"`
}

// RolloutView is the part of the API's Rollout the tests inspect.
type RolloutView struct {
	ID              string   `json:"id"`
	EngineGroupID   string   `json:"engine_group_id"`
	Kind            string   `json:"kind"`
	Strategy        string   `json:"strategy"`
	State           string   `json:"state"`
	HaltReason      string   `json:"halt_reason"`
	Version         uint64   `json:"version"`
	FromVersion     *uint64  `json:"from_version"`
	CanaryEngineIDs []string `json:"canary_engine_ids"`
}

// CreateEngineGroup posts body to /engine-groups and expects 201.
func (a *API) CreateEngineGroup(body map[string]any) EngineGroupView {
	a.T.Helper()
	var g EngineGroupView
	a.Must(http.MethodPost, "/engine-groups", body, &g, http.StatusCreated)
	return g
}

// EngineGroup returns one engine group.
func (a *API) EngineGroup(id string) EngineGroupView {
	a.T.Helper()
	var g EngineGroupView
	a.Must(http.MethodGet, "/engine-groups/"+id, nil, &g, http.StatusOK)
	return g
}

// WaitEngineGroupStable waits until the group's stable version is above after and returns it.
func (a *API) WaitEngineGroupStable(id string, after uint64, timeout time.Duration) uint64 {
	a.T.Helper()
	var v uint64
	Eventually(a.T, timeout, func() error {
		g := a.EngineGroup(id)
		if g.StableVersion == nil || *g.StableVersion <= after {
			return fmt.Errorf("engine group %s stable %v, waiting for > %d", g.Name, g.StableVersion, after)
		}
		v = *g.StableVersion
		return nil
	})
	return v
}

// WaitRollout waits for the group's newest rollout with version >= minVersion to reach one of states.
func (a *API) WaitRollout(engineGroupID string, minVersion uint64, timeout time.Duration, states ...string) RolloutView {
	a.T.Helper()
	var got RolloutView
	Eventually(a.T, timeout, func() error {
		var rs []RolloutView
		if _, err := a.Do(http.MethodGet, "/rollouts?limit=1&engine_group_id="+engineGroupID, nil, &rs); err != nil {
			return err
		}
		if len(rs) == 0 || rs[0].Version < minVersion {
			return fmt.Errorf("no rollout >= %d yet", minVersion)
		}
		for _, s := range states {
			if rs[0].State == s {
				got = rs[0]
				return nil
			}
		}
		return fmt.Errorf("rollout %d is %s (%s), waiting for %v", rs[0].Version, rs[0].State, rs[0].HaltReason, states)
	})
	return got
}

// CreateJoinTokenFor creates a one-hour, unlimited join token for an engine group.
func (a *API) CreateJoinTokenFor(engineGroupID string, labels map[string]string) string {
	a.T.Helper()
	var created struct {
		Token string `json:"token"`
	}
	body := map[string]any{"name": UniqueName("e2e"), "ttl_seconds": 3600, "engine_group_id": engineGroupID}
	if len(labels) > 0 {
		body["labels"] = labels
	}
	a.Must(http.MethodPost, "/join-tokens", body, &created, http.StatusCreated)
	return created.Token
}

// EngineByNode returns the listed engine with nodeName.
func (a *API) EngineByNode(nodeName string) EngineView {
	a.T.Helper()
	var engines []EngineView
	a.Must(http.MethodGet, "/engines", nil, &engines, http.StatusOK)
	for _, e := range engines {
		if e.NodeName == nodeName {
			return e
		}
	}
	a.T.Fatalf("engine %s not listed", nodeName)
	return EngineView{}
}

// PatchEngine sends fields with the engine's current revision and expects 200.
func (a *API) PatchEngine(nodeName string, fields map[string]any) EngineView {
	a.T.Helper()
	e := a.EngineByNode(nodeName)
	body := map[string]any{"revision": e.Revision}
	for k, v := range fields {
		body[k] = v
	}
	var out EngineView
	a.Must(http.MethodPatch, "/engines/"+e.ID, body, &out, http.StatusOK)
	return out
}

// ErrorCode sends a request that must fail and returns its status and the error body's code.
func (a *API) ErrorCode(method, path string, body any) (int, string) {
	a.T.Helper()
	status, err := a.Do(method, path, body, nil)
	if err == nil {
		a.T.Fatalf("%s %s succeeded with %d, want an error", method, path, status)
	}
	msg := err.Error()
	var e struct {
		Code string `json:"code"`
	}
	if i := strings.Index(msg, "{"); i >= 0 {
		_ = json.Unmarshal([]byte(msg[i:]), &e)
	}
	return status, e.Code
}

// RestartEngine stops en and starts it again on the same engine.toml and state directory, then
// waits for its control stream (it must not enroll again).
func (e *Env) RestartEngine(en *Engine) {
	e.T.Helper()
	en.Proc.Stop()
	en.Proc = e.Start("nexora-engine", []string{"--config", en.ConfigPath}, en.env)
	en.readAddrs()
	en.Proc.WaitLog(controlConnected, 30*time.Second)
}

// Metric scrapes the management instance's /metrics like (*Engine).Metric.
func (m *Mgmt) Metric(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()
	return scrapeMetric(t, m.BaseURL+"/metrics", name, labels)
}
