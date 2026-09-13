package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

type tsigKeyResp struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Secret   string `json:"secret"`
	Revision int64  `json:"revision"`
}

func axfr(addr, zone string, key *tsigKeyResp) ([]dns.RR, error) {
	m := new(dns.Msg)
	m.SetAxfr(zone)
	tr := &dns.Transfer{DialTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second}
	if key != nil {
		m.SetTsig(key.Name, dns.HmacSHA256, 300, time.Now().Unix())
		tr.TsigSecret = map[string]string{key.Name: key.Secret}
	}
	ch, err := tr.In(m, addr)
	if err != nil {
		return nil, err
	}
	var out []dns.RR
	for env := range ch {
		if env.Error != nil {
			return out, env.Error
		}
		out = append(out, env.RR...)
	}
	return out, nil
}

func soaSerial(addr, zone string) (uint32, error) {
	m := new(dns.Msg)
	m.SetQuestion(zone, dns.TypeSOA)
	r, _, err := (&dns.Client{Timeout: time.Second}).Exchange(m, addr)
	if err != nil {
		return 0, err
	}
	if r.Rcode != dns.RcodeSuccess || len(r.Answer) == 0 {
		return 0, fmt.Errorf("SOA %s: rcode %s, %d answers", zone, dns.RcodeToString[r.Rcode], len(r.Answer))
	}
	soa, ok := r.Answer[0].(*dns.SOA)
	if !ok {
		return 0, fmt.Errorf("SOA %s: answer %v", zone, r.Answer[0])
	}
	return soa.Serial, nil
}

func getZone(t *testing.T, api *harness.API, id string) zoneResp {
	t.Helper()
	var z zoneResp
	api.Must(http.MethodGet, "/zones/"+id, nil, &z, http.StatusOK)
	return z
}

func waitSerial(t *testing.T, addr, zone string, want uint32) {
	t.Helper()
	harness.Eventually(t, 15*time.Second, func() error {
		got, err := soaSerial(addr, zone)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("serial %d, want %d", got, want)
		}
		return nil
	})
}

func TestAXFRIXFROut(t *testing.T) {
	e := startAuthEnv(t, []string{"NEXORA_KEK_FILE=" + harness.WriteKEK(t)}, "xfr-1")
	api, eng := e.api, e.engines[0]

	var key tsigKeyResp
	api.Must(http.MethodPost, "/tsig-keys", map[string]any{"name": "xfr-key.", "algorithm": "hmac-sha256"}, &key, http.StatusCreated)
	z := createPrimaryZone(t, api, "xfr.test.", map[string]any{
		"transfer": map[string]any{"allow_cidrs": []string{"127.0.0.1/32"}, "tsig_key_id": key.ID},
	})
	addRecord(t, api, z.ID, "ns1.xfr.test.", "A", "192.0.2.1")
	harness.WaitDNSAnswer(t, eng.DNS, "ns1.xfr.test.", dns.TypeA, 5*time.Second, func(m *dns.Msg) bool { return len(m.Answer) == 1 })

	// positive: TSIG-signed AXFR from an allowed address succeeds
	rrs, err := axfr(eng.DNS, "xfr.test.", &key)
	if err != nil || len(rrs) < 4 || rrs[0].Header().Rrtype != dns.TypeSOA || rrs[len(rrs)-1].Header().Rrtype != dns.TypeSOA {
		t.Fatalf("signed AXFR: %d records, err=%v", len(rrs), err)
	}
	// negative: the same transfer without TSIG is refused
	if _, err := axfr(eng.DNS, "xfr.test.", nil); err == nil || !strings.Contains(err.Error(), "bad xfr rcode: 5") {
		t.Fatalf("unsigned AXFR: got %v, want REFUSED", err)
	}

	named := e.env.StartNamedConfig(harness.NamedConfig{
		Keys:  []harness.NamedKey{{Name: "xfr-key.", Algorithm: "hmac-sha256", SecretB64: key.Secret}},
		Zones: []harness.NamedZone{{Name: "xfr.test.", Type: "secondary", Primary: eng.DNS, KeyName: "xfr-key."}},
	})
	initial := getZone(t, api, z.ID)
	waitSerial(t, named.Addr, "xfr.test.", initial.Serial)

	// the secondary's address is known only now: add it as a NOTIFY target
	api.Must(http.MethodPatch, "/zones/"+z.ID, map[string]any{
		"revision": initial.Revision,
		"notify":   []map[string]any{{"address": named.Addr, "tsig_key_id": key.ID}},
	}, nil, http.StatusOK)
	waitLatestApplied(t, api, "xfr-1")

	ixfrBefore := eng.Metric(t, "nexora_auth_transfers_total", map[string]string{"type": "ixfr", "result": "incremental"})
	addRecord(t, api, z.ID, "www.xfr.test.", "A", "192.0.2.80")
	edited := getZone(t, api, z.ID)
	if edited.Serial == initial.Serial {
		t.Fatalf("zone serial did not advance on edit")
	}
	// SOA refresh is 10800 s: only NOTIFY makes the secondary transfer within 15 s
	waitSerial(t, named.Addr, "xfr.test.", edited.Serial)
	r := harness.MustQuery(t, named.Addr, "www.xfr.test.", dns.TypeA, harness.QueryOpts{})
	if len(r.Answer) != 1 {
		t.Fatalf("secondary does not serve the new record: %v", r)
	}
	if after := eng.Metric(t, "nexora_auth_transfers_total", map[string]string{"type": "ixfr", "result": "incremental"}); after <= ixfrBefore {
		t.Fatalf("secondary did not use an incremental IXFR (%v -> %v)", ixfrBefore, after)
	}
	if sent := eng.Metric(t, "nexora_auth_notify_sent_total", map[string]string{"result": "acked"}); sent < 1 {
		t.Fatalf("no acknowledged NOTIFY")
	}
}
