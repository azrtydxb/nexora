// Package querylogtest holds the canonical query-log dataset and the conformance suite every
// query-log backend must pass, with a querylog.Builtin fed the same dataset as the reference.
package querylogtest

import (
	"fmt"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// EngineBatch is one OTLP export attributed to one engine.
type EngineBatch struct {
	EngineID string
	Req      *collogspb.ExportLogsServiceRequest
}

// windowLength is the length of the conformance search window.
const windowLength = 60 * time.Second

// Window is the conformance search window [base, base+60s].
func Window(base time.Time) (from, to time.Time) { return base, base.Add(windowLength) }

// InWindow counts the records of batches whose time lies inside Window(base).
func InWindow(batches []EngineBatch, base time.Time) int {
	from, to := Window(base)
	n := 0
	for _, b := range batches {
		for _, rl := range b.Req.GetResourceLogs() {
			for _, sl := range rl.GetScopeLogs() {
				for _, lr := range sl.GetLogRecords() {
					t := time.Unix(0, int64(lr.GetTimeUnixNano()))
					if !t.Before(from) && !t.After(to) {
						n++
					}
				}
			}
		}
	}
	return n
}

// spec is one dataset record: its offset from base in microseconds, engine, name and the
// attributes that differ from the defaults.
type spec struct {
	offsetUS int64
	engine   string
	name     string
	set      map[string]string
	ints     map[string]int64
	drop     []string
}

// Dataset returns the canonical records for run, in ascending time across all batches, with one
// batch per maximal run of consecutive records from the same engine.
func Dataset(run string, base time.Time) []EngineBatch {
	n := func(prefix string) string { return prefix + "-" + run + ".test." }
	specs := []spec{
		{offsetUS: -1_000, engine: "e1", name: n("before")},
		{offsetUS: 0, engine: "e1", name: n("edge-from")},
		{offsetUS: 1_000_000, engine: "e1", name: "www.you-" + run + ".tube.test.", set: map[string]string{
			"nexora.filter": "allowed", "nexora.filter.source": "allowlist", "nexora.filter.list_id": "allow-g1",
			"nexora.filter.rule": "tube.test", "nexora.policy.group": "g1",
		}},
		{offsetUS: 2_000_000, engine: "e1", name: n("you"), set: map[string]string{
			"dns.response.code": "NXDOMAIN", "nexora.cache": "hit", "client.address": "10.0.0.2",
		}},
		{offsetUS: 3_000_000, engine: "e2", name: n("x*y"), set: map[string]string{"dns.question.type": "AAAA"}},
		{offsetUS: 4_000_000, engine: "e2", name: "x%y_" + run + ".test."},
		{offsetUS: 5_000_000, engine: "e1", name: "X?Y-" + run + ".TEST.", set: map[string]string{"dns.question.type": "MX"}},
		{offsetUS: 6_000_000, engine: "e1", name: n("casino"), set: map[string]string{
			"nexora.filter": "blocked", "nexora.filter.source": "category", "nexora.filter.list_id": "l-gambling",
			"nexora.filter.category": "gambling",
		}},
		{offsetUS: 7_000_000, engine: "e2", name: n("ads"), set: map[string]string{
			"nexora.filter": "blocked", "nexora.filter.source": "blocklist", "nexora.filter.list_id": "l-custom",
			"nexora.filter.rule": "ads-" + run + ".test", "nexora.policy.group": "g1",
		}},
		{offsetUS: 8_000_000, engine: "e1", name: n("renamed"), drop: []string{"nexora.filter"}, set: map[string]string{
			"nexora.filter.result": "blocked", "nexora.filter.source": "blocklist",
		}},
		{offsetUS: 9_000_000, engine: "e1", name: n("rpz"), set: map[string]string{
			"dns.response.code": "NXDOMAIN", "nexora.filter.source": "rpz", "nexora.rpz_zone": "z1", "nexora.rpz": "nxdomain",
		}},
		{offsetUS: 10_000_000, engine: "e2", name: n("rw"), set: map[string]string{
			"nexora.filter.source": "rewrite", "nexora.filter.rule": "rw-" + run + ".test",
		}},
		{offsetUS: 11_000_000, engine: "e1", name: n("acl"), set: map[string]string{
			"dns.response.code": "REFUSED", "nexora.filter.source": "acl", "nexora.acl.refused": "authoritative",
		}},
		{offsetUS: 12_000_000, engine: "e2", name: n("race"), set: map[string]string{
			"nexora.upstream": "fx", "nexora.transport": "tcp",
		}, ints: map[string]int64{"nexora.upstream_raced": 3, "nexora.duration_us": 1234}},
		{offsetUS: 13_000_000, engine: "e1", name: n("tie"), set: map[string]string{"client.address": "10.0.0.11"}},
		{offsetUS: 13_000_000, engine: "e1", name: n("tie"), set: map[string]string{"client.address": "10.0.0.12"}},
		{offsetUS: 13_000_000, engine: "e2", name: n("tie"), set: map[string]string{"client.address": "10.0.0.13"}},
	}
	for i, top := range []string{"a", "a", "a", "a", "b", "b", "b", "c", "c", "c", "d"} {
		s := spec{
			offsetUS: 20_000_000 + int64(i)*100_000,
			engine:   []string{"e1", "e2"}[i%2],
			name:     n("top-" + top),
			set:      map[string]string{"client.address": fmt.Sprintf("10.0.1.%d", i)},
		}
		if top == "b" {
			s.set["nexora.filter"], s.set["nexora.filter.category"], s.set["nexora.filter.source"] = "blocked", "ads-tracking", "category"
		}
		specs = append(specs, s)
	}
	specs = append(specs,
		spec{offsetUS: 60_000_000, engine: "e2", name: n("edge-to")},
		spec{offsetUS: 60_001_000, engine: "e2", name: n("after")},
	)

	var out []EngineBatch
	for _, s := range specs {
		if len(out) == 0 || out[len(out)-1].EngineID != s.engine {
			out = append(out, EngineBatch{EngineID: s.engine, Req: request(s.engine)})
		}
		sl := out[len(out)-1].Req.ResourceLogs[0].ScopeLogs[0]
		sl.LogRecords = append(sl.LogRecords, record(base, s))
	}
	return out
}

func request(engine string) *collogspb.ExportLogsServiceRequest {
	return &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
			str("service.name", "nexora-engine"), str("nexora.engine.id", engine),
		}},
		ScopeLogs: []*logspb.ScopeLogs{{}},
	}}}
}

// record builds one LogRecord with the attribute keys of engine/src/telemetry/otlp.rs log_record.
func record(base time.Time, s spec) *logspb.LogRecord {
	strs := map[string]string{
		"client.address": "10.0.0.1", "dns.question.type": "A", "dns.response.code": "NOERROR",
		"nexora.cache": "miss", "nexora.filter": "none", "nexora.policy.group": "", "nexora.upstream": "",
		"nexora.transport": "udp", "nexora.rpz": "none",
	}
	ints := map[string]int64{"nexora.duration_us": 100}
	for k, v := range s.set {
		strs[k] = v
	}
	for k, v := range s.ints {
		ints[k] = v
	}
	for _, k := range s.drop {
		delete(strs, k)
	}
	attrs := []*commonpb.KeyValue{str("dns.question.name", s.name), str("nexora.engine.id", s.engine)}
	// Fixed key order keeps the records deterministic.
	for _, k := range []string{
		"client.address", "dns.question.type", "dns.response.code", "nexora.cache", "nexora.filter",
		"nexora.filter.result", "nexora.policy.group", "nexora.upstream", "nexora.transport", "nexora.rpz",
		"nexora.filter.list_id", "nexora.filter.category", "nexora.filter.source", "nexora.filter.rule",
		"nexora.acl.refused", "nexora.rpz_zone",
	} {
		if v, ok := strs[k]; ok {
			attrs = append(attrs, str(k, v))
		}
	}
	for _, k := range []string{"nexora.duration_us", "nexora.upstream_raced"} {
		if v, ok := ints[k]; ok {
			attrs = append(attrs, integer(k, v))
		}
	}
	return &logspb.LogRecord{
		TimeUnixNano:   uint64(base.Add(time.Duration(s.offsetUS) * time.Microsecond).UnixNano()),
		SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_INFO,
		SeverityText:   "INFO",
		Body:           &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: s.name}},
		Attributes:     attrs,
	}
}

func str(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func integer(k string, v int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}}
}
