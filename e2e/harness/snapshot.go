package harness

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
)

// BaseSnapshot is a valid snapshot with ordered resolution over ups, a 64 MiB cache, loopback-only
// ACL and null-IP blocking.
func BaseSnapshot(version uint64, ups ...*controlv1.Upstream) *controlv1.ConfigSnapshot {
	return &controlv1.ConfigSnapshot{
		Version:       version,
		Resolver:      &controlv1.ResolverConfig{Strategy: controlv1.UpstreamStrategy_UPSTREAM_STRATEGY_ORDERED},
		Cache:         &controlv1.CacheConfig{MaxBytes: 64 << 20, MinTtl: 0, MaxTtl: 86400, NegativeMaxTtl: 3600, StaleWindow: 86400},
		Upstreams:     ups,
		AclAllowCidrs: []string{"127.0.0.0/8", "::1/128"},
		Filter:        &controlv1.FilterConfig{BlockMode: controlv1.BlockMode_BLOCK_MODE_NULL_IP, BlockTtl: 60},
		Telemetry:     &controlv1.TelemetryConfig{},
	}
}

// UDPUpstream is a plain DNS upstream at addr with a 250 ms timeout.
func UDPUpstream(id, addr string) *controlv1.Upstream {
	return &controlv1.Upstream{Id: id, Name: id, Protocol: controlv1.UpstreamProtocol_UPSTREAM_PROTOCOL_UDP, Address: addr, TimeoutMs: 250}
}

// DoTUpstream is a DoT upstream pointing at the fixture, trusting its CA.
func DoTUpstream(id string, f *DNSFixture) *controlv1.Upstream {
	return &controlv1.Upstream{Id: id, Name: id, Protocol: controlv1.UpstreamProtocol_UPSTREAM_PROTOCOL_DOT, Address: f.DoT, TlsServerName: f.TLSName, TimeoutMs: 1000, CaCertificatePem: f.CACertPEM}
}

// DoHUpstream is a DoH upstream pointing at the fixture by its TLS name (the engine pins that
// name to loopback through NEXORA_DOH_RESOLVE), trusting its CA.
func DoHUpstream(id string, f *DNSFixture) *controlv1.Upstream {
	_, port, _ := strings.Cut(f.DoH, ":")
	return &controlv1.Upstream{Id: id, Name: id, Protocol: controlv1.UpstreamProtocol_UPSTREAM_PROTOCOL_DOH, DohUrl: "https://" + f.TLSName + ":" + port + "/dns-query", TimeoutMs: 1000, CaCertificatePem: f.CACertPEM}
}

// BlocklistBlob builds a normalised blob (sorted domains, one per line, zstd) and its reference.
func BlocklistBlob(name string, domains ...string) (*controlv1.BlobRef, []byte) {
	d := append([]string(nil), domains...)
	sort.Strings(d)
	var buf bytes.Buffer
	enc, _ := zstd.NewWriter(&buf)
	_, _ = enc.Write([]byte(strings.Join(d, "\n") + "\n"))
	_ = enc.Close()
	sum := sha256.Sum256(buf.Bytes())
	return &controlv1.BlobRef{Sha256: hex.EncodeToString(sum[:]), Size: uint64(buf.Len()), Name: name}, buf.Bytes()
}
