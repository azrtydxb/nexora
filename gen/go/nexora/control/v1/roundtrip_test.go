package controlv1_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
)

func TestConfigSnapshotRoundTrip(t *testing.T) {
	in := &controlv1.ConfigSnapshot{
		Version:  7,
		Resolver: &controlv1.ResolverConfig{Strategy: controlv1.UpstreamStrategy_UPSTREAM_STRATEGY_FASTEST},
		Cache:    &controlv1.CacheConfig{MaxBytes: 1 << 26, MinTtl: 0, MaxTtl: 86400, NegativeMaxTtl: 3600, StaleWindow: 86400},
		Upstreams: []*controlv1.Upstream{{
			Id: "u1", Name: "quad9", Protocol: controlv1.UpstreamProtocol_UPSTREAM_PROTOCOL_DOT,
			Address: "9.9.9.9:853", TlsServerName: "dns.quad9.net", TimeoutMs: 250,
		}},
		AclAllowCidrs: []string{"10.0.0.0/8"},
		Filter: &controlv1.FilterConfig{
			Blocklists: []*controlv1.BlobRef{{Sha256: "ab", Size: 10, Name: "ads"}},
			BlockMode:  controlv1.BlockMode_BLOCK_MODE_NULL_IP, BlockTtl: 60,
		},
		Telemetry: &controlv1.TelemetryConfig{OtlpEndpoint: "http://otel:4317", TraceSampleOneIn: 1000, TraceSlowThresholdUs: 100000, QuerylogToManagement: true},
	}
	b, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	out := &controlv1.ConfigSnapshot{}
	if err := proto.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(in, out) {
		t.Fatalf("round trip mismatch:\n in=%v\nout=%v", in, out)
	}
}
