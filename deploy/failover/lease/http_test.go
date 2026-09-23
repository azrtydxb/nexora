package lease

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A byte-stream listener avoids sandbox-forbidden TCP bind while running the
// real HTTP transport, TLS handshake, certificates and server end to end.
type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *pipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 443} }
func (l *pipeListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	a, b := net.Pipe()
	select {
	case l.conns <- b:
		return a, nil
	case <-ctx.Done():
		a.Close()
		b.Close()
		return nil, ctx.Err()
	case <-l.done:
		a.Close()
		b.Close()
		return nil, net.ErrClosed
	}
}

func document() map[string]any {
	return map[string]any{"apiVersion": "coordination.k8s.io/v1", "kind": "Lease",
		"metadata": map[string]any{"namespace": "test", "name": "frontend", "uid": "uid", "resourceVersion": "opaque-1", "generation": 1,
			"annotations": map[string]any{annotation + "protocol": Protocol, annotation + "nonce": strings.Repeat("a", 64), annotation + "epoch": "0", "unrelated": "preserved"}},
		"spec": map[string]any{"holderIdentity": "", "leaseDurationSeconds": 1, "renewTime": "2099-01-01T00:00:00Z"}}
}
func tlsAuthority(t *testing.T, h http.HandlerFunc) (*HTTPSAuthority, *httptest.Server) {
	t.Helper()
	l := &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
	s := &httptest.Server{Listener: l, Config: &http.Server{Handler: h}}
	s.StartTLS()
	t.Cleanup(s.Close)
	roots := x509.NewCertPool()
	roots.AddCert(s.Certificate())
	a, e := NewHTTPS(HTTPSConfig{Endpoint: s.URL, Namespace: "test", Name: "frontend", Token: "test-only-credential", Roots: roots, Timeout: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	a.client.Transport.(*http.Transport).DialContext = l.dial
	return a, s
}
func TestTLSExactCAS(t *testing.T) {
	var calls atomic.Int32
	a, _ := tlsAuthority(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/apis/coordination.k8s.io/v1/namespaces/test/leases/frontend" || r.Header.Get("Authorization") != "Bearer test-only-credential" {
			t.Error("target or auth mismatch")
		}
		m := document()
		if r.Method == http.MethodPut {
			b, _ := io.ReadAll(r.Body)
			if e := json.Unmarshal(b, &m); e != nil {
				t.Error(e)
			}
			meta := m["metadata"].(map[string]any)
			anns := meta["annotations"].(map[string]any)
			if meta["uid"] != "uid" || meta["resourceVersion"] != "opaque-1" || anns["unrelated"] != "preserved" || anns[annotation+"epoch"] != "1" || anns[annotation+"nonce"] == strings.Repeat("a", 64) {
				t.Error("CAS preconditions/mutation missing")
			}
			meta["resourceVersion"] = "opaque-2"
		} else if r.Method != http.MethodGet {
			t.Error("unsupported method")
		}
		_ = json.NewEncoder(w).Encode(m)
	})
	r, e := a.Get(ctx)
	if e != nil {
		t.Fatal(e)
	}
	n := r
	n.Holder = strings.Repeat("b", 64)
	n.Nonce = strings.Repeat("c", 64)
	n.Epoch++
	out, e := a.CAS(ctx, r, n)
	if e != nil || out.RV != "opaque-2" || calls.Load() != 2 {
		t.Fatalf("CAS: %v %v", out, e)
	}
}
func TestTLSCertificateValidation(t *testing.T) {
	trusted, s := tlsAuthority(t, func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted request reached handler") })
	a, e := NewHTTPS(HTTPSConfig{Endpoint: s.URL, Namespace: "test", Name: "frontend", Token: "secret-not-for-errors", Timeout: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	a.client.Transport.(*http.Transport).DialContext = trusted.client.Transport.(*http.Transport).DialContext
	_, e = a.Get(ctx)
	if e == nil || strings.Contains(e.Error(), "secret-not-for-errors") {
		t.Fatal("certificate or redaction failure")
	}
}
func TestHTTPFailures(t *testing.T) {
	for _, mode := range []string{"redirect", "oversize", "malformed", "duplicate", "trailing", "status", "gone", "wrong-name", "wrong-uid-type", "generation-overflow", "epoch-overflow", "deleting", "unknown-protocol", "holder-type", "slow"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			a, _ := tlsAuthority(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				m := document()
				meta := m["metadata"].(map[string]any)
				switch mode {
				case "redirect":
					w.Header().Set("Location", "https://127.0.0.1:1/secret")
					w.WriteHeader(307)
					return
				case "oversize":
					_, _ = io.WriteString(w, strings.Repeat(" ", maxBody+1))
					return
				case "malformed":
					_, _ = io.WriteString(w, `{"metadata":`)
					return
				case "duplicate":
					_, _ = io.WriteString(w, `{"kind":"Lease","kind":"Status"}`)
					return
				case "trailing":
					_, _ = io.WriteString(w, `{} {}`)
					return
				case "status":
					w.WriteHeader(409)
					_, _ = io.WriteString(w, "test-only-credential")
					return
				case "gone":
					w.WriteHeader(404)
					return
				case "wrong-name":
					meta["name"] = "elsewhere"
				case "wrong-uid-type":
					meta["uid"] = 7
				case "generation-overflow":
					meta["generation"] = json.Number("9223372036854775808")
				case "epoch-overflow":
					meta["annotations"].(map[string]any)[annotation+"epoch"] = "18446744073709551616"
				case "deleting":
					meta["deletionTimestamp"] = "2026-01-01T00:00:00Z"
				case "unknown-protocol":
					meta["annotations"].(map[string]any)[annotation+"protocol"] = "unknown"
				case "holder-type":
					m["spec"].(map[string]any)["holderIdentity"] = 3
				case "slow":
					time.Sleep(2 * time.Second)
				}
				_ = json.NewEncoder(w).Encode(m)
			})
			if mode == "slow" {
				// Leave room for the real TLS handshake under the race detector;
				// the handler must actually run before its response times out.
				a.client.Timeout = time.Second
			}
			started := time.Now()
			_, e := a.Get(ctx)
			if mode == "slow" && time.Since(started) > 2*time.Second {
				t.Fatal("slow response exceeded the bounded request budget")
			}
			if e == nil || strings.Contains(e.Error(), "test-only-credential") || calls.Load() != 1 {
				t.Fatalf("failure not bounded/redacted: %v calls=%d", e, calls.Load())
			}
		})
	}
}
func TestHTTPNoopAndAmbiguousPUT(t *testing.T) {
	for _, mode := range []string{"no-op", "conflict", "committed-disconnect"} {
		t.Run(mode, func(t *testing.T) {
			var puts atomic.Int32
			m := document()
			a, _ := tlsAuthority(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					puts.Add(1)
					if mode == "conflict" {
						w.WriteHeader(409)
						return
					}
					if mode == "committed-disconnect" {
						if e := json.NewDecoder(r.Body).Decode(&m); e != nil {
							t.Error(e)
							return
						}
						m["metadata"].(map[string]any)["resourceVersion"] = "committed-2"
						conn, _, e := w.(http.Hijacker).Hijack()
						if e != nil {
							t.Error(e)
							return
						}
						_ = conn.Close()
						return
					}
				}
				_ = json.NewEncoder(w).Encode(m)
			})
			r, e := a.Get(ctx)
			if e != nil {
				t.Fatal(e)
			}
			n := r
			n.Holder = strings.Repeat("b", 64)
			n.Nonce = strings.Repeat("c", 64)
			n.Epoch++
			if _, e = a.CAS(ctx, r, n); e == nil || puts.Load() != 1 {
				t.Fatalf("unsafe PUT %v count=%d", e, puts.Load())
			}
			if mode == "committed-disconnect" {
				observed, e := a.Get(ctx)
				if e != nil || observed.Nonce != n.Nonce || observed.RV != "committed-2" {
					t.Fatalf("not committed: %v", e)
				}
			}
		})
	}
}
func TestHTTPSConfigRejected(t *testing.T) {
	for _, endpoint := range []string{"http://localhost", "https://u:p@localhost", "https://localhost/base", "https://localhost?x=1", "https://localhost/#x"} {
		if _, e := NewHTTPS(HTTPSConfig{Endpoint: endpoint, Namespace: "test", Name: "frontend", Token: "x", Timeout: time.Second}); e == nil {
			t.Fatal(endpoint)
		}
	}
}
func TestRealTLSController(t *testing.T) {
	m := document()
	a, _ := tlsAuthority(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			_ = json.NewDecoder(r.Body).Decode(&m)
			m["metadata"].(map[string]any)["resourceVersion"] = "2"
		}
		_ = json.NewEncoder(w).Encode(m)
	})
	clock := &fakeClock{n: 1}
	g := &fakeGate{c: clock}
	c, e := New(a, g, clock, Config{Holder: strings.Repeat("b", 64), Margin: time.Second, IOTimeout: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	acquire(t, c, clock)
	if len(g.armed) != 1 {
		t.Fatal("no exact-ticket arm")
	}
}

func TestTLSHostnameValidation(t *testing.T) {
	a, _ := tlsAuthority(t, func(w http.ResponseWriter, r *http.Request) { t.Error("hostname mismatch reached server handler") })
	a.target = strings.Replace(a.target, "127.0.0.1", "not-in-test-certificate.invalid", 1)
	if _, e := a.Get(ctx); e == nil {
		t.Fatal("trusted CA bypassed hostname validation")
	}
}
func TestHTTPStrictJSON(t *testing.T) {
	for _, body := range [][]byte{[]byte(strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34)), {'"', 0xff, '"'}, []byte(`{"x":{"k":1,"k":2}}`), []byte(`{"x":NaN}`)} {
		if _, e := strictJSON(body); e == nil {
			t.Fatalf("invalid JSON accepted: %q", body)
		}
	}
}
func TestHTTPInvalidTargetAndToken(t *testing.T) {
	base := HTTPSConfig{Endpoint: "https://example.invalid", Namespace: "test", Name: "frontend", Token: "test-only", Timeout: time.Second}
	for _, mode := range []string{"namespace", "name", "token", "timeout"} {
		cfg := base
		switch mode {
		case "namespace":
			cfg.Namespace = "../other"
		case "name":
			cfg.Name = "a/b"
		case "token":
			cfg.Token = "x\r\ny"
		case "timeout":
			cfg.Timeout = 0
		}
		if _, e := NewHTTPS(cfg); e == nil {
			t.Fatal(mode)
		}
	}
}
