package harness_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

func TestHarnessStandaloneEngineAnswersViaFixture(t *testing.T) {
	env := harness.New(t)
	fx := env.StartDNSFixture()
	eng := env.StartStandaloneEngine(harness.BaseSnapshot(1, harness.UDPUpstream("fx", fx.UDP)), nil)
	name := harness.UniqueName("harness")
	r := harness.MustQuery(t, eng.DNS, name, dns.TypeA, harness.QueryOpts{})
	if r.Rcode != dns.RcodeSuccess || len(r.Answer) != 1 {
		t.Fatalf("unexpected answer: %v", r)
	}
	if got := fx.Count(t, name, dns.TypeA); got != 1 {
		t.Fatalf("fixture count = %d", got)
	}
	if v := eng.Metric(t, "nexora_config_version", nil); v != 1 {
		t.Fatalf("config version metric = %v", v)
	}
}

func TestHarnessPostgres(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var one int
	if err := conn.QueryRow(ctx, "select 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("select 1: %v %d", err, one)
	}
}

func TestHarnessOtelcolStarts(t *testing.T) {
	env := harness.New(t)
	col := env.StartOtelcol(harness.OtelcolConfig{DebugFile: env.Dir + "/otel.jsonl"})
	if col.OTLPGRPC == "" {
		t.Fatal("no OTLP address")
	}
}

func TestHarnessOtelcolRestartWaitsForPortHolder(t *testing.T) {
	env := harness.New(t)
	col := env.StartOtelcol(harness.OtelcolConfig{DebugFile: env.Dir + "/otel.jsonl"})
	col.Stop()
	l, err := net.Listen("tcp", col.OTLPGRPC)
	if err != nil {
		t.Fatalf("take the stopped collector's port: %v", err)
	}
	go func() { time.Sleep(1500 * time.Millisecond); l.Close() }()
	started := time.Now()
	col.Restart(env)
	if time.Since(started) < time.Second {
		t.Fatal("restart returned while another process still held the port")
	}
	c, err := net.DialTimeout("tcp", col.OTLPGRPC, time.Second)
	if err != nil {
		t.Fatalf("collector not listening after restart: %v", err)
	}
	c.Close()
}

func TestHarnessFixtureClients(t *testing.T) {
	env := harness.New(t)
	fx := env.StartDNSFixture()
	name := harness.UniqueName("client")
	for _, c := range []struct {
		server string
		o      harness.QueryOpts
	}{{fx.UDP, harness.QueryOpts{EDNSSize: 1232, Cookie: []byte("12345678")}}, {fx.TCP, harness.QueryOpts{TCP: true}}} {
		if r := harness.MustQuery(t, c.server, name, dns.TypeA, c.o); len(r.Answer) != 1 {
			t.Fatalf("answer with %+v: %v", c.o, r)
		}
	}
	if got := fx.Count(t, name, dns.TypeA); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
	fx.SetMode(t, "servfail")
	if r := harness.MustQuery(t, fx.TCP, name, dns.TypeA, harness.QueryOpts{TCP: true}); r.Rcode != dns.RcodeServerFailure {
		t.Fatalf("servfail mode rcode = %d", r.Rcode)
	}
	fx.SetDelay(t, 300*time.Millisecond)
	if _, rtt, err := harness.Query(t, fx.UDP, name, dns.TypeA, harness.QueryOpts{}); err != nil || rtt < 300*time.Millisecond {
		t.Fatalf("delay: rtt %s err %v", rtt, err)
	}
	fx.Reset(t)
	if fx.Total(t) != 0 {
		t.Fatal("reset did not clear the counters")
	}

	hf := env.StartHTTPFixture()
	hf.SetList(t, "ads", "ads.example\n")
	resp, err := http.Get(hf.URL("ads"))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("get list: %v %v", resp, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "ads.example\n" {
		t.Fatalf("list body = %q", body)
	}
	hf.SetFailing(t, "ads", true)
	if resp, err = http.Get(hf.URL("ads")); err != nil || resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("failing list: %v %v", resp, err)
	}
	resp.Body.Close()
	if n := hf.Hits(t, "ads"); n != 2 {
		t.Fatalf("hits = %d", n)
	}
}
