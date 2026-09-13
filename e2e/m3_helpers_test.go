package e2e

import (
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

const m3Engine = "engine-m3"

type qopt struct {
	DO, CD, TCP bool
	Source      string
	Timeout     time.Duration
}

type recursionEnv struct {
	env *harness.Env
	pg  *harness.Postgres
	api *harness.API
	eng *harness.Engine
	h   *harness.Hierarchy
}

func queryErr(server, name string, qtype uint16, o qopt) (*dns.Msg, error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), qtype)
	m.RecursionDesired = true
	m.CheckingDisabled = o.CD
	m.SetEdns0(1232, o.DO)
	c := &dns.Client{Timeout: o.Timeout}
	if c.Timeout == 0 {
		c.Timeout = 5 * time.Second
	}
	if o.TCP {
		c.Net = "tcp"
	}
	if o.Source != "" {
		if o.TCP {
			c.Dialer = &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(o.Source)}, Timeout: c.Timeout}
		} else {
			c.Dialer = &net.Dialer{LocalAddr: &net.UDPAddr{IP: net.ParseIP(o.Source)}, Timeout: c.Timeout}
		}
	}
	r, _, err := c.Exchange(m, server)
	return r, err
}

func query(t *testing.T, server, name string, qtype uint16, o qopt) *dns.Msg {
	t.Helper()
	r, err := queryErr(server, name, qtype, o)
	if err != nil {
		t.Fatalf("query %s %s: %v", name, dns.TypeToString[qtype], err)
	}
	return r
}

func aValues(m *dns.Msg) []string {
	var out []string
	for _, rr := range m.Answer {
		if a, ok := rr.(*dns.A); ok {
			out = append(out, a.A.String())
		}
	}
	return out
}

func edeCode(m *dns.Msg) (uint16, bool) {
	if opt := m.IsEdns0(); opt != nil {
		for _, o := range opt.Option {
			if e, ok := o.(*dns.EDNS0_EDE); ok {
				return e.InfoCode, true
			}
		}
	}
	return 0, false
}

func wantA(t *testing.T, m *dns.Msg, want string) {
	t.Helper()
	got := aValues(m)
	if m.Rcode != dns.RcodeSuccess || len(got) == 0 || got[len(got)-1] != want {
		t.Fatalf("want A %s, got rcode=%s answers=%v", want, dns.RcodeToString[m.Rcode], got)
	}
}

// waitApplied waits until the M3 engine applied the newest config version.
func waitApplied(t *testing.T, api *harness.API) {
	t.Helper()
	v := api.LatestVersion()
	api.WaitEngine(m3Engine, 15*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion >= v })
}

// setupRecursion starts PostgreSQL, a management plane with key storage, one managed engine and the
// private hierarchy, and switches the engine to recursion from the hierarchy's root.
func setupRecursion(t *testing.T) recursionEnv {
	t.Helper()
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	mgmt := env.StartMgmt(pg, ca, harness.MgmtOptions{ExtraEnv: []string{"NEXORA_KEK_FILE=" + harness.WriteKEK(t)}})
	api := harness.Bootstrap(t, env, mgmt.SetupToken(t), mgmt.BaseURL)
	eng := env.StartManagedEngine(m3Engine, []string{mgmt.GRPCURL}, api.CreateJoinToken())
	waitApplied(t, api)
	h := env.StartHierarchy()
	h.ConfigureRecursion(t, api, m3Engine)
	return recursionEnv{env: env, pg: pg, api: api, eng: eng, h: h}
}
