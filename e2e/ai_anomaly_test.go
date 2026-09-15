package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

// TestAIQueryLogAnomalies catches a tunnelling detector that misses real engine traffic (a threshold or a
// query-log field off), an agent that never reaches the model, and a model explanation that is not stored.
func TestAIQueryLogAnomalies(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	fx := env.StartOpenAIFixture()
	dnsFx := env.StartDNSFixture()
	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{ExtraEnv: harness.AIEnv(fx)})
	admin := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
	admin.DisableForwardedValidation()
	admin.Must(http.MethodPost, "/upstreams", map[string]any{"name": "fixture", "protocol": "udp", "address": dnsFx.UDP, "timeout_ms": 250, "enabled": true, "position": 0}, nil, http.StatusCreated)
	eng := env.StartManagedEngine("ai-anomaly-1", []string{mg.GRPCURL}, admin.CreateJoinToken())
	waitLatestApplied(t, admin, "ai-anomaly-1")

	c := &dns.Client{Timeout: time.Second}
	for i := 0; i < 80; i++ {
		sum := sha256.Sum256(fmt.Appendf(nil, "aia-%d", i))
		name := hex.EncodeToString(sum[:])[:32] + ".tunnel.aia.test."
		if _, _, err := c.Exchange(question(name, dns.TypeTXT), eng.DNS); err != nil {
			t.Fatalf("query %d: %v", i, err)
		}
	}

	fx.ScriptJSON(t, "querylog_anomalies", map[string]any{"anomalies": []any{map[string]any{
		"candidate_id": "dns_tunneling:127.0.0.1", "severity": "critical", "confidence": 0.9, "title": "Tunnelling from 127.0.0.1",
		"description": "scripted", "recommended_actions": []string{"Block tunnel.aia.test"},
	}}})

	harness.EventuallyTrue(t, 30*time.Second, func() bool {
		var page struct {
			Records []struct {
				Client string `json:"client"`
				QType  string `json:"qtype"`
			} `json:"records"`
		}
		admin.Must(http.MethodGet, "/query-log?limit=100&name=tunnel.aia.test", nil, &page, http.StatusOK)
		n := 0
		for _, r := range page.Records {
			if r.Client == "127.0.0.1" {
				n++
			}
		}
		return n == 80
	}, "the 80 tunnelling queries reach the query log")

	admin.Must(http.MethodPost, "/ai/agents/querylog_anomalies/run", nil, nil, http.StatusAccepted)
	type findingView struct {
		CandidateID string `json:"candidate_id"`
		Type        string `json:"type"`
		Severity    string `json:"severity"`
		Description string `json:"description"`
		Explained   bool   `json:"explained"`
	}
	var got findingView
	harness.EventuallyTrue(t, 60*time.Second, func() bool {
		var list []findingView
		admin.Must(http.MethodGet, "/ai/findings?kind=anomaly", nil, &list, http.StatusOK)
		for _, f := range list {
			if f.CandidateID == "dns_tunneling:127.0.0.1" {
				got = f
			}
		}
		return got.Description == "scripted"
	}, "the dns_tunneling finding for 127.0.0.1 carries the scripted explanation")
	if got.Type != "dns_tunneling" || got.Severity != "critical" || !got.Explained {
		t.Fatalf("finding = %+v", got)
	}
	reqs := fx.Requests(t)
	if len(reqs) != 1 || reqs[0].Feature != "querylog_anomalies" {
		t.Fatalf("fixture requests = %+v, want one querylog_anomalies call", reqs)
	}
	if strings.Contains(fmt.Sprint(reqs), "test-key-not-secret") {
		t.Fatal("fixture recorded the API key")
	}
}
