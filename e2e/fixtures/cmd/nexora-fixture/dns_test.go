package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestDNSFixtureBehaviours(t *testing.T) {
	dir := t.TempDir()
	const port0 = "127.0.0.1:0"
	fx, err := startDNSFixture(dnsConfig{UDP: port0, TCP: port0, DoT: port0, DoH: port0, Control: port0, CertDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer fx.Close()
	cfg := fx.bound
	if cfg.UDP != cfg.TCP || strings.HasSuffix(cfg.UDP, ":0") {
		t.Fatalf("UDP %s and TCP %s must share one kernel-chosen port", cfg.UDP, cfg.TCP)
	}
	for _, a := range []string{cfg.DoT, cfg.DoH, cfg.Control} {
		if a == "" || strings.HasSuffix(a, ":0") {
			t.Fatalf("unresolved listen address %q in %+v", a, cfg)
		}
	}

	c := &dns.Client{Net: "udp", Timeout: time.Second}
	m := new(dns.Msg)
	m.SetQuestion("hello.example.", dns.TypeA)
	r, _, err := c.Exchange(m, cfg.UDP)
	if err != nil || len(r.Answer) != 1 || r.Answer[0].Header().Ttl != 300 {
		t.Fatalf("default answer: %v %v", r, err)
	}
	m.SetQuestion("tc-1.example.", dns.TypeA)
	if r, _, _ = c.Exchange(m, cfg.UDP); !r.Truncated {
		t.Fatal("tc-* over UDP must be truncated")
	}
	tcp := &dns.Client{Net: "tcp", Timeout: time.Second}
	if r, _, _ = tcp.Exchange(m, cfg.TCP); len(r.Answer) != 100 {
		t.Fatalf("tc-* over TCP answers = %d", len(r.Answer))
	}
	m.SetQuestion("nx-1.example.", dns.TypeA)
	if r, _, _ = c.Exchange(m, cfg.UDP); r.Rcode != dns.RcodeNameError || len(r.Ns) != 1 {
		t.Fatalf("nx-*: %v", r)
	}

	pem, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	dot := &dns.Client{Net: "tcp-tls", Timeout: time.Second, TLSConfig: &tls.Config{RootCAs: pool, ServerName: "fixture.nexora.test"}}
	m.SetQuestion("dot.example.", dns.TypeA)
	if r, _, err = dot.Exchange(m, cfg.DoT); err != nil || len(r.Answer) != 1 {
		t.Fatalf("dot: %v %v", r, err)
	}
	wire, _ := m.Pack()
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "fixture.nexora.test"}, ForceAttemptHTTP2: true}}
	resp, err := hc.Post("https://"+cfg.DoH+"/dns-query", "application/dns-message", bytes.NewReader(wire))
	if err != nil || resp.StatusCode != 200 || resp.ProtoMajor != 2 {
		t.Fatalf("doh: %v %v", resp, err)
	}
	body, _ := io.ReadAll(resp.Body)
	var dm dns.Msg
	if err := dm.Unpack(body); err != nil || len(dm.Answer) != 1 {
		t.Fatalf("doh body: %v", err)
	}

	if n := fx.count("hello.example.", dns.TypeA); n != 1 {
		t.Fatalf("count = %d", n)
	}
	fx.setMode("blackhole")
	m.SetQuestion("hole.example.", dns.TypeA)
	if _, _, err = (&dns.Client{Net: "udp", Timeout: 300 * time.Millisecond}).Exchange(m, cfg.UDP); err == nil {
		t.Fatal("blackhole answered")
	}
}

func TestDNSFixtureStaticRecords(t *testing.T) {
	const port0 = "127.0.0.1:0"
	fx, err := startDNSFixture(dnsConfig{UDP: port0, TCP: port0, DoT: port0, DoH: port0, Control: port0, CertDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer fx.Close()
	post := func(body string) int {
		resp, err := http.Post("http://"+fx.bound.Control+"/records", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post(`{"rrs": ["Static.Example. 60 IN A 192.0.2.10", "static.example. 60 IN A 192.0.2.11"]}`); code != http.StatusNoContent {
		t.Fatalf("POST /records = %d", code)
	}
	if code := post(`{"rrs": ["not a record"]}`); code != http.StatusBadRequest {
		t.Fatalf("bad record = %d, want 400", code)
	}
	ask := func(name string, qtype uint16) *dns.Msg {
		m := new(dns.Msg)
		m.SetQuestion(name, qtype)
		r, _, err := (&dns.Client{Net: "udp", Timeout: time.Second}).Exchange(m, fx.bound.UDP)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := ask("STATIC.example.", dns.TypeA); len(r.Answer) != 2 || r.Answer[0].Header().Name != "STATIC.example." || !r.RecursionAvailable || r.Authoritative {
		t.Fatalf("static A: %v", r)
	}
	if r := ask("static.example.", dns.TypeAAAA); r.Rcode != dns.RcodeSuccess || len(r.Answer) != 0 {
		t.Fatalf("static name, other type must be NODATA: %v", r)
	}
	if code := post(`{"rrs": ["static.example. 60 IN AAAA 2001:db8::10"]}`); code != http.StatusNoContent {
		t.Fatalf("replace = %d", code)
	}
	if r := ask("static.example.", dns.TypeA); len(r.Answer) != 0 {
		t.Fatalf("records for a name must be replaced: %v", r)
	}
	if r := ask("other.example.", dns.TypeA); len(r.Answer) != 1 || r.Answer[0].(*dns.A).A.String() != "192.0.2.1" {
		t.Fatalf("names without records keep the default answer: %v", r)
	}
}
