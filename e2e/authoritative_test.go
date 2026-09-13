package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

type zoneResp struct {
	ID       string `json:"id"`
	Serial   uint32 `json:"serial"`
	Revision int64  `json:"revision"`
}

// authEnv is a management plane (bootstrapped admin API) with managed engines.
type authEnv struct {
	env     *harness.Env
	pg      *harness.Postgres
	ca      *harness.CA
	mg      *harness.Mgmt
	api     *harness.API
	engines []*harness.Engine
}

// startAuthEnv starts PostgreSQL, one management plane with extraEnv and one managed engine per
// node name, and waits until every engine applied the latest version.
func startAuthEnv(t *testing.T, extraEnv []string, nodes ...string) authEnv {
	t.Helper()
	e := authEnv{env: harness.New(t)}
	e.pg, e.ca = e.env.StartPostgres(), e.env.InitCA()
	e.mg = e.env.StartMgmt(e.pg, e.ca, harness.MgmtOptions{ExtraEnv: extraEnv})
	e.api = harness.Bootstrap(t, e.env, e.mg.SetupToken(t), e.mg.BaseURL)
	for _, n := range nodes {
		e.engines = append(e.engines, e.env.StartManagedEngine(n, []string{e.mg.GRPCURL}, e.api.CreateJoinToken()))
	}
	waitLatestApplied(t, e.api, nodes...)
	return e
}

func createPrimaryZone(t *testing.T, api *harness.API, name string, extra map[string]any) zoneResp {
	t.Helper()
	body := map[string]any{
		"name": name, "kind": "primary", "default_ttl": 300,
		"soa":         map[string]any{"mname": "ns1." + name, "rname": "hostmaster." + name},
		"nameservers": []string{"ns1." + name},
	}
	for k, v := range extra {
		body[k] = v
	}
	var z zoneResp
	api.Must(http.MethodPost, "/zones", body, &z, http.StatusCreated)
	return z
}

func addRecord(t *testing.T, api *harness.API, zoneID, name, typ, data string) {
	t.Helper()
	api.Must(http.MethodPost, "/zones/"+zoneID+"/records", map[string]any{"name": name, "type": typ, "ttl": 300, "data": data}, nil, http.StatusCreated)
}

func TestAuthoritativeZonePropagation(t *testing.T) {
	e := startAuthEnv(t, nil, "auth-1", "auth-2")
	api := e.api

	z := createPrimaryZone(t, api, "prop.test.", nil)
	addRecord(t, api, z.ID, "www.prop.test.", "A", "192.0.2.10")
	addRecord(t, api, z.ID, "child.prop.test.", "NS", "ns.child.prop.test.")
	addRecord(t, api, z.ID, "ns.child.prop.test.", "A", "192.0.2.53")
	created := time.Now()

	for i, eng := range e.engines {
		r := harness.WaitDNSAnswer(t, eng.DNS, "www.prop.test.", dns.TypeA, 5*time.Second-time.Since(created), func(m *dns.Msg) bool {
			return m.Rcode == dns.RcodeSuccess && len(m.Answer) == 1
		})
		if !r.Authoritative {
			t.Fatalf("engine %d answered without AA: %v", i, r)
		}
		if a, ok := r.Answer[0].(*dns.A); !ok || a.A.String() != "192.0.2.10" {
			t.Fatalf("engine %d answer: %v", i, r.Answer)
		}
	}
	// www may already answer from an intermediate version (before the delegation's glue record).
	waitLatestApplied(t, api, "auth-1", "auth-2")
	for i, eng := range e.engines {
		nx := harness.MustQuery(t, eng.DNS, "missing.prop.test.", dns.TypeA, harness.QueryOpts{TCP: true})
		if nx.Rcode != dns.RcodeNameError || !nx.Authoritative || len(nx.Ns) != 1 || nx.Ns[0].Header().Rrtype != dns.TypeSOA {
			t.Fatalf("engine %d NXDOMAIN: %v", i, nx)
		}
		ref := harness.MustQuery(t, eng.DNS, "host.child.prop.test.", dns.TypeA, harness.QueryOpts{TCP: true})
		if ref.Authoritative || len(ref.Ns) != 1 || len(ref.Extra) < 1 {
			t.Fatalf("engine %d referral: %v", i, ref)
		}
	}

	before := e.engines[0].Metric(t, "nexora_auth_zone_loads_total", map[string]string{"kind": "delta"})
	addRecord(t, api, z.ID, "api.prop.test.", "AAAA", "2001:db8::10")
	edited := time.Now()
	for _, eng := range e.engines {
		harness.WaitDNSAnswer(t, eng.DNS, "api.prop.test.", dns.TypeAAAA, 5*time.Second-time.Since(edited), func(m *dns.Msg) bool {
			return m.Authoritative && len(m.Answer) == 1
		})
	}
	if after := e.engines[0].Metric(t, "nexora_auth_zone_loads_total", map[string]string{"kind": "delta"}); after <= before {
		t.Fatalf("edit was not applied incrementally: delta loads %v -> %v", before, after)
	}
	if n := e.engines[0].Metric(t, "nexora_auth_zones", nil); n != 1 {
		t.Fatalf("nexora_auth_zones = %v, want 1", n)
	}
}
