package e2e

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

func TestDNSSECValidation(t *testing.T) {
	r := setupRecursion(t)
	addr := r.eng.DNS

	t.Run("signed zone returns AD", func(t *testing.T) {
		m := query(t, addr, "www.good.test", dns.TypeA, qopt{DO: true})
		wantA(t, m, "192.0.2.10")
		if !m.AuthenticatedData {
			t.Fatal("AD=0 for a secure answer")
		}
		if n := query(t, addr, "www.n3.test", dns.TypeA, qopt{DO: true}); !n.AuthenticatedData {
			t.Fatal("AD=0 for NSEC3-signed zone")
		}
		nx := query(t, addr, "nope.good.test", dns.TypeA, qopt{DO: true})
		if nx.Rcode != dns.RcodeNameError || !nx.AuthenticatedData {
			t.Fatalf("NXDOMAIN proof: rcode=%s ad=%v", dns.RcodeToString[nx.Rcode], nx.AuthenticatedData)
		}
	})
	t.Run("wildcard answer validates with AD", func(t *testing.T) {
		m := query(t, addr, "x.w.good.test", dns.TypeA, qopt{DO: true})
		wantA(t, m, "192.0.2.60")
		if !m.AuthenticatedData {
			t.Fatal("AD=0 for a validated NSEC wildcard answer")
		}
		n := query(t, addr, "x.w.n3.test", dns.TypeA, qopt{DO: true})
		wantA(t, n, "192.0.2.61")
		if !n.AuthenticatedData {
			t.Fatal("AD=0 for a validated NSEC3 wildcard answer")
		}
	})
	t.Run("wildcard NODATA is proven", func(t *testing.T) {
		m := query(t, addr, "x.w.good.test", dns.TypeAAAA, qopt{DO: true})
		if m.Rcode != dns.RcodeSuccess || len(m.Answer) != 0 || !m.AuthenticatedData {
			t.Fatalf("wildcard NODATA: rcode=%s answers=%d ad=%v", dns.RcodeToString[m.Rcode], len(m.Answer), m.AuthenticatedData)
		}
	})
	t.Run("wildcard answer without next-closer proof is bogus", func(t *testing.T) {
		// Positive path first: the zone itself validates.
		if ns := query(t, addr, "ns.wild.test", dns.TypeA, qopt{DO: true}); !ns.AuthenticatedData {
			t.Fatal("wild.test is not a secure zone; the negative check below would prove nothing")
		}
		m := query(t, addr, "x.w.wild.test", dns.TypeA, qopt{DO: true})
		if m.Rcode != dns.RcodeServerFailure || len(aValues(m)) != 0 {
			t.Fatalf("unproven wildcard served: rcode=%s answers=%v", dns.RcodeToString[m.Rcode], aValues(m))
		}
		if code, ok := edeCode(m); !ok || code != 12 {
			t.Fatalf("EDE = %d (present %v), want 12", code, ok)
		}
	})
	t.Run("broken signature returns SERVFAIL with EDE", func(t *testing.T) {
		m := query(t, addr, "www.bad.test", dns.TypeA, qopt{DO: true})
		if m.Rcode != dns.RcodeServerFailure || len(aValues(m)) != 0 {
			t.Fatalf("bogus data served: rcode=%s answers=%v", dns.RcodeToString[m.Rcode], aValues(m))
		}
		if code, ok := edeCode(m); !ok || code != 6 {
			t.Fatalf("EDE = %d (present %v), want 6", code, ok)
		}
		if r.eng.Metric(t, "nexora_dnssec_validations_total", map[string]string{"result": "bogus"}) < 1 {
			t.Fatal("bogus validation not counted")
		}
	})
	t.Run("CD bit returns unvalidated data without AD", func(t *testing.T) {
		m := query(t, addr, "www.bad.test", dns.TypeA, qopt{DO: true, CD: true})
		wantA(t, m, "192.0.2.11")
		if m.AuthenticatedData {
			t.Fatal("AD=1 with CD=1 on bogus data")
		}
		// The hierarchy's authoritative servers answer with AA=1; a recursor never relays it.
		if m.Authoritative {
			t.Fatal("AA=1 on a recursive CD pass-through answer")
		}
	})
	t.Run("unsigned zone returns AD=0", func(t *testing.T) {
		m := query(t, addr, "www.plain.test", dns.TypeA, qopt{DO: true})
		wantA(t, m, "192.0.2.12")
		if m.AuthenticatedData {
			t.Fatal("AD=1 for an insecure answer")
		}
	})
	t.Run("negative trust anchor", func(t *testing.T) {
		var nta struct {
			ID string `json:"id"`
		}
		r.api.Must("POST", "/dnssec/negative-trust-anchors", map[string]any{"domain": "bad.test.", "reason": "e2e", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}, &nta, 201)
		waitApplied(t, r.api)
		m := query(t, addr, "www.bad.test", dns.TypeA, qopt{DO: true})
		wantA(t, m, "192.0.2.11")
		if m.AuthenticatedData {
			t.Fatal("AD=1 under an NTA")
		}
		r.api.Must("DELETE", "/dnssec/negative-trust-anchors/"+nta.ID, nil, nil, 204)
		waitApplied(t, r.api)
		if m := query(t, addr, "www.bad.test", dns.TypeA, qopt{DO: true}); m.Rcode != dns.RcodeServerFailure {
			t.Fatalf("after NTA removal rcode = %s, want SERVFAIL (cached insecure answer must not be reused)", dns.RcodeToString[m.Rcode])
		}
	})
	t.Run("forwarded answers are validated", func(t *testing.T) {
		var fz struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		}
		r.api.Must("POST", "/forward-zones", map[string]any{"domain": "test.", "addresses": []string{r.h.Ready.Forwarder}, "validate": true}, &fz, 201)
		waitApplied(t, r.api)
		before := r.eng.Metric(t, "nexora_resolutions_total", map[string]string{"route": "forward_zone"})
		good := query(t, addr, "www.good.test", dns.TypeA, qopt{DO: true})
		wantA(t, good, "192.0.2.10")
		if !good.AuthenticatedData {
			t.Fatal("forwarded secure answer lacks AD")
		}
		if bad := query(t, addr, "www.bad.test", dns.TypeA, qopt{DO: true}); bad.Rcode != dns.RcodeServerFailure {
			t.Fatalf("forwarded bogus answer rcode = %s", dns.RcodeToString[bad.Rcode])
		}
		if after := r.eng.Metric(t, "nexora_resolutions_total", map[string]string{"route": "forward_zone"}); after <= before {
			t.Fatalf("forward zone route not used (%v -> %v)", before, after)
		}
		r.api.Must("DELETE", fmt.Sprintf("/forward-zones/%s?revision=%d", fz.ID, fz.Revision), nil, nil, 204)
	})
	t.Run("forward mode validates answers from the global upstreams", func(t *testing.T) {
		var dsettings map[string]any
		r.api.Must("GET", "/dnssec/settings", nil, &dsettings, 200)
		if dsettings["validate_forwarded"] != true {
			t.Fatalf("validate_forwarded = %v, want the default true", dsettings["validate_forwarded"])
		}
		var res map[string]any
		r.api.Must("GET", "/resolution", nil, &res, 200)
		res["mode"] = "forward"
		r.api.Must("PUT", "/resolution", res, &res, 200)
		var up struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		}
		r.api.Must("POST", "/upstreams", map[string]any{"name": "hierarchy-forwarder", "protocol": "udp", "address": r.h.Ready.Forwarder, "timeout_ms": 1000, "enabled": true, "position": 0}, &up, 201)
		waitApplied(t, r.api)
		t.Cleanup(func() {
			r.api.Must("DELETE", fmt.Sprintf("/upstreams/%s?revision=%d", up.ID, up.Revision), nil, nil, 204)
			res["mode"] = "recursive"
			r.api.Must("PUT", "/resolution", res, nil, 200)
			waitApplied(t, r.api)
		})
		forwarderIP, _, _ := net.SplitHostPort(r.h.Ready.Forwarder)
		before := r.h.Stats(t).Queries[forwarderIP]
		good := query(t, addr, "www.good.test", dns.TypeA, qopt{DO: true})
		wantA(t, good, "192.0.2.10")
		if !good.AuthenticatedData {
			t.Fatal("forward mode: secure answer lacks AD")
		}
		if r.h.Stats(t).Queries[forwarderIP] <= before {
			t.Fatal("forward mode: the global upstream was never queried")
		}
		bad := query(t, addr, "www.bad.test", dns.TypeA, qopt{DO: true})
		if bad.Rcode != dns.RcodeServerFailure || len(aValues(bad)) != 0 {
			t.Fatalf("forward mode: bogus data served: rcode=%s answers=%v", dns.RcodeToString[bad.Rcode], aValues(bad))
		}
		if code, ok := edeCode(bad); !ok || code != 6 {
			t.Fatalf("forward mode: EDE = %d (present %v), want 6", code, ok)
		}
		if cd := query(t, addr, "www.bad.test", dns.TypeA, qopt{DO: true, CD: true}); len(aValues(cd)) == 0 || cd.AuthenticatedData {
			t.Fatalf("forward mode: CD=1 must return the unvalidated answer without AD: %v", cd)
		}
	})
	t.Run("status reports trust anchors", func(t *testing.T) {
		harness.Eventually(t, 30*time.Second, func() error {
			var st struct {
				Engines []struct {
					Secure       int64 `json:"secure"`
					TrustAnchors []struct {
						Zone string `json:"zone"`
					} `json:"trust_anchors"`
				} `json:"engines"`
			}
			if _, err := r.api.Do("GET", "/dnssec/status", nil, &st); err != nil {
				return err
			}
			if len(st.Engines) != 1 || st.Engines[0].Secure < 1 || len(st.Engines[0].TrustAnchors) < 1 {
				return fmt.Errorf("status = %+v", st)
			}
			return nil
		})
	})
}
