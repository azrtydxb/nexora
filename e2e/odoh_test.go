package e2e

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/e2e/harness"
)

const odohMediaType = "application/oblivious-dns-message"

// TestODoHTargetAndProxy uses the independent circl client against two enrolled
// product engines. Only publication/expiry timestamps are accelerated, in this
// test's disposable database; key generation and delivery remain product code.
func TestODoHTargetAndProxy(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	fx := env.StartDNSFixture()
	fx.SetRecords(t, "www.example.test. 300 IN A 192.0.2.10", "privacy.example.test. 300 IN A 192.0.2.10")
	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{DNSTLS: true, ExtraEnv: []string{"NEXORA_KEK_FILE=" + harness.WriteKEK(t)}})
	api := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
	api.DisableForwardedValidation()
	api.Must(http.MethodPost, "/upstreams", map[string]any{"name": "odoh-fixture", "protocol": "udp", "address": fx.UDP, "timeout_ms": 500, "enabled": true, "position": 0}, nil, http.StatusCreated)
	// Roles and key delivery are fleet-wide. Give the proxy its own managed
	// deployment, without a KEK or target keys, so this actually proves the
	// privacy boundary rather than handing both engines the target's secrets.
	proxyEnv := harness.New(t)
	proxyCA := proxyEnv.InitCA()
	proxyMgmt := proxyEnv.StartMgmt(proxyEnv.StartPostgres(), proxyCA, harness.MgmtOptions{DNSTLS: true, ExtraEnv: []string{"NEXORA_KEK_FILE="}})
	proxyAPI := harness.Bootstrap(t, proxyEnv, proxyMgmt.SetupToken(t), proxyMgmt.BaseURL)
	proxyAPI.DisableForwardedValidation()
	proxyAPI.Must(http.MethodPost, "/upstreams", map[string]any{"name": "odoh-fixture", "protocol": "udp", "address": fx.UDP, "timeout_ms": 500, "enabled": true, "position": 0}, nil, http.StatusCreated)
	opts := harness.EngineOptions{DoH: true}
	a := proxyEnv.StartManagedEngineWith("odoh-a", []string{proxyMgmt.GRPCURL}, proxyAPI.CreateJoinToken(), opts)
	b := env.StartManagedEngineWith("odoh-b", []string{mg.GRPCURL}, api.CreateJoinToken(), opts)
	waitLatestApplied(t, proxyAPI, "odoh-a")
	waitLatestApplied(t, api, "odoh-b")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	roots := mg.DNSTLSRoots()
	proxyCAPEM, err := os.ReadFile(proxyCA.CertFile)
	if err != nil {
		t.Fatal(err)
	}
	if !roots.AppendCertsFromPEM(proxyCAPEM) {
		t.Fatal("proxy CA has no certificate")
	}
	client := harness.EncryptedClient{RootCAs: roots, ServerName: "dns.nexora.test", LocalIP: net.ParseIP("127.0.0.2")}
	hc := client.HTTPClient()
	defer hc.CloseIdleConnections()
	// Wait only for asynchronous TLS/control readiness, before checking disabled defaults.
	for _, en := range []*harness.Engine{a, b} {
		harness.Eventually(t, 30*time.Second, func() error {
			m, _, err := harness.DoH(ctx, hc, en.DoHURL(), http.MethodPost, question("www.example.test.", dns.TypeA))
			if err != nil {
				return err
			}
			if !odohHasAnswer(m) {
				return fmt.Errorf("DoH baseline: %v", m)
			}
			return nil
		})
	}
	base := strings.TrimSuffix(b.DoHURL(), "/dns-query")
	proxyURL := func(host, path string) string {
		return a.DoHURL() + "?" + url.Values{"targethost": {host}, "targetpath": {path}}.Encode()
	}
	target := strings.TrimPrefix(base, "https://")
	var settings struct {
		Revision      int64 `json:"revision"`
		TargetEnabled bool  `json:"target_enabled"`
		ProxyEnabled  bool  `json:"proxy_enabled"`
		Keys          []struct {
			ID string `json:"id"`
		} `json:"keys"`
	}
	api.Must(http.MethodGet, "/odoh", nil, &settings, 200)
	if settings.TargetEnabled || settings.ProxyEnabled || len(settings.Keys) != 0 {
		t.Fatal("ODoH must default to disabled with no keys")
	}
	cfg, resp, err := harness.FetchODoHConfigs(ctx, hc, base)
	odohStatus(t, resp, err, 404, "")
	if len(cfg) != 0 {
		t.Fatal("disabled target published configs")
	}
	raw := func(endpoint, ct string, body []byte, want int, ps string) *http.Response {
		t.Helper()
		r, e := harness.ODoHRaw(ctx, hc, endpoint, ct, body)
		odohStatus(t, r, e, want, ps)
		return r
	}
	raw(b.DoHURL(), odohMediaType, []byte{1}, 415, "")
	raw(proxyURL(target, "/dns-query"), odohMediaType, []byte{1}, 403, "nexora; error=http_request_denied")
	update := func(targetOn, proxyOn bool, targets []map[string]string) {
		t.Helper()
		if targets == nil {
			targets = []map[string]string{}
		}
		for _, role := range []struct {
			api           *harness.API
			node          string
			target, proxy bool
			targets       []map[string]string
		}{
			{api, "odoh-b", targetOn, false, []map[string]string{}},
			{proxyAPI, "odoh-a", false, proxyOn, targets},
		} {
			role.api.Must(http.MethodGet, "/odoh", nil, &settings, 200)
			role.api.Must(http.MethodPut, "/odoh", map[string]any{"revision": settings.Revision, "target_enabled": role.target, "proxy_enabled": role.proxy, "proxy_targets": role.targets, "proxy_timeout_ms": 1000, "key_rotation_hours": 1}, nil, 200)
			waitLatestApplied(t, role.api, role.node)
		}
	}
	rotate := func() string {
		t.Helper()
		api.Must(http.MethodPost, "/odoh/rotate-key", nil, &settings, 200)
		if len(settings.Keys) == 0 {
			t.Fatal("rotation returned no key metadata")
		}
		return settings.Keys[0].ID
	}
	notify := func() { harness.PGExec(t, pg.URL, "SELECT pg_notify('nexora_odoh_keys', '')") }
	publish := func(id string) {
		harness.PGExec(t, pg.URL, "UPDATE odoh_keys SET publish_after = now() - interval '1 second' WHERE id = $1", id)
		notify()
	}
	// Rotate while disabled to avoid the periodic scheduler racing initial key creation.
	oldID := rotate()
	update(true, false, nil)
	_, resp, err = harness.FetchODoHConfigs(ctx, hc, base)
	odohStatus(t, resp, err, 503, "") // key is accepted but not yet publicly discoverable
	publish(oldID)
	configs := odohWaitConfigs(t, ctx, hc, base, 1)
	old := configs[0]
	var ciphertext []byte
	observing := *hc
	observing.Transport = odohTransport(func(r *http.Request) (*http.Response, error) {
		body, e := io.ReadAll(r.Body)
		if e != nil {
			return nil, e
		}
		r.Body.Close()
		ciphertext = bytes.Clone(body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		return hc.Transport.RoundTrip(r)
	})
	odohAnswer(t, ctx, &observing, b.DoHURL(), old, "www.example.test.", true, "")
	if len(ciphertext) < 40 {
		t.Fatal("no real encrypted query captured")
	}
	garbled := bytes.Clone(ciphertext)
	garbled[len(garbled)-1] ^= 1
	unknown := bytes.Clone(ciphertext)
	unknown[3] ^= 1 // change key id, preserving framing
	wrongType := bytes.Clone(ciphertext)
	wrongType[0] = 2
	for _, tc := range []struct {
		name, ct string
		body     []byte
		want     int
	}{
		{"ciphertext authentication", odohMediaType, garbled, 400},
		{"unknown key", odohMediaType, unknown, 401},
		{"wrong message type", odohMediaType, wrongType, 400},
		{"truncated framing", odohMediaType, ciphertext[:3], 400},
		{"trailing bytes", odohMediaType, append(bytes.Clone(ciphertext), 0), 400},
		{"content type", "application/json", []byte(`{}`), 415},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, e := harness.ODoHRaw(ctx, hc, b.DoHURL(), tc.ct, tc.body)
			odohStatus(t, r, e, tc.want, "")
		})
	}

	newID := rotate()
	if newID == oldID {
		t.Fatal("rotation reused key id")
	}
	configs = odohWaitConfigs(t, ctx, hc, base, 1)
	if !bytes.Equal(configs[0].PublicKey, old.PublicKey) {
		t.Fatal("new key published before publish_after")
	}
	odohAnswer(t, ctx, hc, b.DoHURL(), old, "www.example.test.", true, "")
	publish(newID)
	configs = odohWaitConfigs(t, ctx, hc, base, 2)
	fresh := configs[0]
	if bytes.Equal(fresh.PublicKey, old.PublicKey) || !bytes.Equal(configs[1].PublicKey, old.PublicKey) {
		t.Fatal("rotation did not publish newest first with old key overlap")
	}
	odohAnswer(t, ctx, hc, b.DoHURL(), fresh, "www.example.test.", true, "")
	odohAnswer(t, ctx, hc, b.DoHURL(), old, "www.example.test.", true, "")
	// Rewind publication after obtaining ONLY the public config. This tests acceptance
	// before publish_after without deriving a config from a management-plane seed.
	harness.PGExec(t, pg.URL, "UPDATE odoh_keys SET publish_after = now() + interval '5 minutes' WHERE id = $1", newID)
	notify()
	configs = odohWaitConfigs(t, ctx, hc, base, 1)
	if !bytes.Equal(configs[0].PublicKey, old.PublicKey) {
		t.Fatal("unpublished key still advertised")
	}
	odohAnswer(t, ctx, hc, b.DoHURL(), fresh, "www.example.test.", true, "")
	publish(newID)
	odohWaitConfigs(t, ctx, hc, base, 2)
	harness.PGExec(t, pg.URL, "UPDATE odoh_keys SET not_after = now() + interval '8 seconds' WHERE id = $1", oldID)
	notify()
	odohAnswer(t, ctx, hc, b.DoHURL(), old, "www.example.test.", true, "")
	configs = odohWaitConfigs(t, ctx, hc, base, 1)
	if !bytes.Equal(configs[0].PublicKey, fresh.PublicKey) {
		t.Fatal("wrong key survived expiry")
	}
	m, r, e := harness.ODoHQuery(ctx, hc, b.DoHURL(), old, question("www.example.test.", dns.TypeA))
	odohStatus(t, r, e, 401, "")
	if m != nil {
		t.Fatal("expired key returned plaintext")
	}
	// Capture fresh ciphertext for proxy rejection tests: the previous ciphertext
	// now refers to the expired key and would mask authentication failures as 401.
	odohAnswer(t, ctx, &observing, b.DoHURL(), fresh, "www.example.test.", true, "")
	garbled = bytes.Clone(ciphertext)
	garbled[len(garbled)-1] ^= 1
	unknown = bytes.Clone(ciphertext)
	unknown[3] ^= 1

	caPEM, err := os.ReadFile(ca.CertFile)
	if err != nil {
		t.Fatal(err)
	}
	targets := []map[string]string{{"host": target, "ca_pem": string(caPEM)}}
	update(true, true, targets)
	odohAnswer(t, ctx, hc, proxyURL(target, "/dns-query"), fresh, "privacy.example.test.", false, "nexora; received-status=200")
	// A's connecting client is 127.0.0.2; B must see A's 127.0.0.1, never
	// the original address. The unique name makes the log check non-vacuous.
	bID := api.EngineByNode("odoh-b").ID
	harness.Eventually(t, 30*time.Second, func() error {
		var page struct {
			Records []struct {
				Client   string `json:"client"`
				EngineID string `json:"engine_id"`
				Name     string `json:"name"`
			} `json:"records"`
		}
		api.Must(http.MethodGet, "/query-log?name=privacy.example.test&limit=100", nil, &page, 200)
		found := false
		for _, record := range page.Records {
			if record.EngineID == bID {
				found = true
				if record.Client != "127.0.0.1" {
					t.Fatalf("target saw original client: %q", record.Client)
				}
			}
		}
		if !found {
			return fmt.Errorf("target query log entry has not arrived")
		}
		return nil
	})
	raw(proxyURL("denied.invalid", "/dns-query"), odohMediaType, ciphertext, 403, "nexora; error=http_request_denied")
	// The proxy's own listener is deliberately outside the allow list: no self loop.
	raw(proxyURL(strings.TrimPrefix(strings.TrimSuffix(a.DoHURL(), "/dns-query"), "https://"), "/dns-query"), odohMediaType, ciphertext, 403, "nexora; error=http_request_denied")
	for _, endpoint := range []string{a.DoHURL() + "?targethost=" + url.QueryEscape(target), proxyURL(target, "relative"), proxyURL("user@"+target, "/dns-query"), proxyURL(target+"/path", "/dns-query"), proxyURL(target, "/dns-query") + "&targethost=" + url.QueryEscape(target)} {
		raw(endpoint, odohMediaType, ciphertext, 400, "nexora; error=http_request_error")
	}
	direct401 := raw(b.DoHURL(), odohMediaType, unknown, 401, "")
	relay401 := raw(proxyURL(target, "/dns-query"), odohMediaType, unknown, 401, "nexora; received-status=401")
	directBody, err := io.ReadAll(direct401.Body)
	if err != nil {
		t.Fatal(err)
	}
	relayBody, err := io.ReadAll(relay401.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(directBody, relayBody) || direct401.Header.Get("Content-Type") != relay401.Header.Get("Content-Type") {
		t.Fatal("proxy changed target 401 body/content type")
	}
	raw(proxyURL(target, "/dns-query"), odohMediaType, garbled, 400, "nexora; received-status=400")
	raw(proxyURL(target, "/dns-query"), odohMediaType, make([]byte, 65535+1024+1), 413, "")
	raw(proxyURL(target, "/dns-query"), odohMediaType, make([]byte, 65535+1024), 400, "nexora; received-status=400")

	// A TLS observer forwards the unchanged opaque bytes to the actual B engine.
	// It implements no ODoH codec or DNS answer and cannot fabricate a passing exchange.
	type capture struct {
		header http.Header
		body   []byte
		peer   string
	}
	captures := make(chan capture, 1)
	relayClient := harness.EncryptedClient{RootCAs: mg.DNSTLSRoots(), ServerName: "dns.nexora.test"}.HTTPClient()
	defer relayClient.CloseIdleConnections()
	observer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		captures <- capture{r.Header.Clone(), bytes.Clone(body), r.RemoteAddr}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, b.DoHURL(), bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		req.Header.Set("Content-Type", r.Header.Get("Content-Type"))
		req.Header.Set("Accept", r.Header.Get("Accept"))
		resp, err := relayClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	// Use the disposable harness's issued chain and trust its CA, rather than
	// treating httptest's end-entity certificate as a CA accepted by rustls.
	observerCert, err := tls.LoadX509KeyPair(filepath.Join(mg.DNSTLSDir, "tls.crt"), filepath.Join(mg.DNSTLSDir, "tls.key"))
	if err != nil {
		t.Fatal(err)
	}
	observer.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{observerCert}}
	observer.EnableHTTP2 = true
	observer.StartTLS()
	defer observer.Close()
	observerCA := string(caPEM)
	observerHost := strings.TrimPrefix(observer.URL, "https://")
	targets = append(targets, map[string]string{"host": observerHost, "ca_pem": observerCA})
	update(true, true, targets)
	privateClient := *hc
	var sent []byte
	privateClient.Transport = odohTransport(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		r.Body.Close()
		sent = bytes.Clone(body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.Header.Set("X-Forwarded-For", "198.51.100.77")
		r.Header.Set("Forwarded", "for=198.51.100.77")
		r.Header.Set("Cookie", "odoh-private=do-not-forward")
		r.Header.Set("Authorization", "Bearer do-not-forward")
		r.Header.Set("X-Real-IP", "198.51.100.77")
		r.Header.Set("User-Agent", "private-odoh-client")
		r.Header.Set("X-Private-Token", "do-not-forward")
		return hc.Transport.RoundTrip(r)
	})
	odohAnswer(t, ctx, &privateClient, proxyURL(observerHost, "/dns-query"), fresh, "www.example.test.", false, "nexora; received-status=200")
	select {
	case got := <-captures:
		if !bytes.Equal(got.body, sent) {
			t.Fatal("proxy changed encrypted body")
		}
		wire, err := question("www.example.test.", dns.TypeA).Pack()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(got.body, wire[12:]) || bytes.Contains(got.body, []byte("www.example.test")) {
			t.Fatal("proxy received plaintext DNS question")
		}
		for h := range got.header {
			switch http.CanonicalHeaderKey(h) {
			case "Content-Type", "Accept", "Content-Length": // transport framing is allowed
			default:
				t.Errorf("proxy sent forbidden header %s", h)
			}
		}
		if got.header.Get("Content-Type") != odohMediaType || got.header.Get("Accept") != odohMediaType {
			t.Fatalf("wrong forwarding media headers: %v", got.header)
		}
		peer, _, err := net.SplitHostPort(got.peer)
		if err != nil || peer != "127.0.0.1" {
			t.Fatalf("observer peer %q: %v", got.peer, err)
		}
	case <-ctx.Done():
		t.Fatal("no captured proxy request")
	}

	// Removing trust for the observer must surface a TLS failure, never bypass it.
	targets[len(targets)-1] = map[string]string{"host": observerHost}
	update(true, true, targets)
	raw(proxyURL(observerHost, "/dns-query"), odohMediaType, ciphertext, 502, "nexora; error=tls_protocol_error")

	// Real TCP endpoints exercise connection refusal and a TLS-handshake black hole.
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedHost := closed.Addr().String()
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	blackhole, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blackhole.Close()
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		for {
			c, err := blackhole.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				select {
				case <-stopped:
				case <-time.After(5 * time.Second):
				}
			}()
		}
	}()
	targets = append(targets, map[string]string{"host": closedHost}, map[string]string{"host": blackhole.Addr().String()})
	update(true, true, targets)
	raw(proxyURL(closedHost, "/dns-query"), odohMediaType, ciphertext, 502, "nexora; error=destination_unavailable")
	started := time.Now()
	raw(proxyURL(blackhole.Addr().String(), "/dns-query"), odohMediaType, ciphertext, 502, "nexora; error=connection_timeout")
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("proxy timeout took %s, limit 1000ms + 500ms", elapsed)
	}

	// The proxy deployment has no target seed/key and never gains a target role.
	proxyAPI.Must(http.MethodGet, "/odoh", nil, &settings, 200)
	if settings.TargetEnabled || !settings.ProxyEnabled || len(settings.Keys) != 0 {
		t.Fatal("proxy deployment acquired target role or key metadata")
	}
	var proxyLog struct {
		Records []any `json:"records"`
	}
	harness.Eventually(t, 30*time.Second, func() error {
		proxyAPI.Must(http.MethodGet, "/query-log?name=www.example.test&limit=100", nil, &proxyLog, 200)
		if len(proxyLog.Records) == 0 {
			return fmt.Errorf("proxy deployment's ordinary DoH baseline has not reached query log")
		}
		return nil
	})
	proxyAPI.Must(http.MethodGet, "/query-log?name=privacy.example.test&limit=100", nil, &proxyLog, 200)
	if len(proxyLog.Records) != 0 {
		t.Fatal("proxy logged the plaintext DNS question")
	}
	_, resp, err = harness.FetchODoHConfigs(ctx, hc, strings.TrimSuffix(a.DoHURL(), "/dns-query"))
	odohStatus(t, resp, err, 404, "")
	raw(a.DoHURL(), odohMediaType, ciphertext, 415, "")
	var acl struct {
		Revision int64 `json:"revision"`
	}
	for _, role := range []struct {
		api  *harness.API
		node string
	}{{api, "odoh-b"}, {proxyAPI, "odoh-a"}} {
		role.api.Must(http.MethodGet, "/access-control", nil, &acl, 200)
		role.api.Must(http.MethodPut, "/access-control", map[string]any{"revision": acl.Revision, "allow_cidrs": []string{"127.0.0.1/32"}, "authoritative_allow_cidrs": []string{"0.0.0.0/0", "::/0"}}, nil, 200)
		waitLatestApplied(t, role.api, role.node)
	}
	raw(proxyURL(target, "/dns-query"), odohMediaType, ciphertext, 403, "nexora; error=http_request_denied")
	m, r, e = harness.ODoHQuery(ctx, hc, b.DoHURL(), fresh, question("acl-refused.example.test.", dns.TypeA))
	odohStatus(t, r, e, 200, "")
	if m == nil || m.Rcode != dns.RcodeRefused || r.Header.Get("Content-Type") != odohMediaType || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("ACL DNS refusal must be encrypted HTTP 200: %v", m)
	}
	update(false, false, nil)
	_, resp, err = harness.FetchODoHConfigs(ctx, hc, base)
	odohStatus(t, resp, err, 404, "")
	raw(b.DoHURL(), odohMediaType, ciphertext, 415, "")
	raw(proxyURL(target, "/dns-query"), odohMediaType, ciphertext, 403, "nexora; error=http_request_denied")
}

type odohTransport func(*http.Request) (*http.Response, error)

func (f odohTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func odohStatus(t *testing.T, r *http.Response, err error, want int, proxyStatus string) {
	t.Helper()
	if err != nil {
		t.Fatalf("ODoH request: %v", err)
	}
	if r == nil {
		t.Fatal("ODoH response missing")
	}
	if r.StatusCode != want {
		t.Fatalf("ODoH status %d, want %d (Proxy-Status %q)", r.StatusCode, want, r.Header.Get("Proxy-Status"))
	}
	if r.Header.Get("Proxy-Status") != proxyStatus {
		t.Fatalf("Proxy-Status %q, want %q", r.Header.Get("Proxy-Status"), proxyStatus)
	}
}

func odohHasAnswer(m *dns.Msg) bool {
	if m == nil || m.Rcode != dns.RcodeSuccess || len(m.Answer) != 1 {
		return false
	}
	a, ok := m.Answer[0].(*dns.A)
	return ok && a.A.String() == "192.0.2.10"
}

func odohAnswer(t *testing.T, ctx context.Context, hc *http.Client, endpoint string, c harness.ODoHConfig, name string, target bool, ps string) {
	t.Helper()
	m, r, e := harness.ODoHQuery(ctx, hc, endpoint, c, question(name, dns.TypeA))
	odohStatus(t, r, e, 200, ps)
	if !odohHasAnswer(m) || m.Id != 0 || !m.Response || len(m.Question) != 1 || m.Question[0].Name != name {
		t.Fatalf("ODoH answer: %v", m)
	}
	if r.Header.Get("Content-Type") != odohMediaType {
		t.Fatalf("ODoH response media type %q", r.Header.Get("Content-Type"))
	}
	if target && r.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("target response missing Cache-Control: no-store")
	}
}

func odohWaitConfigs(t *testing.T, ctx context.Context, hc *http.Client, base string, n int) []harness.ODoHConfig {
	t.Helper()
	var configs []harness.ODoHConfig
	harness.Eventually(t, 30*time.Second, func() error {
		var r *http.Response
		var err error
		configs, r, err = harness.FetchODoHConfigs(ctx, hc, base)
		if err != nil {
			return err
		}
		if r.StatusCode != 200 || len(configs) != n {
			return fmt.Errorf("config status %d, count %d, want 200/%d", r.StatusCode, len(configs), n)
		}
		if r.Header.Get("Content-Type") != "application/octet-stream" {
			return fmt.Errorf("config content type %q", r.Header.Get("Content-Type"))
		}
		for _, c := range configs {
			if c.KemID != 0x20 || c.KdfID != 1 || c.AeadID != 1 || len(c.PublicKey) != 32 {
				return fmt.Errorf("unexpected HPKE suite or public key length")
			}
		}
		return nil
	})
	return configs
}
