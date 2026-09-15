package anomaly_test

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/ai/anomaly"
	"github.com/piwi3910/nexora/mgmt/internal/ai/finding"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
)

func rec(client, name, qtype, rcode string, at time.Time) querylog.Record {
	return querylog.Record{Time: at, Client: client, Name: name, QType: qtype, RCode: rcode, Filter: "none"}
}

// randomLabel is deterministic lower-case base32 of sha256(i), truncated to n characters.
func randomLabel(i, n int) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(i))
	sum := sha256.Sum256(b[:])
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]))[:n]
}

func has(cands []finding.Candidate, id string) bool {
	for _, c := range cands {
		if c.ID == id {
			return true
		}
	}
	return false
}

func severity(cands []finding.Candidate, id string) string {
	for _, c := range cands {
		if c.ID == id {
			return c.Severity
		}
	}
	return ""
}

// newestFirst reverses recs, which the tests build oldest first, into the backend's order.
func newestFirst(recs []querylog.Record) []querylog.Record {
	out := make([]querylog.Record, len(recs))
	for i, r := range recs {
		out[len(recs)-1-i] = r
	}
	return out
}

func TestEntropyAndRegistrableParent(t *testing.T) {
	if e := anomaly.Entropy("aaaa"); e != 0 {
		t.Fatalf("entropy(aaaa) = %v", e)
	}
	if e := anomaly.Entropy("abcd"); math.Abs(e-2) > 1e-9 {
		t.Fatalf("entropy(abcd) = %v, want 2", e)
	}
	for name, want := range map[string]string{
		"x.tunnel.bad.example.": "bad.example",
		"a.b.Example.CO.uk.":    "example.co.uk",
		"www.shop.com.au":       "shop.com.au",
		"example.":              "example",
		"deep.a.b.gov.uk":       "b.gov.uk",
		"deep.a.b.io":           "b.io",
	} {
		if got := anomaly.RegistrableParent(name); got != want {
			t.Errorf("RegistrableParent(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestTunnelingDetector(t *testing.T) {
	at := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	var recs []querylog.Record
	for i := 0; i < 50; i++ {
		recs = append(recs, rec("10.0.1.45", randomLabel(i, 32)+".tunnel.bad.example.", "A", "NOERROR", at.Add(time.Duration(i)*time.Second)))
	}
	c := anomaly.Detect(anomaly.Window{Records: recs})
	if !has(c, "dns_tunneling:10.0.1.45") || severity(c, "dns_tunneling:10.0.1.45") != "critical" {
		t.Fatalf("50 high-entropy queries not detected: %+v", c)
	}
	if c := anomaly.Detect(anomaly.Window{Records: recs[:49]}); has(c, "dns_tunneling:10.0.1.45") {
		t.Fatal("49 queries detected")
	}
	var short []querylog.Record
	for i := 0; i < 60; i++ {
		short = append(short, rec("10.0.1.47", randomLabel(i, 19)+".tunnel.bad.example.", "A", "NOERROR", at))
	}
	if c := anomaly.Detect(anomaly.Window{Records: short}); has(c, "dns_tunneling:10.0.1.47") {
		t.Fatal("19-character labels detected as tunnelling")
	}
	var low []querylog.Record
	for i := 0; i < 60; i++ {
		low = append(low, rec("10.0.1.46", fmt.Sprintf("host%02d.example.com.", i), "A", "NOERROR", at))
	}
	if c := anomaly.Detect(anomaly.Window{Records: low}); has(c, "dns_tunneling:10.0.1.46") {
		t.Fatal("low-entropy names detected as tunnelling")
	}

	// TXT/NULL share: 30 of 75 (40%) is detected; 29 of 72 (40.3%) and 30 of 76 (39.5%) are not.
	txt := func(client string, special, total int) []querylog.Record {
		var out []querylog.Record
		for i := 0; i < total; i++ {
			qtype := "A"
			if i < special {
				qtype = []string{"TXT", "NULL"}[i%2]
			}
			out = append(out, rec(client, fmt.Sprintf("host%02d.example.com.", i), qtype, "NOERROR", at))
		}
		return out
	}
	if c := anomaly.Detect(anomaly.Window{Records: txt("10.0.2.1", 30, 75)}); !has(c, "dns_tunneling:10.0.2.1") {
		t.Fatalf("30 of 75 TXT/NULL not detected: %+v", c)
	}
	if c := anomaly.Detect(anomaly.Window{Records: txt("10.0.2.1", 29, 72)}); has(c, "dns_tunneling:10.0.2.1") {
		t.Fatal("29 TXT/NULL queries detected")
	}
	if c := anomaly.Detect(anomaly.Window{Records: txt("10.0.2.1", 30, 76)}); has(c, "dns_tunneling:10.0.2.1") {
		t.Fatal("39.5% TXT/NULL detected")
	}
}

func TestNXDomainBurstDetector(t *testing.T) {
	at := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	burst := func(nx, total int) []querylog.Record {
		var out []querylog.Record
		for i := 0; i < total; i++ {
			rcode := "NOERROR"
			if i < nx {
				rcode = "NXDOMAIN"
			}
			out = append(out, rec("10.0.3.9", fmt.Sprintf("host%02d.example.com.", i), "A", rcode, at))
		}
		return out
	}
	c := anomaly.Detect(anomaly.Window{Records: burst(25, 50)})
	if !has(c, "nxdomain_burst:10.0.3.9") || severity(c, "nxdomain_burst:10.0.3.9") != "warning" {
		t.Fatalf("50 queries at 0.5 NXDOMAIN not detected: %+v", c)
	}
	if c := anomaly.Detect(anomaly.Window{Records: burst(49, 49)}); has(c, "nxdomain_burst:10.0.3.9") {
		t.Fatal("49 queries detected")
	}
	if c := anomaly.Detect(anomaly.Window{Records: burst(24, 50)}); has(c, "nxdomain_burst:10.0.3.9") {
		t.Fatal("NXDOMAIN ratio 0.48 detected")
	}
}

func TestQueryFloodDetector(t *testing.T) {
	to := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	from := to.Add(-10 * time.Second)
	flood := func(qps int, baseline float64) anomaly.Window {
		var recs []querylog.Record
		for i := 0; i < qps*10; i++ {
			recs = append(recs, rec("10.0.4.2", "busy.example.com.", "A", "NOERROR", from.Add(time.Duration(i)*time.Second/time.Duration(qps))))
		}
		return anomaly.Window{Records: newestFirst(recs), From: from, To: to, Baseline: map[string]float64{"10.0.4.2": baseline}}
	}
	c := anomaly.Detect(flood(20, 2))
	if !has(c, "query_flood:10.0.4.2") || severity(c, "query_flood:10.0.4.2") != "warning" {
		t.Fatalf("20 QPS against baseline 2 not detected: %+v", c)
	}
	if c := anomaly.Detect(flood(19, 1)); has(c, "query_flood:10.0.4.2") {
		t.Fatal("19 QPS detected")
	}
	if c := anomaly.Detect(flood(20, 2.1)); has(c, "query_flood:10.0.4.2") {
		t.Fatal("20 QPS against baseline 2.1 (below 10x) detected")
	}
	w := flood(20, 2)
	w.Baseline = nil
	if c := anomaly.Detect(w); has(c, "query_flood:10.0.4.2") {
		t.Fatal("flood detected without a baseline")
	}
}

func TestPeriodicBeaconDetector(t *testing.T) {
	start := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	series := func(n int, gap func(i int) time.Duration) []querylog.Record {
		var out []querylog.Record
		at := start
		for i := 0; i < n; i++ {
			out = append(out, rec("10.0.5.7", "c2.beacon.example.", "A", "NOERROR", at))
			at = at.Add(gap(i))
		}
		return newestFirst(out)
	}
	jitter := func(i int) time.Duration { return 60*time.Second + time.Duration(i%7-3)*time.Second }
	c := anomaly.Detect(anomaly.Window{Records: series(20, jitter)})
	if id := "periodic_beacon:10.0.5.7|c2.beacon.example."; !has(c, id) || severity(c, id) != "warning" {
		t.Fatalf("20 queries at 60 s +-3 s not detected: %+v", c)
	}
	if c := anomaly.Detect(anomaly.Window{Records: series(19, jitter)}); len(c) != 0 {
		t.Fatalf("19 queries detected: %+v", c)
	}
	random := func(i int) time.Duration {
		return time.Duration(10+int(binary.BigEndian.Uint16([]byte(randomLabel(i, 2)))%111)) * time.Second
	}
	if c := anomaly.Detect(anomaly.Window{Records: series(20, random)}); len(c) != 0 {
		t.Fatalf("random 10-120 s gaps detected: %+v", c)
	}
}

func TestCategoryEscalationDetector(t *testing.T) {
	at := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	blocked := rec("10.0.6.3", "evil.example.", "A", "NXDOMAIN", at)
	blocked.Filter, blocked.Category, blocked.PolicyGroupID = "blocked", "malware", "g-kids"
	c := anomaly.Detect(anomaly.Window{Records: []querylog.Record{blocked}, PriorThreats: map[string]bool{}})
	if !has(c, "category_escalation:client:10.0.6.3") || !has(c, "category_escalation:group:g-kids") ||
		severity(c, "category_escalation:client:10.0.6.3") != "critical" {
		t.Fatalf("new malware block not detected: %+v", c)
	}
	c = anomaly.Detect(anomaly.Window{Records: []querylog.Record{blocked}, PriorThreats: map[string]bool{"client:10.0.6.3": true, "group:g-kids": true}})
	if has(c, "category_escalation:client:10.0.6.3") || has(c, "category_escalation:group:g-kids") {
		t.Fatalf("block with prior threats detected: %+v", c)
	}
	ads := blocked
	ads.Category = "ads-tracking"
	if c := anomaly.Detect(anomaly.Window{Records: []querylog.Record{ads}}); len(c) != 0 {
		t.Fatalf("ads block detected: %+v", c)
	}
	allowed := blocked
	allowed.Filter = "allowed"
	if c := anomaly.Detect(anomaly.Window{Records: []querylog.Record{allowed}}); len(c) != 0 {
		t.Fatalf("unblocked malware-category record detected: %+v", c)
	}
}
