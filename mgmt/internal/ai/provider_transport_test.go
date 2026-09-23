package ai

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	sdk "github.com/azrtydxb/go-ai-sdk/ai"
	"github.com/azrtydxb/go-ai-sdk/providers/openai"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/piwi3910/nexora/mgmt/internal/config"
)

func TestProviderRejectsRedirect(t *testing.T) {
	var contacted atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { contacted.Add(1); w.WriteHeader(500) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	m := NewModel(config.AIConfig{BaseURL: origin.URL, Model: "test"})
	_, err := m.Generate(context.Background(), provider.Call{Messages: []provider.Message{provider.UserText("private DNS data")}})
	if err == nil || contacted.Load() != 0 {
		t.Fatalf("redirect error=%v target contacts=%d; want rejection before forwarding", err, contacted.Load())
	}
}

// pipeDial runs a real HTTP/1 transport without requiring permission to bind sockets.
func pipeDial(t *testing.T, reply string, calls *atomic.Int32, address *string) func(context.Context, string, string) (net.Conn, error) {
	t.Helper()
	return func(_ context.Context, _, addr string) (net.Conn, error) {
		calls.Add(1)
		*address = addr
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			req, err := http.ReadRequest(bufio.NewReader(server))
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, req.Body)
			_ = req.Body.Close()
			_, _ = io.WriteString(server, reply)
		}()
		return client, nil
	}
}

func TestProviderTransportPinsAndRechecks(t *testing.T) {
	var resolutions, contacts atomic.Int32
	address := ""
	resolve := func(context.Context, string) ([]netip.Addr, error) {
		if resolutions.Add(1) == 1 {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	c := providerClient(false, resolve, pipeDial(t, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", &contacts, &address))
	r, err := c.Get("http://private.test:1234/v1")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if address != "127.0.0.1:1234" || resolutions.Load() != 1 {
		t.Fatalf("dialled %s, resolutions=%d", address, resolutions.Load())
	}
	_, err = c.Get("http://private.test:1234/v1")
	if !errors.Is(err, ErrEndpointNotPrivate) || contacts.Load() != 1 {
		t.Fatalf("rebind error=%v contacts=%d", err, contacts.Load())
	}
}

func TestProviderTransportRedirectAndMixedDNS(t *testing.T) {
	for _, public := range []bool{false, true} {
		var contacts atomic.Int32
		address := ""
		c := providerClient(public, nil, pipeDial(t, "HTTP/1.1 307 Temporary Redirect\r\nLocation: http://8.8.8.8/stolen\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", &contacts, &address))
		_, err := c.Post("http://127.0.0.1/v1", "application/json", strings.NewReader("private data"))
		if err == nil || contacts.Load() != 1 {
			t.Fatalf("allowPublic=%v redirect err=%v contacts=%d", public, err, contacts.Load())
		}
	}
	var contacts atomic.Int32
	address := ""
	c := providerClient(false, fakeResolve(map[string][]string{"mixed.test": {"127.0.0.1", "8.8.8.8"}}), pipeDial(t, "", &contacts, &address))
	_, err := c.Get("http://mixed.test/v1")
	if !errors.Is(err, ErrEndpointNotPrivate) || contacts.Load() != 0 {
		t.Fatalf("mixed DNS: %v contacts=%d", err, contacts.Load())
	}
}

func TestProviderSDKRetryRechecksAddress(t *testing.T) {
	var resolutions, contacts atomic.Int32
	address := ""
	resolve := func(context.Context, string) ([]netip.Addr, error) {
		if resolutions.Add(1) == 1 {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	c := providerClient(false, resolve, pipeDial(t, "HTTP/1.1 503 Service Unavailable\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", &contacts, &address))
	m := openai.New(openai.WithBaseURL("http://private.test/v1"), openai.WithAPIKey(""), openai.WithHTTPClient(c)).Model("test")
	retries := 2
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := sdk.GenerateObject[budgetAnswer](ctx, sdk.GenerateObjectOpts{Model: m, Messages: []provider.Message{provider.UserText("private data")}, MaxRetries: &retries})
	if err == nil || resolutions.Load() != 2 || contacts.Load() != 1 {
		t.Fatalf("retry err=%v resolutions=%d contacts=%d", err, resolutions.Load(), contacts.Load())
	}
}

func TestProviderTLSVerification(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"private.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	for _, tc := range []struct {
		name, host     string
		trust, success bool
	}{{"trusted-hostname", "private.test", true, true}, {"wrong-hostname", "other.test", true, false}, {"untrusted-cert", "private.test", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			var contacted atomic.Int32
			sni := make(chan string, 1)
			dial := func(_ context.Context, _, address string) (net.Conn, error) {
				if address != "127.0.0.1:443" {
					t.Errorf("unclassified dial: %s", address)
				}
				client, server := net.Pipe()
				go func() {
					defer server.Close()
					conn := tls.Server(server, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) { sni <- hello.ServerName; return nil, nil }})
					if conn.Handshake() != nil {
						return
					}
					req, err := http.ReadRequest(bufio.NewReader(conn))
					if err != nil {
						return
					}
					req.Body.Close()
					contacted.Add(1)
					io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
				}()
				return client, nil
			}
			c := providerClient(false, fakeResolve(map[string][]string{tc.host: {"127.0.0.1"}}), dial)
			tr := c.Transport.(*http.Transport)
			if tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
				t.Fatal("TLS bypass")
			}
			if tc.trust {
				tr.TLSClientConfig = &tls.Config{RootCAs: roots}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+tc.host+"/v1", nil)
			resp, err := c.Do(req)
			if resp != nil {
				resp.Body.Close()
			}
			if (err == nil) != tc.success {
				t.Fatalf("TLS error=%v want success=%v", err, tc.success)
			}
			if !tc.success && contacted.Load() != 0 {
				t.Fatal("sent HTTP data before certificate validation")
			}
			select {
			case name := <-sni:
				if name != tc.host {
					t.Fatalf("SNI=%s want %s", name, tc.host)
				}
			case <-ctx.Done():
				t.Fatal("missing TLS handshake")
			}
		})
	}
}

func TestProviderTransportOwnsTLSAndProxyPolicy(t *testing.T) {
	old := http.DefaultTransport
	http.DefaultTransport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, Proxy: http.ProxyFromEnvironment}
	defer func() { http.DefaultTransport = old }()
	tr := providerClient(false, nil, nil).Transport.(*http.Transport)
	if tr.Proxy != nil || (tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify) || tr.DialTLSContext != nil {
		t.Fatal("AI transport inherited an unchecked global transport policy")
	}
}

func TestDisabledServiceDoesNotResolve(t *testing.T) {
	svc, reason, err := New(context.Background(), Options{Config: config.AIConfig{}, Resolve: func(context.Context, string) ([]netip.Addr, error) {
		t.Fatal("disabled service resolved an endpoint")
		return nil, nil
	}})
	if svc != nil || reason != "not_configured" || err != nil {
		t.Fatalf("service=%v reason=%s err=%v", svc, reason, err)
	}
}
