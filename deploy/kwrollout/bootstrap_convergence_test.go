package kwrollout

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Execute the actual bootstrap script against a local TLS API and the real Unix
// ownership guard. Only kubectl is faked; no cluster or DNS probes are executed.
func TestBootstrapConvergenceShell(t *testing.T) {
	for _, scenario := range []string{"lag", "ready", "timeout", "rejected", "persist", "stale", "future", "disconnected", "identity", "missing", "duplicate", "ahead", "target-moved", "http-failure", "ownership", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			root, err := filepath.Abs("../..")
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			var reads, versions, writes, guards atomic.Int32
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			guard, err := newMutationGuard(ctx, func(context.Context) error {
				guards.Add(1)
				if scenario == "ownership" && reads.Load() >= 2 {
					return fmt.Errorf("lost ownership")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer guard.close()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					writes.Add(1)
				}
				respond := func(v any) { _ = json.NewEncoder(w).Encode(v) }
				switch r.URL.Path {
				case "/api/v1/health", "/api/v1/auth/login":
					respond(map[string]any{})
				case "/api/v1/setup":
					respond(map[string]any{"required": false})
				case "/api/v1/config-versions":
					n := versions.Add(1)
					version := 1234
					if scenario == "target-moved" && n > 1 {
						version++
					}
					respond([]map[string]int{{"version": version}})
				case "/api/v1/engines":
					n := reads.Add(1)
					if scenario == "http-failure" && n > 1 {
						w.WriteHeader(503)
						return
					}
					engines, _ := healthyManagementFixture()
					for i := range engines {
						engines[i].AppliedVersion = 1234
						engines[i].TargetVersion = 1234
					}
					if n > 1 {
						switch scenario {
						case "lag", "timeout":
							if n == 2 || scenario == "timeout" {
								engines[0].AppliedVersion = 1233
								engines[0].TargetVersion = 1233
							}
						case "rejected":
							engines[0].Status = "rejected"
							engines[0].RejectedReason = "invalid configuration"
						case "persist":
							engines[0].PersistError = "disk full"
						case "stale":
							old := time.Now().Add(-time.Minute)
							engines[0].LastSeenAt = &old
						case "future":
							future := time.Now().Add(time.Minute)
							engines[0].LastSeenAt = &future
						case "disconnected":
							engines[0].Connected = false
						case "identity":
							engines[0].ID = "replacement"
						case "missing":
							engines = engines[1:]
						case "duplicate":
							engines = append(engines, engines[0])
						case "ahead":
							engines[0].TargetVersion++
						case "cancel":
							cancel()
						}
					}
					respond(engines)
				case "/api/v1/upstreams":
					respond([]map[string]string{{"name": "existing"}})
				case "/api/v1/filter-lists":
					respond([]map[string]string{{"name": "kw-smoke", "id": "smoke"}})
				case "/api/v1/filter-lists/smoke/refresh":
					respond(map[string]any{"entry_count": 1, "last_error": ""})
				case "/api/v1/resolution":
					respond(map[string]string{"mode": "recursive"})
				case "/api/v1/dnssec/settings":
					respond(map[string]bool{"validation": true, "validate_forwarded": true})
				case "/api/v1/rpz-zones":
					respond([]map[string]string{{"name": "rpz.kw.nexora.", "file_records": "existing"}})
				case "/api/v1/tsig-keys":
					respond([]map[string]string{{"name": "nexora-demo-xfr.", "id": "key"}})
				case "/api/v1/zones":
					respond([]map[string]string{{"name": "nexora-demo.kw.", "id": "demo"}, {"name": "bind-demo.kw.", "id": "secondary"}})
				case "/api/v1/zones/demo/records":
					data := map[string]string{"ns1.nexora-demo.kw.A": "192.168.10.136", "www.nexora-demo.kw.A": "192.0.2.80", "www.nexora-demo.kw.AAAA": "2001:db8::80", "mail.nexora-demo.kw.A": "192.0.2.25", "nexora-demo.kw.MX": "10 mail.nexora-demo.kw.", "nexora-demo.kw.TXT": "\"nexora authoritative demo\""}
					respond(map[string]any{"items": []map[string]string{{"data": data[r.URL.Query().Get("name")+r.URL.Query().Get("type")]}}})
				case "/api/v1/zones/demo/dnssec":
					respond(map[string]bool{"enabled": true})
				case "/api/v1/engine-groups":
					respond([]any{})
				default:
					t.Errorf("unexpected API operation %s %s", r.Method, r.URL)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			ca := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
			fake := `#!/usr/bin/env bash
set -eu
case "$*" in
*"get secret nexora-ingress-tls"*) printf '%s' '` + ca + `' ;;
*"get secret nexora-admin -o jsonpath={.data.username}"*) printf admin | base64 ;;
*"get secret nexora-admin -o jsonpath={.data.password}"*) printf fixture | base64 ;;
*"get secret nexora-demo-tsig"*) printf nexora-demo-xfr. | base64 ;;
*"get secret nexora-admin"*|*"get secret nexora-join-token"*) ;;
*"delete secret nexora-join-token-edge-b --ignore-not-found"*) ;;
*) echo "unexpected kubectl call" >&2; exit 1 ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(fake), 0700); err != nil {
				t.Fatal(err)
			}
			commandCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
			defer stop()
			cmd := exec.CommandContext(commandCtx, "bash", filepath.Join(root, "deploy/kw/bootstrap.sh"))
			budget := "10"
			if scenario == "timeout" {
				budget = "2"
			}
			cmd.Env = guardEnvironment(os.Environ(), map[string]string{"PATH": dir + string(os.PathListSeparator) + os.Getenv("PATH"), "NEXORA_KW_GUARD_SOCKET": guard.path, "NEXORA_KW_API_URL": server.URL, "NEXORA_KW_CATEGORIES": "", "NEXORA_KW_BOOTSTRAP_WAIT_SECONDS": budget, "NO_PROXY": "*", "no_proxy": "*"})
			out, err := cmd.CombinedOutput()
			wantOK := scenario == "lag" || scenario == "ready"
			if (err == nil) != wantOK {
				t.Fatalf("result=%v reads=%d versions=%d output=%s", err, reads.Load(), versions.Load(), out)
			}
			if commandCtx.Err() != nil {
				t.Fatalf("script exceeded test deadline: %s", out)
			}
			if reads.Load() < 2 {
				t.Fatalf("did not reach convergence: %s", out)
			}
			if writes.Load() != 2 {
				t.Fatalf("expected only login and existing list refresh, got %d mutations", writes.Load())
			}
			if wantOK && !strings.Contains(string(out), "1234 acknowledged") {
				t.Fatalf("missing acknowledgement: %s", out)
			}
			if scenario == "lag" && reads.Load() != 3 {
				t.Fatalf("lag must poll once, reads=%d", reads.Load())
			}
			if scenario == "timeout" && !strings.Contains(string(out), "timed out") {
				t.Fatalf("wrong failure: %s", out)
			}
			if !wantOK && scenario != "timeout" && reads.Load() != 2 {
				t.Fatalf("permanent failure was retried: %d", reads.Load())
			}
			if guards.Load() == 0 {
				t.Fatal("ownership was not checked")
			}
		})
	}
}

func TestBootstrapLagStillFailsStrictManagementProbe(t *testing.T) {
	engines, expected := healthyManagementFixture()
	for i := range engines {
		engines[i].AppliedVersion = 1233
		engines[i].TargetVersion = 1233
	}
	client := &http.Client{Transport: bootstrapFixtureTransport(func(r *http.Request) (*http.Response, error) {
		body := `[{"version":1234}]`
		if r.URL.Path == "/api/v1/engines" {
			raw, _ := json.Marshal(engines)
			body = string(raw)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	err := CheckManagement(context.Background(), client, "https://fixture.invalid", expected)
	if err == nil || !strings.Contains(err.Error(), "applied=1233 target=1233 expected=1234") {
		t.Fatalf("error=%v", err)
	}
}

type bootstrapFixtureTransport func(*http.Request) (*http.Response, error)

func (f bootstrapFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
