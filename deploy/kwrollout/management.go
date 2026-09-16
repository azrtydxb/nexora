package kwrollout

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// EngineBinding pins the persistent identity discovered for a running workload.
// The runtime adapter must establish this binding independently of a mutable
// node-name lookup; otherwise a stale engine record could satisfy the gate.
type EngineBinding struct {
	ID, NodeName, GroupID string
}

type managedEngine struct {
	ID             string     `json:"id"`
	NodeName       string     `json:"node_name"`
	GroupID        string     `json:"engine_group_id"`
	Connected      bool       `json:"connected"`
	Status         string     `json:"status"`
	AppliedVersion int64      `json:"applied_version"`
	TargetVersion  int64      `json:"target_version"`
	LastSeenAt     *time.Time `json:"last_seen_at"`
	RevokedAt      *time.Time `json:"revoked_at"`
	PersistError   string     `json:"persist_error"`
	RejectedReason string     `json:"rejected_reason"`
	VersionAhead   bool       `json:"version_ahead"`
}

// CheckManagement checks the entire expected connected fleet without polling or
// extending acceptance deadlines. client supplies authenticated cookies and TLS
// trust; redirects are refused. Config changes during observation fail the gate.
// Kubernetes readiness, image/template identity and DNS are separate checks.
func CheckManagement(ctx context.Context, client *http.Client, baseURL string, expected []EngineBinding) error {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("management probe requires an HTTPS origin without credentials or query")
	}
	if client == nil || len(expected) == 0 {
		return fmt.Errorf("management probe requires an authenticated client and expected identities")
	}
	bindings, names := map[string]EngineBinding{}, map[string]bool{}
	for _, b := range expected {
		if b.ID == "" || b.NodeName == "" || b.GroupID == "" || bindings[b.ID].ID != "" || names[b.NodeName] {
			return fmt.Errorf("empty or duplicate expected engine identity/node name")
		}
		bindings[b.ID], names[b.NodeName] = b, true
	}
	// Copy instead of modifying a caller's shared client.
	httpClient := *client
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	base := strings.TrimSuffix(baseURL, "/") + "/api/v1"
	latest := func() (int64, error) {
		var versions []struct {
			Version int64 `json:"version"`
		}
		if err := managementJSON(ctx, &httpClient, base+"/config-versions?limit=1", &versions); err != nil {
			return 0, err
		}
		if len(versions) != 1 || versions[0].Version <= 0 {
			return 0, fmt.Errorf("management API did not return one current configuration version")
		}
		return versions[0].Version, nil
	}
	version, err := latest()
	if err != nil {
		return err
	}
	var engines []managedEngine
	if err := managementJSON(ctx, &httpClient, base+"/engines", &engines); err != nil {
		return err
	}
	seen := map[string]bool{}
	now := time.Now()
	for _, e := range engines {
		if e.ID == "" || seen[e.ID] {
			return fmt.Errorf("management API returned an empty or duplicate engine identity")
		}
		seen[e.ID] = true
		b, wanted := bindings[e.ID]
		if !wanted {
			if e.Connected {
				return fmt.Errorf("unexpected connected engine %s", e.ID)
			}
			continue
		}
		if e.NodeName != b.NodeName || e.GroupID != b.GroupID || !e.Connected || e.RevokedAt != nil || e.Status != "current" || e.PersistError != "" || e.RejectedReason != "" || e.VersionAhead {
			return fmt.Errorf("engine %s identity, connection or status is unhealthy", e.ID)
		}
		if e.LastSeenAt == nil || now.Sub(*e.LastSeenAt) > 30*time.Second || e.LastSeenAt.Sub(now) > 5*time.Second {
			return fmt.Errorf("engine %s has stale or implausible last-seen time", e.ID)
		}
		if e.AppliedVersion != version || e.TargetVersion != version {
			return fmt.Errorf("engine %s configuration mismatch: applied=%d target=%d expected=%d", e.ID, e.AppliedVersion, e.TargetVersion, version)
		}
	}
	for id := range bindings {
		if !seen[id] {
			return fmt.Errorf("expected engine %s is missing from management", id)
		}
	}
	after, err := latest()
	if err != nil {
		return err
	}
	if after != version {
		return fmt.Errorf("configuration changed during health observation: %d to %d", version, after)
	}
	return ctx.Err()
}

func managementJSON(ctx context.Context, client *http.Client, endpoint string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("management health request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		// Do not echo response bodies, cookies or credentials into rollout logs.
		return fmt.Errorf("management health HTTP status %d", response.StatusCode)
	}
	const maxBody = 2 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxBody {
		return fmt.Errorf("management health response exceeds size limit")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("invalid management health JSON: %w", err)
	}
	return nil
}
