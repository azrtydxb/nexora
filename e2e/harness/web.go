package harness

// debt: InitCA, StartMgmt, NewAPI and Bootstrap are the Task 16 interface (planned for
// e2e/harness/mgmt.go) added here because Task 19's GUI test needs them first; move them to
// mgmt.go when Task 16 extends them (join tokens, managed engines).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// CA is a management-plane certificate authority on disk.
type CA struct{ Dir, CertFile, KeyFile string }

// InitCA runs `nexora-mgmt ca init` into a fresh directory.
func (e *Env) InitCA() *CA {
	e.T.Helper()
	dir := filepath.Join(e.Dir, "ca")
	// The binary path comes from Bin (the harness's own build output directories).
	out, err := exec.Command(e.Bin("nexora-mgmt"), "ca", "init", "--out", dir).CombinedOutput() // nosemgrep: dangerous-exec-command
	if err != nil {
		e.T.Fatalf("nexora-mgmt ca init: %v\n%s", err, out)
	}
	return &CA{Dir: dir, CertFile: filepath.Join(dir, "ca.crt"), KeyFile: filepath.Join(dir, "ca.key")}
}

// MgmtOptions configures StartMgmt.
type MgmtOptions struct {
	QueryLogBackend, OpenSearchURL, OTLPEndpoint string
	OIDC                                         *OIDCFixture
	OIDCAdminGroup, OIDCOperatorGroup            string
	ExtraEnv                                     []string
}

// Mgmt is a running `nexora-mgmt serve`.
type Mgmt struct {
	HTTPAddr, GRPCAddr, BaseURL, GRPCURL string
	Proc                                 *Proc
}

var (
	httpListening = regexp.MustCompile(`http listening on `)
	setupTokenRE  = regexp.MustCompile(`setup token: (\S+)`)
)

// StartMgmt starts `nexora-mgmt serve` on random loopback ports (insecure cookies, since the test
// browser talks plain HTTP) and waits until the HTTP API listens.
func (e *Env) StartMgmt(pg *Postgres, ca *CA, o MgmtOptions) *Mgmt {
	e.T.Helper()
	m := &Mgmt{
		HTTPAddr: fmt.Sprintf("127.0.0.1:%d", e.FreePort()),
		GRPCAddr: fmt.Sprintf("127.0.0.1:%d", e.FreePort()),
	}
	m.BaseURL = "http://" + m.HTTPAddr
	m.GRPCURL = "https://" + m.GRPCAddr
	env := []string{
		"NEXORA_DATABASE_URL=" + pg.URL,
		"NEXORA_CA_CERT_FILE=" + ca.CertFile,
		"NEXORA_CA_KEY_FILE=" + ca.KeyFile,
		"NEXORA_HTTP_LISTEN=" + m.HTTPAddr,
		"NEXORA_GRPC_LISTEN=" + m.GRPCAddr,
		"NEXORA_GRPC_SERVER_NAMES=127.0.0.1,localhost",
		"NEXORA_PUBLIC_URL=" + m.BaseURL,
		"NEXORA_SECURE_COOKIES=false",
	}
	if o.QueryLogBackend != "" {
		env = append(env, "NEXORA_QUERYLOG_BACKEND="+o.QueryLogBackend)
	}
	if o.OpenSearchURL != "" {
		env = append(env, "NEXORA_OPENSEARCH_URL="+o.OpenSearchURL)
	}
	if o.OTLPEndpoint != "" {
		env = append(env, "NEXORA_OTLP_ENDPOINT="+o.OTLPEndpoint)
	}
	if o.OIDC != nil {
		env = append(env,
			"NEXORA_OIDC_ISSUER="+o.OIDC.Issuer,
			"NEXORA_OIDC_CLIENT_ID="+o.OIDC.ClientID,
			"NEXORA_OIDC_CLIENT_SECRET_FILE="+o.OIDC.ClientSecretFile,
			"NEXORA_OIDC_ADMIN_GROUP="+o.OIDCAdminGroup,
			"NEXORA_OIDC_OPERATOR_GROUP="+o.OIDCOperatorGroup,
		)
	}
	m.Proc = e.Start("nexora-mgmt", []string{"serve"}, append(env, o.ExtraEnv...))
	m.Proc.WaitLog(httpListening, 30*time.Second)
	return m
}

// SetupToken returns the setup token this instance logged, or "" when it logged none (users
// already exist or another instance created the token).
func (m *Mgmt) SetupToken(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(m.Proc.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if sub := setupTokenRE.FindSubmatch(data); sub != nil {
		return string(sub[1])
	}
	return ""
}

// API is an HTTP client for the management API under <Base>/api/v1.
type API struct {
	T      *testing.T
	Base   string
	HC     *http.Client
	Bearer string
}

// NewAPI returns a client with its own cookie jar (one browser-like session per client).
func (e *Env) NewAPI(baseURL string) *API {
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.T.Fatal(err)
	}
	return &API{T: e.T, Base: strings.TrimSuffix(baseURL, "/"), HC: &http.Client{
		Jar:       jar,
		Timeout:   30 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}}
}

// Do sends body as JSON (when non-nil) to /api/v1<path> and decodes a 2xx JSON response into out
// (when non-nil). A non-2xx response returns its status with an error holding the body.
func (a *API) Do(method, path string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, a.Base+"/api/v1"+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.Bearer != "" {
		req.Header.Set("Authorization", "Bearer "+a.Bearer)
	}
	resp, err := a.HC.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, data)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: decode: %v: %s", method, path, err, data)
		}
	}
	return resp.StatusCode, nil
}

// Must is Do that fails the test unless the status is want.
func (a *API) Must(method, path string, body, out any, want int) {
	a.T.Helper()
	status, err := a.Do(method, path, body, out)
	if status != want {
		a.T.Fatalf("%s %s: status %d, want %d (%v)", method, path, status, want, err)
	}
	if want >= 200 && want <= 299 && err != nil {
		a.T.Fatal(err)
	}
}

// Bootstrap completes setup as admin / admin-password-e2e, creates an admin API token and returns
// a bearer client using it.
func Bootstrap(t *testing.T, e *Env, setupToken, baseURL string) *API {
	t.Helper()
	if setupToken == "" {
		t.Fatal("Bootstrap: no setup token was logged")
	}
	session := e.NewAPI(baseURL)
	session.Must("POST", "/setup", map[string]string{
		"token": setupToken, "username": "admin", "email": "admin@example.test", "password": "admin-password-e2e",
	}, nil, http.StatusCreated)
	var created struct {
		Token string `json:"token"`
	}
	session.Must("POST", "/api-tokens", map[string]any{"name": "e2e-admin", "role": "admin"}, &created, http.StatusCreated)
	admin := e.NewAPI(baseURL)
	admin.Bearer = created.Token
	return admin
}

// RunPlaywright runs `pnpm exec playwright test <specs...>` in <repo>/web with env (plus CI=1),
// logs the output and fails the test on a non-zero exit. The coverage directory is
// env["NEXORA_E2E_COVERAGE_DIR"] when set, otherwise a new temporary directory; it is returned.
func RunPlaywright(t *testing.T, specs []string, env map[string]string) string {
	t.Helper()
	coverage := env["NEXORA_E2E_COVERAGE_DIR"]
	if coverage == "" {
		coverage = t.TempDir()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	// specs come from test code; pnpm resolves from PATH like any developer invocation.
	cmd := exec.CommandContext(ctx, "pnpm", append([]string{"exec", "playwright", "test"}, specs...)...) // nosemgrep: dangerous-exec-command
	cmd.Dir = filepath.Join(repoRoot(), "web")
	cmd.Env = append(os.Environ(), "CI=1")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Env = append(cmd.Env, "NEXORA_E2E_COVERAGE_DIR="+coverage)
	out, err := cmd.CombinedOutput()
	t.Logf("playwright %s:\n%s", strings.Join(specs, " "), out)
	if err != nil {
		t.Fatalf("playwright %s: %v", strings.Join(specs, " "), err)
	}
	return coverage
}
