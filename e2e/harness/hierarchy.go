package harness

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/piwi3910/nexora/e2e/fixtures/authhier"
)

// Hierarchy is a running `nexora-fixture authhier`: the private signed DNS hierarchy on
// 127.0.53.0/24 sharing Ready.Port.
type Hierarchy struct {
	Ready authhier.Ready
}

// StartHierarchy starts `nexora-fixture authhier` and reads the Ready JSON it writes once every
// server is bound.
func (e *Env) StartHierarchy() *Hierarchy {
	e.T.Helper()
	e.mu.Lock()
	n := len(e.procs)
	e.mu.Unlock()
	readyFile := filepath.Join(e.Dir, fmt.Sprintf("authhier-%d.json", n))
	p := e.Start("nexora-fixture", []string{"authhier", "--ready-file", readyFile}, nil)
	p.WaitReady(20 * time.Second)
	data, err := os.ReadFile(readyFile)
	if err != nil {
		e.T.Fatal(err)
	}
	h := &Hierarchy{}
	if err := json.Unmarshal(data, &h.Ready); err != nil {
		e.T.Fatalf("authhier ready file %s: %v", readyFile, err)
	}
	return h
}

// Stats returns the hierarchy's per-server query counts and spoofed replies sent.
func (h *Hierarchy) Stats(t *testing.T) authhier.Stats {
	t.Helper()
	var s authhier.Stats
	fixtureCall(t, http.MethodGet, h.Ready.StatsURL+"/stats", nil, &s)
	return s
}

// ConfigureRecursion switches the management plane to recursive resolution over the hierarchy
// (its root hints and port), adds the hierarchy's root DS as a trust anchor and waits until the
// engine nodeName applied the resulting config version.
func (h *Hierarchy) ConfigureRecursion(t *testing.T, api *API, nodeName string) {
	t.Helper()
	var res map[string]any
	api.Must("GET", "/resolution", nil, &res, http.StatusOK)
	res["mode"] = "recursive"
	res["qname_minimisation"] = true
	res["aggressive_nsec"] = false
	res["authority_port"] = h.Ready.Port
	res["root_hints"] = h.Ready.RootHints
	api.Must("PUT", "/resolution", res, nil, http.StatusOK)
	api.Must("POST", "/dnssec/trust-anchors", map[string]string{"zone": ".", "ds": h.Ready.RootDS}, nil, http.StatusCreated)
	v := api.LatestVersion()
	api.WaitEngine(nodeName, 15*time.Second, func(e EngineView) bool { return e.AppliedVersion >= v })
}
