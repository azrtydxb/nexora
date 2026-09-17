package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
)

// TestKwQueryLogBackends checks on the live kw deployment that the collector's three query-log
// destinations agree: records of fresh queries to both DNS addresses appear identically in
// OpenSearch, ClickHouse and the shared Loki, and their top names and clients over a settled
// ten-minute window are equal. scripts/kw-acceptance.sh sets the environment (deploy/kw/README.md).
func TestKwQueryLogBackends(t *testing.T) {
	dnsAddr := os.Getenv("NEXORA_KW_DNS_ADDR")
	if dnsAddr == "" {
		t.Skip("NEXORA_KW_DNS_ADDR is not set: run through scripts/kw-acceptance.sh")
	}
	vars := map[string]string{}
	for _, k := range []string{"NEXORA_KW_DNS_ADDR_2", "NEXORA_KW_OPENSEARCH_URL", "NEXORA_KW_CLICKHOUSE_URL", "NEXORA_KW_CLICKHOUSE_PASSWORD_FILE", "NEXORA_KW_LOKI_URL"} {
		if vars[k] = os.Getenv(k); vars[k] == "" {
			t.Fatalf("%s is required (set by scripts/kw-acceptance.sh; see deploy/kw/README.md)", k)
		}
	}

	osb, err := querylog.NewOpenSearch(config.OpenSearchConfig{URL: vars["NEXORA_KW_OPENSEARCH_URL"], Index: "nexora-querylog-*"})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := querylog.NewClickHouse(config.ClickHouseConfig{URL: vars["NEXORA_KW_CLICKHOUSE_URL"], Database: "nexora", Table: "querylog",
		Username: "nexora_reader", PasswordFile: vars["NEXORA_KW_CLICKHOUSE_PASSWORD_FILE"]})
	if err != nil {
		t.Fatal(err)
	}
	lk, err := querylog.NewLoki(config.LokiConfig{URL: vars["NEXORA_KW_LOKI_URL"], Selector: `{service_name="nexora-engine"}`, Lookback: 168 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	backends := []interface {
		querylog.Backend
		querylog.Topper
	}{osb, ch, lk}

	// Positive path: fresh queries over both addresses reach every backend.
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	run := hex.EncodeToString(b)
	start := time.Now()
	addrs := []string{dnsAddr, vars["NEXORA_KW_DNS_ADDR_2"]}
	const sent = 20
	for i := range sent {
		m := new(dns.Msg)
		m.SetQuestion(fmt.Sprintf("kwql-%d-%s.test.", i, run), dns.TypeA)
		r, _, err := (&dns.Client{Timeout: 3 * time.Second}).Exchange(m, addrs[i%2])
		if err != nil {
			t.Fatalf("query %s via %s: %v", m.Question[0].Name, addrs[i%2], err)
		}
		if r.Rcode != dns.RcodeSuccess && r.Rcode != dns.RcodeNameError {
			t.Fatalf("query %s via %s: rcode %s, want NOERROR or NXDOMAIN", m.Question[0].Name, addrs[i%2], dns.RcodeToString[r.Rcode])
		}
	}

	sets := make([][]string, len(backends))
	harness.EventuallyTrue(t, 60*time.Second, func() bool {
		for i, be := range backends {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			page, err := be.Search(ctx, querylog.Query{Name: "-" + run, From: start.Add(-time.Minute), To: time.Now(), Limit: 100})
			cancel()
			if err != nil {
				t.Logf("%s: %v", be.Name(), err)
				return false
			}
			sets[i] = tuples(page.Records)
			if len(sets[i]) != sent {
				t.Logf("%s: %d of %d records visible", be.Name(), len(sets[i]), sent)
				return false
			}
		}
		return true
	}, fmt.Sprintf("%d records of run %s in every backend", sent, run))
	for i := 1; i < len(backends); i++ {
		if !slices.Equal(sets[0], sets[i]) {
			t.Fatalf("%s and %s records differ:\n%s:\n  %s\n%s:\n  %s", backends[0].Name(), backends[i].Name(),
				backends[0].Name(), strings.Join(sets[0], "\n  "), backends[i].Name(), strings.Join(sets[i], "\n  "))
		}
	}

	// Top over a window every backend has long ingested.
	from := time.Now().Add(-13 * time.Minute).Truncate(time.Minute)
	to := from.Add(10 * time.Minute)
	for _, field := range []querylog.TopField{querylog.TopName, querylog.TopClient} {
		lists := make([][]querylog.TopEntry, len(backends))
		for i, be := range backends {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			lists[i], err = be.Top(ctx, querylog.TopQuery{From: from, To: to, Field: field, Limit: 10})
			cancel()
			if err != nil {
				t.Fatalf("%s top %s: %v", be.Name(), field, err)
			}
		}
		if len(lists[0]) == 0 {
			t.Fatalf("%s top %s over [%s, %s] is empty", backends[0].Name(), field, from, to)
		}
		if !reflect.DeepEqual(lists[0], lists[1]) || !reflect.DeepEqual(lists[0], lists[2]) {
			t.Fatalf("top %s over [%s, %s] differs:\n%s: %v\n%s: %v\n%s: %v", field, from, to,
				backends[0].Name(), lists[0], backends[1].Name(), lists[1], backends[2].Name(), lists[2])
		}
	}
}

// tuples returns the sorted (Name, Client, QType, RCode, EngineID, Filter) tuples of records.
func tuples(records []querylog.Record) []string {
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, strings.Join([]string{r.Name, r.Client, r.QType, r.RCode, r.EngineID, r.Filter}, " | "))
	}
	slices.Sort(out)
	return out
}
