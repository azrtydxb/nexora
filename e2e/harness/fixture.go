package harness

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

var fixtureReady = regexp.MustCompile(`fixture ready`)

// OIDCUser is a user the OIDC fixture can sign in.
type OIDCUser struct {
	Username string   `json:"username"`
	Email    string   `json:"email"`
	Groups   []string `json:"groups"`
}

// OIDCFixture is a running minimal OIDC provider.
type OIDCFixture struct {
	Issuer, ClientID, ClientSecretFile string
	Proc                               *Proc
}

// StartOIDCFixture starts `nexora-fixture oidc` for users with client id `nexora` and a random
// client secret written to a file.
func (e *Env) StartOIDCFixture(users ...OIDCUser) *OIDCFixture {
	e.T.Helper()
	dir, err := os.MkdirTemp(e.Dir, "oidc-")
	if err != nil {
		e.T.Fatal(err)
	}
	usersJSON, err := json.Marshal(users)
	if err != nil {
		e.T.Fatal(err)
	}
	usersFile := filepath.Join(dir, "users.json")
	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	secretFile := filepath.Join(dir, "client-secret")
	if err := os.WriteFile(usersFile, usersJSON, 0o600); err != nil {
		e.T.Fatal(err)
	}
	if err := os.WriteFile(secretFile, []byte(hex.EncodeToString(secret)), 0o600); err != nil {
		e.T.Fatal(err)
	}
	listen := fmt.Sprintf("127.0.0.1:%d", e.FreePort())
	fx := &OIDCFixture{Issuer: "http://" + listen, ClientID: "nexora", ClientSecretFile: secretFile}
	fx.Proc = e.Start("nexora-fixture", []string{"oidc", "--listen", listen, "--client-id", fx.ClientID,
		"--client-secret-file", secretFile, "--users-file", usersFile}, nil)
	fx.Proc.WaitLog(fixtureReady, 10*time.Second)
	return fx
}

// DNSFixture is a running `nexora-fixture dns` upstream. UDP, TCP, DoT and DoH are host:port
// addresses; Control is the base URL of its JSON control API.
type DNSFixture struct {
	UDP, TCP, DoT, DoH, Control, CACertPEM, TLSName string
}

// StartDNSFixture starts `nexora-fixture dns` on five free loopback ports with a fresh CA.
func (e *Env) StartDNSFixture() *DNSFixture {
	e.T.Helper()
	certDir, err := os.MkdirTemp(e.Dir, "fixture-")
	if err != nil {
		e.T.Fatal(err)
	}
	addr := func() string { return fmt.Sprintf("127.0.0.1:%d", e.FreePort()) }
	fx := &DNSFixture{UDP: addr(), TCP: addr(), DoT: addr(), DoH: addr(), TLSName: "fixture.nexora.test"}
	control := addr()
	fx.Control = "http://" + control
	p := e.Start("nexora-fixture", []string{"dns", "--udp", fx.UDP, "--tcp", fx.TCP, "--dot", fx.DoT,
		"--doh", fx.DoH, "--control", control, "--cert-dir", certDir}, nil)
	p.WaitLog(fixtureReady, 10*time.Second)
	ca, err := os.ReadFile(filepath.Join(certDir, "ca.pem"))
	if err != nil {
		e.T.Fatal(err)
	}
	fx.CACertPEM = string(ca)
	return fx
}

type dnsStats struct {
	Total   int            `json:"total"`
	Queries map[string]int `json:"queries"`
}

func (f *DNSFixture) stats(t *testing.T) dnsStats {
	t.Helper()
	var s dnsStats
	fixtureCall(t, http.MethodGet, f.Control+"/stats", nil, &s)
	return s
}

// Count returns how many queries for name (case-insensitive, with or without the trailing dot)
// and qtype the fixture has received.
func (f *DNSFixture) Count(t *testing.T, name string, qtype uint16) int {
	t.Helper()
	return f.stats(t).Queries[fmt.Sprintf("%s|%d", strings.ToLower(dns.Fqdn(name)), qtype)]
}

// Total returns the number of queries the fixture has received.
func (f *DNSFixture) Total(t *testing.T) int {
	t.Helper()
	return f.stats(t).Total
}

// SetMode switches the fixture to normal, blackhole (never answers) or servfail.
func (f *DNSFixture) SetMode(t *testing.T, mode string) {
	t.Helper()
	fixtureCall(t, http.MethodPost, f.Control+"/mode", map[string]string{"mode": mode}, nil)
}

// SetDelay delays every answer by d.
func (f *DNSFixture) SetDelay(t *testing.T, d time.Duration) {
	t.Helper()
	fixtureCall(t, http.MethodPost, f.Control+"/delay", map[string]int64{"ms": d.Milliseconds()}, nil)
}

// Reset clears the counters, mode and delay.
func (f *DNSFixture) Reset(t *testing.T) {
	t.Helper()
	fixtureCall(t, http.MethodPost, f.Control+"/reset", nil, nil)
}

// HTTPFixture is a running `nexora-fixture http` blocklist file server.
type HTTPFixture struct {
	Base string
}

// StartHTTPFixture starts `nexora-fixture http` on a free loopback port.
func (e *Env) StartHTTPFixture() *HTTPFixture {
	e.T.Helper()
	listen := fmt.Sprintf("127.0.0.1:%d", e.FreePort())
	e.Start("nexora-fixture", []string{"http", "--listen", listen}, nil).WaitLog(fixtureReady, 10*time.Second)
	return &HTTPFixture{Base: "http://" + listen}
}

// URL is the download URL of list name.
func (f *HTTPFixture) URL(name string) string {
	return f.Base + "/lists/" + url.PathEscape(name)
}

// SetList stores body as list name.
func (f *HTTPFixture) SetList(t *testing.T, name, body string) {
	t.Helper()
	fixtureRaw(t, http.MethodPut, f.URL(name), strings.NewReader(body), nil)
}

// SetFailing makes downloads of list name answer 500 (or recover).
func (f *HTTPFixture) SetFailing(t *testing.T, name string, failing bool) {
	t.Helper()
	fixtureCall(t, http.MethodPost, f.URL(name)+"/fail", map[string]bool{"failing": failing}, nil)
}

// Hits returns how many times list name was requested.
func (f *HTTPFixture) Hits(t *testing.T, name string) int {
	t.Helper()
	var out struct {
		Hits int `json:"hits"`
	}
	fixtureCall(t, http.MethodGet, f.Base+"/hits/"+url.PathEscape(name), nil, &out)
	return out.Hits
}

// fixtureCall sends body as JSON (when non-nil) and decodes a JSON reply into out (when non-nil).
func fixtureCall(t *testing.T, method, target string, body, out any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	fixtureRaw(t, method, target, r, out)
}

func fixtureRaw(t *testing.T, method, target string, body io.Reader, out any) {
	t.Helper()
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := fixtureClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("%s %s: %s: %s", method, target, resp.Status, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s %s: decode %q: %v", method, target, data, err)
		}
	}
}

var fixtureClient = &http.Client{Timeout: 5 * time.Second}
