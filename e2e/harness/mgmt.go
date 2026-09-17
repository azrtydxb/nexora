package harness

import (
	"bytes"
	"context"
	"crypto/x509"
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

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
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
	// ClickHouseURL (HTTP interface, read as nexora_reader with ClickHousePasswordFile) and LokiURL
	// configure the M10 query-log backends.
	ClickHouseURL, ClickHousePasswordFile, LokiURL string
	OIDC                                           *OIDCFixture
	OIDCAdminGroup, OIDCOperatorGroup              string
	ExtraEnv                                       []string
	// DNSTLS issues a DNS serving certificate (dns.nexora.test, 127.0.0.1) from the CA into
	// <env dir>/dnstls and points the instance at it with a 1 s reload interval.
	DNSTLS bool
}

// Mgmt is a running `nexora-mgmt serve`.
type Mgmt struct {
	HTTPAddr, GRPCAddr, BaseURL, GRPCURL string
	Proc                                 *Proc
	// DNSTLSDir holds tls.crt and tls.key when MgmtOptions.DNSTLS is set.
	DNSTLSDir string

	env   *Env
	ca    *CA
	roots *x509.CertPool
}

var (
	setupTokenRE     = regexp.MustCompile(`setup token: (\S+)`)
	controlConnected = regexp.MustCompile(`nexora-engine: control connected to `)
)

// StartMgmt starts `nexora-mgmt serve` on kernel-chosen loopback ports (insecure cookies, since
// the test browser talks plain HTTP) and waits until the HTTP API and the gRPC server listen.
// NEXORA_PUBLIC_URL must be known before the instance picks its port, so it names a loopback port
// the harness holds and forwards to HTTPAddr; BaseURL is the instance's own listener.
func (e *Env) StartMgmt(pg *Postgres, ca *CA, o MgmtOptions) *Mgmt {
	e.T.Helper()
	m := &Mgmt{env: e, ca: ca, roots: caPool(e.T, ca)}
	public := e.listenLoopback()
	backend := o.QueryLogBackend
	if backend == "" {
		backend = "builtin"
	}
	env := []string{
		"NEXORA_DATABASE_URL=" + pg.URL,
		"NEXORA_CA_CERT_FILE=" + ca.CertFile,
		"NEXORA_CA_KEY_FILE=" + ca.KeyFile,
		"NEXORA_HTTP_LISTEN=" + loopbackPort0,
		"NEXORA_GRPC_LISTEN=" + loopbackPort0,
		"NEXORA_GRPC_SERVER_NAMES=127.0.0.1,localhost",
		"NEXORA_PUBLIC_URL=http://" + public.Addr().String(),
		"NEXORA_SECURE_COOKIES=false",
		"NEXORA_QUERYLOG_BACKEND=" + backend,
	}
	if o.DNSTLS {
		m.DNSTLSDir = filepath.Join(e.Dir, "dnstls")
		e.issueDNSTLS(e.T, ca, m.DNSTLSDir)
		env = append(env,
			"NEXORA_DNS_TLS_CERT_FILE="+filepath.Join(m.DNSTLSDir, "tls.crt"),
			"NEXORA_DNS_TLS_KEY_FILE="+filepath.Join(m.DNSTLSDir, "tls.key"),
			"NEXORA_DNS_TLS_RELOAD_INTERVAL=1s",
		)
	}
	if o.OpenSearchURL != "" {
		env = append(env, "NEXORA_OPENSEARCH_URL="+o.OpenSearchURL)
	}
	if o.ClickHouseURL != "" {
		env = append(env, "NEXORA_CLICKHOUSE_URL="+o.ClickHouseURL, "NEXORA_CLICKHOUSE_USERNAME=nexora_reader")
		if o.ClickHousePasswordFile != "" {
			env = append(env, "NEXORA_CLICKHOUSE_PASSWORD_FILE="+o.ClickHousePasswordFile)
		}
	}
	if o.LokiURL != "" {
		env = append(env, "NEXORA_LOKI_URL="+o.LokiURL)
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
	ready := m.Proc.WaitReady(30 * time.Second)
	m.HTTPAddr, m.GRPCAddr = m.Proc.Addr(ready, "http"), m.Proc.Addr(ready, "grpc")
	m.BaseURL, m.GRPCURL = "http://"+m.HTTPAddr, "https://"+m.GRPCAddr
	e.forward(public, func() []string { return []string{m.HTTPAddr} })
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

// DisableForwardedValidation turns off DNSSEC validation of answers from the global upstreams, for
// tests whose fixture upstreams serve unsigned synthetic data under the real root trust anchor.
func (a *API) DisableForwardedValidation() {
	a.T.Helper()
	var s map[string]any
	a.Must("GET", "/dnssec/settings", nil, &s, http.StatusOK)
	s["validate_forwarded"] = false
	a.Must("PUT", "/dnssec/settings", s, nil, http.StatusOK)
}

// CreateJoinToken creates a one-hour join token and returns the full token string.
func (a *API) CreateJoinToken() string {
	a.T.Helper()
	var created struct {
		Token string `json:"token"`
	}
	a.Must("POST", "/join-tokens", map[string]any{"name": UniqueName("e2e"), "ttl_seconds": 3600}, &created, http.StatusCreated)
	return created.Token
}

// LatestVersion returns the newest config version.
func (a *API) LatestVersion() uint64 {
	a.T.Helper()
	var versions []struct {
		Version uint64 `json:"version"`
	}
	a.Must("GET", "/config-versions?limit=1", nil, &versions, http.StatusOK)
	if len(versions) == 0 {
		a.T.Fatal("no config version exists")
	}
	return versions[0].Version
}

// EngineView is the part of the API's Engine the tests inspect.
type EngineView struct {
	ID              string  `json:"id"`
	NodeName        string  `json:"node_name"`
	Status          string  `json:"status"`
	RejectedReason  string  `json:"rejected_reason"`
	AppliedVersion  uint64  `json:"applied_version"`
	RejectedVersion *uint64 `json:"rejected_version"`
	Connected       bool    `json:"connected"`

	EngineGroupID     string            `json:"engine_group_id"`
	EngineGroupName   string            `json:"engine_group_name"`
	Labels            map[string]string `json:"labels"`
	Revision          int64             `json:"revision"`
	TargetVersion     uint64            `json:"target_version"`
	CertificateSerial string            `json:"certificate_serial"`
	RevokedAt         *time.Time        `json:"revoked_at"`
}

// WaitEngine polls GET /engines every 200 ms until the engine named nodeName satisfies cond.
func (a *API) WaitEngine(nodeName string, timeout time.Duration, cond func(EngineView) bool) EngineView {
	a.T.Helper()
	deadline := time.Now().Add(timeout)
	var last []EngineView
	var lastErr error
	for {
		var engines []EngineView
		_, lastErr = a.Do("GET", "/engines", nil, &engines)
		if lastErr == nil {
			last = engines
			for _, v := range engines {
				if v.NodeName == nodeName && cond(v) {
					return v
				}
			}
		}
		if time.Now().After(deadline) {
			a.T.Fatalf("engine %s did not reach the expected state within %s: engines %+v (last error %v)", nodeName, timeout, last, lastErr)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// EngineOptions adds encrypted listeners (port 0 on loopback) to a managed engine.
type EngineOptions struct {
	DoT, DoH, DoQ, ProxyProtocolDoT bool
	ProxyTrustedCIDRs               []string
	// ExtraEnv is added to the engine process environment (also on RestartEngine).
	ExtraEnv []string
	// SkipControlWait returns once the READY line is read instead of waiting for the control stream.
	SkipControlWait bool
}

// StartManagedEngine runs nexora-engine enrolled through joinToken against grpcURLs on loopback
// and waits until its control stream connects.
func (e *Env) StartManagedEngine(nodeName string, grpcURLs []string, joinToken string) *Engine {
	e.T.Helper()
	return e.StartManagedEngineWith(nodeName, grpcURLs, joinToken, EngineOptions{})
}

// StartManagedEngineWith is StartManagedEngine with the encrypted listeners o enables.
func (e *Env) StartManagedEngineWith(nodeName string, grpcURLs []string, joinToken string, o EngineOptions) *Engine {
	e.T.Helper()
	dir, err := os.MkdirTemp(e.Dir, "engine-")
	if err != nil {
		e.T.Fatal(err)
	}
	en := &Engine{StateDir: filepath.Join(dir, "state"), ConfigPath: filepath.Join(dir, "engine.toml")}
	if err := os.MkdirAll(en.StateDir, 0o700); err != nil {
		e.T.Fatal(err)
	}
	tokenFile := filepath.Join(dir, "join-token")
	if err := os.WriteFile(tokenFile, []byte(joinToken+"\n"), 0o600); err != nil {
		e.T.Fatal(err)
	}
	// JSON string arrays are valid TOML arrays.
	tomlArray := func(v []string) string {
		raw, err := json.Marshal(v)
		if err != nil {
			e.T.Fatal(err)
		}
		return string(raw)
	}
	toml := fmt.Sprintf(`node_name = %q
state_dir = %q
management_urls = %s
join_token_file = %q
listen_udp = [%q]
listen_tcp = [%q]
metrics_listen = %q
workers = 2
`, nodeName, en.StateDir, tomlArray(grpcURLs), tokenFile, loopbackPort0, loopbackPort0, loopbackPort0)
	for _, l := range []struct {
		on  bool
		key string
	}{{o.DoT, "listen_dot"}, {o.DoH, "listen_doh"}, {o.DoQ, "listen_doq"}} {
		if l.on {
			toml += fmt.Sprintf("%s = [%q]\n", l.key, loopbackPort0)
		}
	}
	if o.ProxyProtocolDoT {
		toml += "proxy_protocol_dot = true\n"
	}
	if len(o.ProxyTrustedCIDRs) > 0 {
		toml += "proxy_protocol_trusted_cidrs = " + tomlArray(o.ProxyTrustedCIDRs) + "\n"
	}
	if err := os.WriteFile(en.ConfigPath, []byte(toml), 0o600); err != nil {
		e.T.Fatal(err)
	}
	en.env = o.ExtraEnv
	en.Proc = e.Start("nexora-engine", []string{"--config", en.ConfigPath}, en.env)
	en.readAddrs()
	if !o.SkipControlWait {
		en.Proc.WaitLog(controlConnected, 30*time.Second)
	}
	return en
}

// PublishRawSnapshot stores snap unvalidated as the next config version directly in the database
// (as the management plane's builder never would) and notifies the instances. It returns the
// version assigned.
func PublishRawSnapshot(t *testing.T, pgURL string, snap *controlv1.ConfigSnapshot) uint64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, pgURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.WithoutCancel(ctx))
	var version uint64
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtext('nexora:config_version'))"); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "select coalesce(max(version), 0) + 1 from config_versions").Scan(&version); err != nil {
			return err
		}
		snap.Version = version
		raw, err := proto.Marshal(snap)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "insert into config_versions(version, created_by, summary) values ($1, 'e2e', 'raw snapshot')", version); err != nil {
			return err
		}
		// Every engine group gets the snapshot, rolled out at once (the management plane's
		// snapshot.PublishRaw, which the harness cannot import).
		rows, err := tx.Query(ctx, "select id::text from engine_groups order by id for no key update")
		if err != nil {
			return err
		}
		groups, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, g := range groups {
			if _, err := tx.Exec(ctx, `insert into group_snapshots(version, engine_group_id, snapshot, content_sha256)
				values ($1, $2, $3, encode(sha256($3), 'hex'))`, int64(version), g, raw); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update rollouts set state = 'superseded', finished_at = now(), updated_at = now()
				where engine_group_id = $1 and state in ('pending', 'canary', 'verifying', 'rolling', 'halted')`, g); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into rollouts(engine_group_id, version, kind, strategy, state, params, created_by, phase_started_at)
				values ($1, $2, 'change', 'all_at_once', 'rolling', '{"strategy":"all_at_once","ack_timeout_seconds":60}', 'e2e', now())`,
				g, int64(version)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "select pg_notify('nexora_rollout', $1)", g); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, "select pg_notify('nexora_config', $1)", fmt.Sprint(version))
		return err
	})
	if err != nil {
		t.Fatalf("publish raw snapshot: %v", err)
	}
	return version
}
