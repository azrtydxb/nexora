package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/miekg/dns"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	logs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	logv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestDisposableEngineMaterial(t *testing.T) {
	dir := t.TempDir()
	trust := filepath.Join(dir, "tls")
	if err := labTLS(trust); err != nil {
		t.Fatal(err)
	}
	if err := labTLS(trust); err == nil {
		t.Fatal("reused trust directory")
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(trust, "cert.pem"), filepath.Join(trust, "key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = cert.VerifyHostname("dsr-lab.test"); err != nil {
		t.Fatal(err)
	}
	for _, group := range []string{"g1", "g2"} {
		path := filepath.Join(dir, group)
		if err = prepareEngine(path, trust, group, "fx-lab-"+group+"-a"); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(path, "snapshot.binpb"))
		if err != nil {
			t.Fatal(err)
		}
		snap := new(controlv1.ConfigSnapshot)
		if err = proto.Unmarshal(raw, snap); err != nil {
			t.Fatal(err)
		}
		if snap.Version != 1 || len(snap.PolicyGroups) != 2 || snap.Telemetry.OtlpEndpoint != "http://198.19.1.2:4317" {
			t.Fatalf("bad snapshot: %v", snap)
		}
		for i, ip := range []string{"10", "11"} {
			policy := snap.PolicyGroups[i]
			rule := snap.RewriteSets[i].Rules[0]
			if policy.Cidrs[0] != "198.18.0."+ip+"/32" || rule.Value != "203.0."+group[1:]+"."+ip || policy.RewriteSetIds[0] != snap.RewriteSets[i].Id {
				t.Fatal("client/group policy not distinct")
			}
		}
		config, _ := os.ReadFile(filepath.Join(path, "engine.toml"))
		for _, required := range []string{"standalone_snapshot", "listen_udp", "listen_tcp", "listen_dot", "listen_doh", "listen_doq", "198.19.1.1:9153"} {
			if !strings.Contains(string(config), required) {
				t.Fatalf("missing %s", required)
			}
		}
		if strings.Contains(string(config), "management_urls") || strings.Contains(string(config), "join_token") {
			t.Fatal("managed configuration leaked")
		}
		if err = prepareEngine(path, trust, group, "fx-lab"); err == nil {
			t.Fatal("overwrote existing engine state")
		}
	}
	if _, err = engineSnapshot("g3"); err == nil {
		t.Fatal("accepted foreign group")
	}
}

func TestEnginePolicyAcceptanceRejectsSourceAndGroupLoss(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("udp.r0.g1.dsr-lab.test.", dns.TypeA)
	r := new(dns.Msg)
	r.SetReply(q)
	r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.ParseIP("203.0.1.10")}}
	if err := checkEngineReply(r, "198.18.0.10"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"203.0.1.11", "203.0.2.10", "198.18.0.10"} {
		r.Answer[0].(*dns.A).A = net.ParseIP(bad)
		if checkEngineReply(r, "198.18.0.10") == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestSignedPayloadAndTruncationAcceptance(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")
	if err := labTLS(dir); err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := os.ReadFile(filepath.Join(dir, "cert.pem"))
	q := new(dns.Msg)
	q.SetQuestion("tcp.r0.g1.mtu.test.", dns.TypeTXT)
	q.SetEdns0(1232, true)
	r, err := signedAnswer(q, pair.PrivateKey.(*ecdsa.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	for _, transport := range []string{"tcp", "dot", "doh", "doq"} {
		if err = checkSignedReply(r, transport, cert); err != nil {
			t.Fatal(err)
		}
	}
	if checkSignedReply(r, "udp", cert) == nil {
		t.Fatal("accepted oversized untruncated UDP")
	}
	truncated := r.Copy()
	truncated.Truncate(1232)
	if err = checkSignedReply(truncated, "udp", cert); err != nil {
		t.Fatal(err)
	}
	if checkSignedReply(truncated, "tcp", cert) == nil {
		t.Fatal("accepted truncated stream")
	}
	r.Answer[0].(*dns.TXT).Txt[0] = "tampered"
	if checkSignedReply(r, "tcp", cert) == nil {
		t.Fatal("accepted corrupted signed payload")
	}
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	r, err = signedAnswer(q, other)
	if err != nil {
		t.Fatal(err)
	}
	if checkSignedReply(r, "tcp", cert) == nil {
		t.Fatal("accepted untrusted signature")
	}
}

func TestCollectorRequiresBothLabOptInsBeforeSideEffects(t *testing.T) {
	for _, values := range [][2]string{{"", ""}, {"yes", ""}, {"", "yes"}} {
		t.Run(strings.Join(values[:], "-"), func(t *testing.T) {
			t.Setenv("FAILOVER_CROSSHOST_ENABLE", values[0])
			t.Setenv("FAILOVER_REAL_ENGINE_ENABLE", values[1])
			path := filepath.Join(t.TempDir(), "querylog.jsonl")
			if err := collectEngine(path, "/missing-lab-trust"); err == nil || !strings.Contains(err.Error(), "requires both lab opt-ins") {
				t.Fatalf("collector opt-in: %v", err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("collector created evidence before opt-in: %v", err)
			}
		})
	}
}

func TestCollectorPersistsActualOTLPAndFailsOnWrite(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "otlp")
	if err != nil {
		t.Fatal(err)
	}
	c := &logCollector{file: f}
	request := &logs.ExportLogsServiceRequest{ResourceLogs: []*logv1.ResourceLogs{{ScopeLogs: []*logv1.ScopeLogs{{LogRecords: []*logv1.LogRecord{{Attributes: []*common.KeyValue{{Key: "client.address", Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: "198.18.0.10"}}}}}}}}}}}
	if _, err = c.Export(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	actual := new(logs.ExportLogsServiceRequest)
	if err = protojson.Unmarshal(raw, actual); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(request, actual) {
		t.Fatal("collector altered OTLP")
	}
	f.Close()
	if _, err = c.Export(context.Background(), request); err == nil {
		t.Fatal("acknowledged lost evidence")
	}
}

func TestRejectNonLabOrReplacedTrust(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")
	if err := labTLS(dir); err != nil {
		t.Fatal(err)
	}
	if err := checkLabTrust(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if checkLabTrust(dir) == nil {
		t.Fatal("accepted replaced trust")
	}
	if checkLabTrust(t.TempDir()) == nil {
		t.Fatal("adopted existing unmarked trust")
	}
}
