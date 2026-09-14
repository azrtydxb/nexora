package control_test

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
)

// M6 fields use 700-799 and survive a round trip; a renumbering or a missing field fails here.
func TestContractM6FieldsRoundTrip(t *testing.T) {
	snap := &controlv1.ConfigSnapshot{
		AuthoritativeAllowCidrs: []string{"10.0.0.0/8"}, AuthoritativeAclSet: true,
		Resolver:  &controlv1.ResolverConfig{Strategy: controlv1.UpstreamStrategy_UPSTREAM_STRATEGY_PARALLEL, ParallelMax: 2},
		AuthZones: []*controlv1.AuthZone{{Name: "kw.test.", AllowQueryCidrs: []string{"192.168.0.0/16"}, UpdateAllowCidrs: []string{"127.0.0.1/32"}}},
	}
	stats := &controlv1.Stats{
		QueriesByRcode: map[string]uint64{"NOERROR": 3}, QueriesByTransport: map[string]uint64{"udp": 3},
		MissDurationBucketCounts: []uint64{1}, FilterRewrittenTotal: 1, AnswersByRoute: map[string]uint64{"cache": 2},
		ResolutionFailuresTotal: 1, ProcessCpuSecondsTotal: 1.5, ProcessResidentBytes: 1 << 20, MemoryLimitBytes: 1 << 30,
		OpenConnections: map[string]uint64{"dot": 1}, StartedUnixMs: 42, AclRefused: map[string]uint64{"recursion": 1},
		TlsCertificateNotAfterUnix: 99, LogLinesDroppedTotal: 5, RaceDurationBucketCounts: []uint64{2},
		Recursion: &controlv1.RecursionStats{UpstreamTimeouts: 7},
		Upstreams: []*controlv1.UpstreamStatus{{Name: "a", RaceWinsTotal: 4}},
	}
	req := &controlv1.ServerMessage{Msg: &controlv1.ServerMessage_LogRequest{LogRequest: &controlv1.LogRequest{
		RequestId: "r1", AfterSeq: 9, MinLevel: controlv1.LogLevel_LOG_LEVEL_WARN, Limit: 1000, Contains: "snapshot"}}}
	batch := &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_LogBatch{LogBatch: &controlv1.LogBatch{
		RequestId: "r1", Lines: []*controlv1.LogLine{{Seq: 10, UnixMs: 1, Level: controlv1.LogLevel_LOG_LEVEL_INFO, Message: "m"}}, LastSeq: 10, OldestSeq: 1}}}
	for _, m := range []proto.Message{snap, stats, req, batch} {
		b, err := proto.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		back := m.ProtoReflect().New().Interface()
		if err := proto.Unmarshal(b, back); err != nil || !proto.Equal(m, back) {
			t.Fatalf("round trip of %T: %v", m, err)
		}
	}
	fields := map[string]int32{
		"authoritative_allow_cidrs": 700, "authoritative_acl_set": 701,
	}
	desc := snap.ProtoReflect().Descriptor().Fields()
	for name, num := range fields {
		if f := desc.ByName(protoreflectName(name)); f == nil || int32(f.Number()) != num {
			t.Fatalf("ConfigSnapshot.%s must be field %d", name, num)
		}
	}
}

func protoreflectName(s string) protoreflect.Name { return protoreflect.Name(s) }
