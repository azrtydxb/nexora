package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/e2e/harness"
)

// kwBlockedName is listed by the in-cluster static block list that deploy/kw/bootstrap.sh
// subscribes to (deploy/kw/blocklist.yaml).
const kwBlockedName = "ads.nexora-smoke.test."

// kwSmokeGroup is the policy group the smoke test creates for its own client address and removes
// again (also when an interrupted run left it behind).
const kwSmokeGroup = "kw-smoke-client"

// kwEnv is TestKwSmoke's environment, printed by scripts/kw-deploy.sh (see deploy/kw/README.md).
type kwEnv struct {
	dnsAddr, apiURL, encAddr, tlsName, mgmtLBIP string
	engines                                     int
	dnsRoots, apiRoots                          *x509.CertPool
	password                                    string
}

func loadKwEnv(t *testing.T) kwEnv {
	t.Helper()
	e := kwEnv{
		dnsAddr: os.Getenv("NEXORA_KW_DNS_ADDR"), apiURL: strings.TrimSuffix(os.Getenv("NEXORA_KW_API_URL"), "/"),
		encAddr: os.Getenv("NEXORA_KW_ENCRYPTED_ADDR"), tlsName: os.Getenv("NEXORA_KW_DNS_TLS_NAME"),
		mgmtLBIP: os.Getenv("NEXORA_KW_MGMT_LB_IP"),
	}
	if e.dnsAddr == "" || e.apiURL == "" {
		t.Skip("NEXORA_KW_DNS_ADDR and NEXORA_KW_API_URL are not set")
	}
	for k, v := range map[string]string{
		"NEXORA_KW_ENCRYPTED_ADDR": e.encAddr, "NEXORA_KW_DNS_TLS_NAME": e.tlsName, "NEXORA_KW_MGMT_LB_IP": e.mgmtLBIP,
		"NEXORA_KW_ENGINES": os.Getenv("NEXORA_KW_ENGINES"), "NEXORA_KW_CA_FILE": os.Getenv("NEXORA_KW_CA_FILE"),
		"NEXORA_KW_ADMIN_PASSWORD_FILE": os.Getenv("NEXORA_KW_ADMIN_PASSWORD_FILE"),
	} {
		if v == "" {
			t.Fatalf("%s is required (printed by scripts/kw-deploy.sh; see deploy/kw/README.md)", k)
		}
	}
	if !strings.HasPrefix(e.apiURL, "https://") {
		t.Fatalf("NEXORA_KW_API_URL must be https (session cookies are Secure): %s", e.apiURL)
	}
	var err error
	if e.engines, err = strconv.Atoi(os.Getenv("NEXORA_KW_ENGINES")); err != nil || e.engines < 1 {
		t.Fatalf("NEXORA_KW_ENGINES must be a positive integer: %v", err)
	}
	e.dnsRoots = certPool(t, os.Getenv("NEXORA_KW_CA_FILE"))
	// the cluster CA that issued the ingress certificate; the system roots when unset
	if f := os.Getenv("NEXORA_KW_API_CA_FILE"); f != "" {
		e.apiRoots = certPool(t, f)
	}
	raw, err := os.ReadFile(os.Getenv("NEXORA_KW_ADMIN_PASSWORD_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	e.password = strings.TrimSpace(string(raw))
	return e
}

func certPool(t *testing.T, file string) *x509.CertPool {
	t.Helper()
	pemBytes, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pemBytes) {
		t.Fatalf("%s holds no PEM certificate", file)
	}
	return roots
}

// TestKwSmoke checks the kw deployment from inside the cluster network: HTTPS-only management with
// Secure cookies, stamped versions, one connected engine per node, forwarding and blocking over
// UDP/TCP, DoT/DoH/DoQ on the DNS LoadBalancer, real client addresses in the query log, and
// per-client policy with rewrites; then M3's DNSSEC validation, recursion, RPZ and metrics (kwSmokeM3).
func TestKwSmoke(t *testing.T) {
	env := loadKwEnv(t)
	hc := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: env.apiRoots, MinVersion: tls.VersionTLS12}},
		// redirects are asserted, never followed
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	resp, err := hc.Get(env.apiURL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	var health map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&health)
	resp.Body.Close()
	if resp.StatusCode != 200 || health["status"] != "ok" || health["database"] != "ok" {
		t.Fatalf("health: %d %v", resp.StatusCode, health)
	}
	version := health["version"]
	if version == "" || version == "dev" {
		t.Fatalf("nexora-mgmt version is not stamped: %q", version)
	}

	resp, err = hc.Get(env.apiURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(page), `id="root"`) {
		t.Fatalf("GUI not served: %d", resp.StatusCode)
	}

	t.Run("http-redirects-to-https", func(t *testing.T) {
		plain := "http://" + strings.TrimPrefix(env.apiURL, "https://") + "/api/v1/health"
		resp, err := hc.Get(plain)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusPermanentRedirect || !strings.HasPrefix(loc, "https://") {
			t.Fatalf("GET %s = %d Location %q, want 308 to https", plain, resp.StatusCode, loc)
		}
	})

	t.Run("management-lb-has-no-cleartext-http", func(t *testing.T) {
		if c, err := net.DialTimeout("tcp", net.JoinHostPort(env.mgmtLBIP, "80"), 3*time.Second); err == nil {
			c.Close()
			t.Fatalf("%s:80 accepts connections; the GUI must only be reachable over TLS", env.mgmtLBIP)
		}
		c, err := net.DialTimeout("tcp", net.JoinHostPort(env.mgmtLBIP, "9443"), 3*time.Second)
		if err != nil {
			t.Fatalf("engine gRPC on %s:9443: %v", env.mgmtLBIP, err)
		}
		c.Close()
	})

	api := kwLogin(t, env)

	deadline := time.Now().Add(90 * time.Second)
	want := "\nnexora_fleet_engines_connected " + strconv.Itoa(env.engines) + "\n"
	for {
		resp, err = hc.Get(env.apiURL + "/metrics")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if strings.Contains(string(body), want) {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d engines are not connected to the management plane", env.engines)
		}
		time.Sleep(2 * time.Second)
	}

	t.Run("engine-version-stamped", func(t *testing.T) {
		for _, e := range kwConnectedEngines(t, api) {
			if e.EngineVersion != version {
				t.Errorf("engine %s runs version %q, want %q (the deployed tag)", e.NodeName, e.EngineVersion, version)
			}
		}
	})

	for _, network := range []string{"udp", "tcp"} {
		c := &dns.Client{Net: network, Timeout: 3 * time.Second}
		m := new(dns.Msg)
		m.SetQuestion("example.com.", dns.TypeA)
		first, _, err := c.Exchange(m, env.dnsAddr)
		if err != nil || first.Rcode != dns.RcodeSuccess || len(first.Answer) == 0 {
			t.Fatalf("%s query: %v %v", network, first, err)
		}
		time.Sleep(1100 * time.Millisecond)
		second, _, err := c.Exchange(m, env.dnsAddr)
		if err != nil || second.Rcode != dns.RcodeSuccess || len(second.Answer) == 0 {
			t.Fatalf("%s second query: %v %v", network, second, err)
		}
		if second.Answer[0].Header().Ttl > first.Answer[0].Header().Ttl {
			t.Fatalf("%s TTL grew between queries: %d -> %d", network, first.Answer[0].Header().Ttl, second.Answer[0].Header().Ttl)
		}

		b := new(dns.Msg)
		b.SetQuestion(kwBlockedName, dns.TypeA)
		blocked, _, err := c.Exchange(b, env.dnsAddr)
		if err != nil || blocked.Rcode != dns.RcodeSuccess || len(blocked.Answer) == 0 {
			t.Fatalf("%s blocked query: %v %v", network, blocked, err)
		}
		if a, ok := blocked.Answer[0].(*dns.A); !ok || !a.A.Equal(net.IPv4zero) {
			t.Fatalf("%s %s was not blocked: %v", network, kwBlockedName, blocked.Answer)
		}
	}

	enc := harness.EncryptedClient{RootCAs: env.dnsRoots, ServerName: env.tlsName}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dotAddr, doqAddr := net.JoinHostPort(env.encAddr, "853"), net.JoinHostPort(env.encAddr, "853")
	dohURL := "https://" + net.JoinHostPort(env.encAddr, "443") + "/dns-query"
	q := func() *dns.Msg { return question("example.com.", dns.TypeA) }
	ok := func(t *testing.T, m *dns.Msg, err error) {
		t.Helper()
		if err != nil || m.Rcode != dns.RcodeSuccess || len(m.Answer) == 0 {
			t.Fatalf("answer %v err %v", m, err)
		}
	}
	t.Run("dot", func(t *testing.T) {
		m, _, err := enc.DoT(ctx, dotAddr, q())
		ok(t, m, err)
	})
	t.Run("doh-get", func(t *testing.T) {
		m, _, err := harness.DoH(ctx, enc.HTTPClient(), dohURL, http.MethodGet, q())
		ok(t, m, err)
	})
	t.Run("doh-post", func(t *testing.T) {
		m, _, err := harness.DoH(ctx, enc.HTTPClient(), dohURL, http.MethodPost, q())
		ok(t, m, err)
	})
	t.Run("doq", func(t *testing.T) {
		m, _, err := enc.DoQ(ctx, doqAddr, q())
		ok(t, m, err)
	})

	// The address this pod queries the LoadBalancer from; engines must see exactly this address.
	clientIP := kwLocalIP(t, env.dnsAddr)

	t.Run("query-log-records-client-address", func(t *testing.T) {
		udpName, dohName := kwUniqueName("kw-client-udp"), kwUniqueName("kw-client-doh")
		if _, _, err := (&dns.Client{Net: "udp", Timeout: 3 * time.Second}).Exchange(question(udpName, dns.TypeA), env.dnsAddr); err != nil {
			t.Fatal(err)
		}
		if _, _, err := harness.DoH(ctx, enc.HTTPClient(), dohURL, http.MethodPost, question(dohName, dns.TypeA)); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{udpName, dohName} {
			var page struct {
				Records []struct {
					Client    string `json:"client"`
					Name      string `json:"name"`
					Transport string `json:"transport"`
				} `json:"records"`
			}
			harness.EventuallyTrue(t, 60*time.Second, func() bool {
				code, _ := api.Do(http.MethodGet, "/query-log?name="+url.QueryEscape(strings.TrimSuffix(name, ".")), nil, &page)
				return code == http.StatusOK && len(page.Records) > 0
			}, "query log lists "+name)
			for _, r := range page.Records {
				if r.Client != clientIP {
					t.Errorf("query log for %s (%s) records client %q, want this pod's address %s", r.Name, r.Transport, r.Client, clientIP)
				}
			}
		}
	})

	t.Run("per-client-policy-and-rewrites", func(t *testing.T) {
		kwRemoveSmokePolicy(t, api)
		t.Cleanup(func() { kwRemoveSmokePolicy(t, api) })
		a := func(name string) string {
			m, _, err := (&dns.Client{Net: "udp", Timeout: 3 * time.Second}).Exchange(question(name, dns.TypeA), env.dnsAddr)
			if err != nil {
				t.Fatalf("query %s: %v", name, err)
			}
			return firstA(m)
		}

		api.Must(http.MethodPost, "/rewrites", map[string]any{"group_id": nil, "name": "global.nexora-smoke.test", "type": "A", "value": "192.0.2.53", "ttl": 60}, nil, http.StatusCreated)
		kwWaitApplied(t, api, env.engines)
		if got := a("global.nexora-smoke.test."); got != "192.0.2.53" {
			t.Fatalf("global rewrite = %q, want 192.0.2.53", got)
		}

		var group struct {
			ID string `json:"id"`
		}
		api.Must(http.MethodPost, "/policy-groups", map[string]any{"name": kwSmokeGroup, "cidrs": []string{clientIP + "/32"}}, &group, http.StatusCreated)
		api.Must(http.MethodPost, "/rewrites", map[string]any{"group_id": group.ID, "name": "group.nexora-smoke.test", "type": "A", "value": "192.0.2.54", "ttl": 60}, nil, http.StatusCreated)
		kwWaitApplied(t, api, env.engines)

		// this pod's /32 group selects no block list and has its own rewrites, replacing the global ones
		if got := a(kwBlockedName); got == "0.0.0.0" {
			t.Errorf("%s is still blocked for a client in a group without block lists", kwBlockedName)
		}
		if got := a("global.nexora-smoke.test."); got == "192.0.2.53" {
			t.Errorf("group client got the global rewrite %q; a group replaces global rewrites", got)
		}
		gq := question("group.nexora-smoke.test.", dns.TypeA)
		answers := map[string]func() (*dns.Msg, error){
			"udp": func() (*dns.Msg, error) {
				m, _, err := (&dns.Client{Net: "udp", Timeout: 3 * time.Second}).Exchange(gq, env.dnsAddr)
				return m, err
			},
			"tcp": func() (*dns.Msg, error) {
				m, _, err := (&dns.Client{Net: "tcp", Timeout: 3 * time.Second}).Exchange(gq, env.dnsAddr)
				return m, err
			},
			"dot": func() (*dns.Msg, error) { m, _, err := enc.DoT(ctx, dotAddr, gq); return m, err },
			"doh": func() (*dns.Msg, error) {
				m, _, err := harness.DoH(ctx, enc.HTTPClient(), dohURL, http.MethodPost, gq)
				return m, err
			},
			"doq": func() (*dns.Msg, error) { m, _, err := enc.DoQ(ctx, doqAddr, gq); return m, err },
		}
		for transport, exchange := range answers {
			m, err := exchange()
			if err != nil {
				t.Errorf("%s group rewrite: %v", transport, err)
				continue
			}
			if got := firstA(m); got != "192.0.2.54" {
				t.Errorf("%s group rewrite = %q, want 192.0.2.54", transport, got)
			}
		}
	})

	kwSmokeM3(t, env, api)
}

// kwRPZBlocked is answered NXDOMAIN by the RPZ file zone rpz.kw.nexora. that deploy/kw/bootstrap.sh
// uploads.
const kwRPZBlocked = "example.net."

// kwSmokeM3 checks M3 on kw: forward mode with DNSSEC validation of forwarded answers (the kw
// deployment's settings), recursion from the root servers where kw's network allows it, the root
// trust anchor state, RPZ and the M3 engine metrics. It restores the resolution settings it changes.
func kwSmokeM3(t *testing.T, env kwEnv, api *harness.API) {
	ask := func(name string, qtype uint16, o qopt) (*dns.Msg, error) {
		return queryErr(env.dnsAddr, name, qtype, o)
	}
	setMode := func(mode string) {
		var cur map[string]any
		api.Must(http.MethodGet, "/resolution", nil, &cur, http.StatusOK)
		if cur["mode"] == mode {
			return
		}
		cur["mode"] = mode
		api.Must(http.MethodPut, "/resolution", cur, nil, http.StatusOK)
		kwWaitApplied(t, api, env.engines)
	}
	var original map[string]any
	api.Must(http.MethodGet, "/resolution", nil, &original, http.StatusOK)
	t.Cleanup(func() { setMode(original["mode"].(string)) })

	// Runs first, in the deployed mode, before any other M3 query can fill the caches.
	t.Run("dnssec-forwarded", func(t *testing.T) {
		var ds map[string]any
		api.Must(http.MethodGet, "/dnssec/settings", nil, &ds, http.StatusOK)
		if original["mode"] != "forward" || ds["validation"] != true || ds["validate_forwarded"] != true {
			t.Fatalf("kw must run forward mode with validation and validate_forwarded on: mode=%v dnssec=%v", original["mode"], ds)
		}
		for _, name := range []string{"www.iana.org.", "cloudflare.com."} {
			harness.Eventually(t, 30*time.Second, func() error {
				r, err := ask(name, dns.TypeA, qopt{DO: true})
				if err != nil {
					return err
				}
				if r.Rcode != dns.RcodeSuccess || !r.AuthenticatedData {
					return fmt.Errorf("%s: rcode %s AD=%v, want NOERROR with AD", name, dns.RcodeToString[r.Rcode], r.AuthenticatedData)
				}
				return nil
			})
		}
		kwWantBogus(t, env.dnsAddr, "dnssec-failed.org.")
	})

	t.Run("recursion", func(t *testing.T) {
		// A root server answers a non-recursive query for a TLD name with a referral; an answer means
		// something on the path redirects outbound DNS to a resolver (kw's network does).
		probe := new(dns.Msg)
		probe.SetQuestion("example.com.", dns.TypeA)
		probe.RecursionDesired = false
		if r, _, err := (&dns.Client{Timeout: 3 * time.Second}).Exchange(probe, "198.41.0.4:53"); err != nil {
			t.Fatalf("no reply from a.root-servers.net: %v", err)
		} else if len(r.Answer) > 0 || r.RecursionAvailable {
			t.Skip("outbound DNS from the cluster is redirected to a resolver (a.root-servers.net answered recursively), so recursion from the real root servers cannot be checked on kw")
		}
		setMode("recursive")
		defer setMode(original["mode"].(string))
		harness.Eventually(t, 60*time.Second, func() error {
			r, err := ask("www.isc.org.", dns.TypeA, qopt{DO: true})
			if err != nil {
				return err
			}
			if r.Rcode != dns.RcodeSuccess || len(r.Answer) == 0 || !r.AuthenticatedData {
				return fmt.Errorf("www.isc.org: rcode %s answers %d AD=%v", dns.RcodeToString[r.Rcode], len(r.Answer), r.AuthenticatedData)
			}
			return nil
		})
		kwWantBogus(t, env.dnsAddr, "sigfail.verteiltesysteme.net.")
	})

	t.Run("trust-anchors", func(t *testing.T) {
		var st struct {
			Engines []struct {
				EngineName   string `json:"engine_name"`
				TrustAnchors []struct {
					Zone  string `json:"zone"`
					State string `json:"state"`
				} `json:"trust_anchors"`
			} `json:"engines"`
		}
		harness.Eventually(t, 60*time.Second, func() error {
			if _, err := api.Do(http.MethodGet, "/dnssec/status", nil, &st); err != nil {
				return err
			}
			usable := 0
			for _, e := range st.Engines {
				for _, a := range e.TrustAnchors {
					if a.Zone == "." && (a.State == "valid" || a.State == "configured") {
						usable++
						break
					}
				}
			}
			if usable < env.engines {
				return fmt.Errorf("%d of %d engines report a usable root trust anchor: %+v", usable, env.engines, st.Engines)
			}
			return nil
		})
	})

	t.Run("rpz", func(t *testing.T) {
		wantRcode := func(name string, rcode int) {
			t.Helper()
			harness.Eventually(t, 60*time.Second, func() error {
				r, err := ask(name, dns.TypeA, qopt{})
				if err != nil {
					return err
				}
				if r.Rcode != rcode {
					return fmt.Errorf("%s: rcode %s, want %s", name, dns.RcodeToString[r.Rcode], dns.RcodeToString[rcode])
				}
				return nil
			})
		}
		wantRcode(kwRPZBlocked, dns.RcodeNameError)
		wantRcode("example.org.", dns.RcodeSuccess)

		// A zone added at run time reaches every engine and its removal lifts the block.
		const name = "rpz.kwsmoke.nexora."
		var z struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Revision int64  `json:"revision"`
		}
		var zones []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Revision int64  `json:"revision"`
		}
		api.Must(http.MethodGet, "/rpz-zones", nil, &zones, http.StatusOK)
		for _, old := range zones { // left behind by an interrupted run
			if old.Name == name {
				api.Must(http.MethodDelete, fmt.Sprintf("/rpz-zones/%s?revision=%d", old.ID, old.Revision), nil, nil, http.StatusNoContent)
			}
		}
		api.Must(http.MethodPost, "/rpz-zones", map[string]any{"name": name, "source_type": "file", "policy_override": "given", "min_refresh_seconds": 60}, &z, http.StatusCreated)
		deleted := false
		t.Cleanup(func() {
			if !deleted {
				api.Must(http.MethodDelete, fmt.Sprintf("/rpz-zones/%s?revision=%d", z.ID, z.Revision), nil, nil, http.StatusNoContent)
			}
		})
		zone := "$TTL 60\n@ SOA ns.rpz.kwsmoke.nexora. h.rpz.kwsmoke.nexora. 1 60 60 86400 60\n@ NS ns.rpz.kwsmoke.nexora.\nexample.org CNAME .\n"
		api.Must(http.MethodPut, "/rpz-zones/"+z.ID+"/file", map[string]any{"content": zone, "revision": z.Revision}, &z, http.StatusOK)
		kwWaitApplied(t, api, env.engines)
		wantRcode("example.org.", dns.RcodeNameError)
		api.Must(http.MethodDelete, fmt.Sprintf("/rpz-zones/%s?revision=%d", z.ID, z.Revision), nil, nil, http.StatusNoContent)
		deleted = true
		kwWaitApplied(t, api, env.engines)
		wantRcode("example.org.", dns.RcodeSuccess)
	})

	t.Run("m3-metrics", func(t *testing.T) {
		metricsURL := os.Getenv("NEXORA_KW_ENGINE_METRICS_URL")
		if metricsURL == "" {
			metricsURL = "http://nexora-engine-metrics.nexora.svc.cluster.local:9153/metrics"
		}
		// The Service picks an engine per connection; every engine exports these once it has answered
		// and validated a query and loaded the RPZ zone and trust anchors.
		want := []string{"nexora_resolutions", "nexora_dnssec_validations", "nexora_rpz_zone_serial{zone=\"rpz.kw.nexora.\"}", "nexora_dnssec_trust_anchor_keys"}
		hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
		harness.Eventually(t, 30*time.Second, func() error {
			resp, err := hc.Get(metricsURL)
			if err != nil {
				return err
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			for _, name := range want {
				if !strings.Contains(string(body), "\n"+name) {
					return fmt.Errorf("%s lacks %s", metricsURL, name)
				}
			}
			return nil
		})
	})
}

// kwWantBogus asserts name is bogus: SERVFAIL with DO, but an answer with CD (validation off).
func kwWantBogus(t *testing.T, dnsAddr, name string) {
	t.Helper()
	if r, err := queryErr(dnsAddr, name, dns.TypeA, qopt{DO: true}); err != nil || r.Rcode != dns.RcodeServerFailure {
		t.Fatalf("%s: %v %v, want SERVFAIL", name, r, err)
	}
	if r, err := queryErr(dnsAddr, name, dns.TypeA, qopt{DO: true, CD: true}); err != nil || r.Rcode != dns.RcodeSuccess || len(r.Answer) == 0 {
		t.Fatalf("%s with CD: %v %v, want an answer", name, r, err)
	}
}

// kwLogin signs in as the bootstrap admin over HTTPS and checks the session cookie is Secure.
func kwLogin(t *testing.T, env kwEnv) *harness.API {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	api := &harness.API{T: t, Base: env.apiURL, HC: &http.Client{
		Jar: jar, Timeout: 30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: env.apiRoots, MinVersion: tls.VersionTLS12}},
	}}
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": env.password})
	req, err := http.NewRequest(http.MethodPost, env.apiURL+"/api/v1/auth/login", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := api.HC.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		t.Fatal("login set no cookie")
	}
	for _, c := range cookies {
		if !c.Secure || !c.HttpOnly {
			t.Fatalf("cookie %s: Secure=%v HttpOnly=%v, want both", c.Name, c.Secure, c.HttpOnly)
		}
	}
	return api
}

type kwEngine struct {
	NodeName       string `json:"node_name"`
	Connected      bool   `json:"connected"`
	AppliedVersion uint64 `json:"applied_version"`
	EngineVersion  string `json:"engine_version"`
}

func kwConnectedEngines(t *testing.T, api *harness.API) []kwEngine {
	t.Helper()
	var all, connected []kwEngine
	api.Must(http.MethodGet, "/engines", nil, &all, http.StatusOK)
	for _, e := range all {
		if e.Connected {
			connected = append(connected, e)
		}
	}
	return connected
}

// kwWaitApplied waits until the expected number of connected engines serve the newest config.
func kwWaitApplied(t *testing.T, api *harness.API, engines int) {
	t.Helper()
	v := api.LatestVersion()
	harness.EventuallyTrue(t, 60*time.Second, func() bool {
		connected := kwConnectedEngines(t, api)
		for _, e := range connected {
			if e.AppliedVersion < v {
				return false
			}
		}
		return len(connected) == engines
	}, "every engine applies config version "+strconv.FormatUint(v, 10))
}

// kwRemoveSmokePolicy deletes the smoke group (its rewrites go with it) and the global smoke rewrites.
func kwRemoveSmokePolicy(t *testing.T, api *harness.API) {
	t.Helper()
	var groups []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Revision int64  `json:"revision"`
	}
	api.Must(http.MethodGet, "/policy-groups", nil, &groups, http.StatusOK)
	for _, g := range groups {
		if g.Name == kwSmokeGroup {
			api.Must(http.MethodDelete, "/policy-groups/"+g.ID+"?revision="+strconv.FormatInt(g.Revision, 10), nil, nil, http.StatusNoContent)
		}
	}
	var rewrites []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Revision int64  `json:"revision"`
	}
	api.Must(http.MethodGet, "/rewrites?scope=global", nil, &rewrites, http.StatusOK)
	for _, r := range rewrites {
		if strings.HasSuffix(r.Name, ".nexora-smoke.test") {
			api.Must(http.MethodDelete, "/rewrites/"+r.ID+"?revision="+strconv.FormatInt(r.Revision, 10), nil, nil, http.StatusNoContent)
		}
	}
}

// kwLocalIP returns the source address this host uses towards addr.
func kwLocalIP(t *testing.T, addr string) string {
	t.Helper()
	c, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ua, isUDP := c.LocalAddr().(*net.UDPAddr)
	if !isUDP {
		t.Fatal(errors.New("no UDP local address"))
	}
	return ua.IP.String()
}

func kwUniqueName(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b) + ".nexora-smoke.test."
}
