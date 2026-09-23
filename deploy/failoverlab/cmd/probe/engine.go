package main

// Disposable standalone engine material and an actual OTLP receiver. Nothing here
// enrolls an engine or reads production identities, tokens, or trust.
import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	logs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func labTLS(dir string) error {
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "dsr-lab.test"}, DNSNames: []string{"dsr-lab.test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		return err
	}
	raw, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}), 0600); err != nil {
		return err
	}
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err = os.WriteFile(filepath.Join(dir, "cert.pem"), certificate, 0600); err != nil {
		return err
	}
	digest := sha256.Sum256(certificate)
	return os.WriteFile(filepath.Join(dir, "lab-trust.sha256"), []byte(fmt.Sprintf("nexora-disposable-failover-lab-v1 %x\n", digest)), 0600)
}

// A marker binds this fixture's public certificate to the explicit lab generator.
// Check it before reading the private key; never adopt an arbitrary trust directory.
func checkLabTrust(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("absolute lab trust directory required")
	}
	marker, err := os.ReadFile(filepath.Join(dir, "lab-trust.sha256"))
	if err != nil {
		return err
	}
	cert, err := os.ReadFile(filepath.Join(dir, "cert.pem"))
	if err != nil {
		return err
	}
	digest := sha256.Sum256(cert)
	if !bytes.Equal(marker, []byte(fmt.Sprintf("nexora-disposable-failover-lab-v1 %x\n", digest))) {
		return fmt.Errorf("not matching generated lab trust")
	}
	return nil
}

func engineSnapshot(group string) (*controlv1.ConfigSnapshot, error) {
	if group != "g1" && group != "g2" {
		return nil, fmt.Errorf("invalid group %q", group)
	}
	s := &controlv1.ConfigSnapshot{
		Version: 1, Resolver: &controlv1.ResolverConfig{Strategy: controlv1.UpstreamStrategy_UPSTREAM_STRATEGY_ORDERED},
		Cache:         &controlv1.CacheConfig{MaxBytes: 64 << 20, MaxTtl: 86400, NegativeMaxTtl: 3600, StaleWindow: 86400},
		Upstreams:     []*controlv1.Upstream{{Id: "lab-signed", Name: "lab-signed", Protocol: controlv1.UpstreamProtocol_UPSTREAM_PROTOCOL_TCP, Address: "198.19.1.2:5353", TimeoutMs: 2000}},
		AclAllowCidrs: []string{"198.18.0.0/24"},
		Filter:        &controlv1.FilterConfig{BlockMode: controlv1.BlockMode_BLOCK_MODE_NULL_IP, BlockTtl: 60},
		Telemetry:     &controlv1.TelemetryConfig{OtlpEndpoint: "http://198.19.1.2:4317"},
	}
	for _, client := range []string{"10", "11"} {
		id := group + "-client-" + client
		s.PolicyGroups = append(s.PolicyGroups, &controlv1.PolicyGroup{Id: id, Name: id, Cidrs: []string{"198.18.0." + client + "/32"}, RewriteSetIds: []string{id}})
		s.RewriteSets = append(s.RewriteSets, &controlv1.RewriteSet{Id: id, Label: id, Rules: []*controlv1.RewriteRule{{Name: "*." + group + ".dsr-lab.test", Type: controlv1.RewriteType_REWRITE_TYPE_A, Value: "203.0." + group[1:] + "." + client, Ttl: 0}}})
	}
	return s, nil
}

func prepareEngine(dir, tlsDir, group, node string) error {
	if err := checkLabTrust(tlsDir); err != nil {
		return err
	}
	s, err := engineSnapshot(group)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(dir) || !filepath.IsAbs(tlsDir) {
		return fmt.Errorf("absolute disposable directories required")
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		return err
	}
	for _, sub := range []string{"state", "blobs"} {
		if err = os.Mkdir(filepath.Join(dir, sub), 0700); err != nil {
			return err
		}
	}
	raw, err := proto.Marshal(s)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "snapshot.binpb"), raw, 0600); err != nil {
		return err
	}
	config := fmt.Sprintf(`node_name = %q
state_dir = %q
standalone_snapshot = %q
standalone_blob_dir = %q
listen_udp = ["198.18.0.100:53"]
listen_tcp = ["198.18.0.100:53"]
listen_dot = ["198.18.0.100:853"]
listen_doh = ["198.18.0.100:443"]
listen_doq = ["198.18.0.100:853"]
metrics_listen = "198.19.1.1:9153"
tls_cert_file = %q
tls_key_file = %q
workers = 2
`, node, filepath.Join(dir, "state"), filepath.Join(dir, "snapshot.binpb"), filepath.Join(dir, "blobs"), filepath.Join(tlsDir, "cert.pem"), filepath.Join(tlsDir, "key.pem"))
	return os.WriteFile(filepath.Join(dir, "engine.toml"), []byte(config), 0600)
}

type logCollector struct {
	logs.UnimplementedLogsServiceServer
	mu   sync.Mutex
	file *os.File
}

func (c *logCollector) Export(_ context.Context, req *logs.ExportLogsServiceRequest) (*logs.ExportLogsServiceResponse, error) {
	// Keep the wire-level OTLP resource/attributes, not a synthesized echo log.
	raw, err := protojson.Marshal(req)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err = c.file.Write(append(raw, '\n')); err != nil {
		return nil, err
	}
	if err = c.file.Sync(); err != nil {
		return nil, err
	}
	return &logs.ExportLogsServiceResponse{}, nil
}
func collectEngine(path, tlsDir string) error {
	if os.Getenv("FAILOVER_CROSSHOST_ENABLE") != "yes" || os.Getenv("FAILOVER_REAL_ENGINE_ENABLE") != "yes" {
		return fmt.Errorf("disposable cross-host engine collector requires both lab opt-ins")
	}
	if err := checkLabTrust(tlsDir); err != nil {
		return err
	}
	if err := serveSigned(tlsDir); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	l, err := net.Listen("tcp", "198.19.1.2:4317")
	if err != nil {
		return err
	}
	// debt: plaintext OTLP is confined to the disposable, no-default-route mg0
	// veth and synthetic query evidence; require authenticated TLS before any
	// non-lab exposure. The lab supervisor owns both endpoints of this link.
	s := grpc.NewServer() // nosemgrep: grpc-server-insecure-connection
	logs.RegisterLogsServiceServer(s, &logCollector{file: f})
	fmt.Println("READY collector=198.19.1.2:4317")
	return s.Serve(l)
}

// evidenceJSON keeps each successful acceptance result independently parseable.
func evidenceJSON(v any) { b, err := json.Marshal(v); must(err); fmt.Println(string(b)) }

func checkEngineReply(r *dns.Msg, client string) error {
	if r == nil || len(r.Question) != 1 || len(r.Answer) != 1 {
		return fmt.Errorf("invalid engine reply")
	}
	labels := strings.Split(r.Question[0].Name, ".")
	if len(labels) != 6 || (labels[2] != "g1" && labels[2] != "g2") {
		return fmt.Errorf("invalid question contract")
	}
	ip := net.ParseIP(client).To4()
	if ip == nil {
		return fmt.Errorf("invalid client")
	}
	want := fmt.Sprintf("203.0.%s.%d", labels[2][1:], ip[3])
	a, ok := r.Answer[0].(*dns.A)
	if !ok || a.Hdr.Name != r.Question[0].Name || a.A.String() != want {
		return fmt.Errorf("engine policy mismatch: %v expected %s", r.Answer, want)
	}
	return nil
}
