package rpzsuggest_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/piwi3910/nexora/mgmt/internal/ai/rpzsuggest"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func attr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

// ingest adds n not-blocked records for name from client, one second apart, ending a minute before now.
func ingest(b *querylog.Builtin, now time.Time, client, name string, n int) {
	var recs []*logspb.LogRecord
	for i := range n {
		at := now.Add(-time.Minute - time.Duration(i)*time.Second)
		recs = append(recs, &logspb.LogRecord{TimeUnixNano: uint64(at.UnixNano()), Attributes: []*commonpb.KeyValue{
			attr("client.address", client), attr("dns.question.name", name+"."), attr("dns.question.type", "A"),
			attr("dns.response.code", "NOERROR"), attr("nexora.cache", "miss"), attr("nexora.filter", "none"), attr("nexora.engine.id", "e1"),
		}})
	}
	b.Ingest("e1", &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{ScopeLogs: []*logspb.ScopeLogs{{LogRecords: recs}}}}})
}

// randomLabel is a 48-character hexadecimal label: entropy above the 3.5 bits/char family threshold.
func randomLabel(i int) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "ars-%d", i))
	return hex.EncodeToString(sum[:])[:48]
}

// seedTraffic is the shared 24 h fixture: a look-alike of the hosted zone, a high-entropy family, a plain
// name and a name with a threat verdict.
func seedTraffic(t *testing.T, st *store.Store, now time.Time) *querylog.Builtin {
	t.Helper()
	ctx := context.Background()
	if _, err := st.Pool.Exec(ctx, `insert into zones(name, kind, soa_mname, soa_rname)
		values ('corp.example.', 'primary', 'ns1.corp.example.', 'hostmaster.corp.example.')`); err != nil {
		t.Fatal(err)
	}
	b := querylog.NewBuiltin(1000)
	ingest(b, now, "10.0.0.5", "crop.example", 12)
	ingest(b, now, "10.0.0.6", "crop.example", 3)
	for i := range 6 {
		ingest(b, now, "10.0.0.5", randomLabel(i)+".fam.ars.test", 4)
	}
	ingest(b, now, "10.0.0.5", "ok.ars.test", 20)
	ingest(b, now, "10.0.0.5", "bad.ars.test", 7)
	if _, err := st.Pool.Exec(ctx, `insert into ai_domain_verdicts(name, is_threat, categories, confidence, reasoning, checked_at, expires_at)
		values ('bad.ars.test', true, '{malware}', 0.8, 'known command and control host', $1, $2)`, now, now.Add(7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	return b
}

// TestCollectRpzInputs catches a look-alike distance that matches everything or nothing, a family detector
// that groups by the wrong parent or ignores entropy, a threat verdict below the confidence floor and
// ordinary names offered to the model as suggestions.
func TestCollectRpzInputs(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"corp", "crop", 1}, {"corp", "c0rp", 1}, {"corp", "corp", 0}} {
		if got := rpzsuggest.DamerauLevenshtein(c.a, c.b); got != c.want {
			t.Fatalf("DamerauLevenshtein(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if got := rpzsuggest.DamerauLevenshtein("corp", "example"); got <= 2 {
		t.Fatalf("DamerauLevenshtein(corp, example) = %d, want more than 2", got)
	}

	ctx := context.Background()
	st := storetest.New(t)
	now := time.Now()
	b := seedTraffic(t, st, now)

	inputs, err := rpzsuggest.Collect(ctx, st, b, now)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]rpzsuggest.Input{}
	for _, in := range inputs {
		if _, dup := byName[in.Name]; dup {
			t.Fatalf("duplicate input for %s: %+v", in.Name, inputs)
		}
		byName[in.Name] = in
	}

	look, ok := byName["crop.example"]
	if !ok || look.Kind != "lookalike" || look.Queries != 15 || look.Clients != 2 {
		t.Fatalf("look-alike input = %+v (inputs %+v)", look, inputs)
	}
	fam, ok := byName["*.fam.ars.test"]
	if !ok || fam.Kind != "family" || fam.Queries != 24 {
		t.Fatalf("family input = %+v (inputs %+v)", fam, inputs)
	}
	threat, ok := byName["bad.ars.test"]
	if !ok || threat.Kind != "threat" || threat.Queries != 7 {
		t.Fatalf("threat input = %+v (inputs %+v)", threat, inputs)
	}
	if threat.Evidence == "" || fam.Evidence == "" || look.Evidence == "" {
		t.Fatalf("inputs without evidence: %+v", inputs)
	}
	for _, name := range []string{"ok.ars.test", "*.ars.test", "corp.example"} {
		if in, ok := byName[name]; ok {
			t.Fatalf("unwanted input %+v", in)
		}
	}

	// A record already applied is never suggested again.
	if _, err := st.Pool.Exec(ctx, `insert into ai_rpz_rules(record, policy, category, reason, proposal_id, applied_by)
		values ('bad.ars.test', 'nxdomain', 'malware', 'applied', gen_random_uuid(), 'tester')`); err != nil {
		t.Fatal(err)
	}
	inputs, err = rpzsuggest.Collect(ctx, st, b, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range inputs {
		if in.Name == "bad.ars.test" {
			t.Fatalf("applied record suggested again: %+v", in)
		}
	}
}
