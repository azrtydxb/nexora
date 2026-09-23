package e2e

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

// rpzZonemdText is rpz.test. at serial with the given policy lines and no ZONEMD.
func rpzZonemdText(serial int, policies ...string) string {
	return fmt.Sprintf("$ORIGIN rpz.test.\n$TTL 60\n@ SOA ns.rpz.test. h.rpz.test. %d 2 1 30 60\n@ NS ns.rpz.test.\nns A 127.0.0.1\n%s\n", serial, strings.Join(policies, "\n"))
}

// withZonemd adds a SIMPLE SHA-384 ZONEMD to zoneText with ldns-signzone, an implementation
// independent of the management plane and the engine (e2e cannot import mgmt/internal/zonemd).
func withZonemd(t *testing.T, zoneText string) string {
	t.Helper()
	dir := t.TempDir()
	in, out := filepath.Join(dir, "zone"), filepath.Join(dir, "zone.zonemd")
	if err := os.WriteFile(in, []byte(zoneText), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command("ldns-signzone", "-Z", "-z", "simple:sha384", "-f", out, in).CombinedOutput(); err != nil {
		t.Fatalf("ldns-signzone: %v\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// zonemdLine returns the apex ZONEMD line of a zone written by withZonemd.
func zonemdLine(t *testing.T, zoneText string) string {
	t.Helper()
	for _, line := range strings.Split(zoneText, "\n") {
		if f := strings.Fields(line); len(f) > 3 && f[0] == "rpz.test." && f[3] == "ZONEMD" {
			return line
		}
	}
	t.Fatalf("no apex ZONEMD in:\n%s", zoneText)
	return ""
}

type rpzZonemdView struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Status   []struct {
		Serial      int64  `json:"serial"`
		LastError   string `json:"last_error"`
		Zonemd      string `json:"zonemd"`
		ZonemdError string `json:"zonemd_error"`
	} `json:"status"`
}

func TestRPZZonemdVerification(t *testing.T) {
	r := setupRecursion(t)
	addr := r.eng.DNS
	const bad, worse = "www.plain.test", "mail.plain.test"
	blocked := func(name string) error {
		m, err := queryErr(addr, name, dns.TypeA, qopt{})
		if err != nil {
			return err
		}
		if code, ok := edeCode(m); m.Rcode != dns.RcodeNameError || !ok || code != 15 {
			return fmt.Errorf("%s: rcode %s EDE %d %v, want NXDOMAIN with EDE 15", name, dns.RcodeToString[m.Rcode], code, ok)
		}
		return nil
	}
	// positive path before any policy exists: both names resolve
	wantA(t, query(t, addr, bad, dns.TypeA, qopt{}), "192.0.2.12")
	wantA(t, query(t, addr, worse, dns.TypeA, qopt{}), "192.0.2.13")

	v1 := withZonemd(t, rpzZonemdText(1, bad+" CNAME ."))
	named := r.env.StartNamed("rpz.test.", v1)
	var z rpzZonemdView
	r.api.Must(http.MethodPost, "/rpz-zones", map[string]any{"name": "rpz.test.", "source_type": "transfer", "primary": named.Addr,
		"tsig_key_name": named.KeyName, "tsig_algorithm": "hmac-sha256", "tsig_secret": named.KeySecretB64,
		"policy_override": "given", "min_refresh_seconds": 1, "zonemd_verify": "if_present"}, &z, http.StatusCreated)
	waitStatus := func(stage string, ok func(serial int64, zonemd, zonemdErr, lastErr string) bool) {
		t.Helper()
		harness.Eventually(t, 30*time.Second, func() error {
			var got rpzZonemdView
			if _, err := r.api.Do(http.MethodGet, "/rpz-zones/"+z.ID, nil, &got); err != nil {
				return err
			}
			if len(got.Status) != 1 {
				return fmt.Errorf("%s: status %+v", stage, got.Status)
			}
			s := got.Status[0]
			if !ok(s.Serial, s.Zonemd, s.ZonemdError, s.LastError) {
				return fmt.Errorf("%s: status %+v", stage, s)
			}
			return nil
		})
	}
	update := func(mode string) {
		t.Helper()
		var cur rpzZonemdView
		r.api.Must(http.MethodGet, "/rpz-zones/"+z.ID, nil, &cur, http.StatusOK)
		r.api.Must(http.MethodPut, "/rpz-zones/"+z.ID, map[string]any{"primary": named.Addr, "tsig_key_name": named.KeyName, "tsig_algorithm": "hmac-sha256",
			"policy_override": "given", "min_refresh_seconds": 1, "zonemd_verify": mode, "revision": cur.Revision}, nil, http.StatusOK)
		waitApplied(t, r.api)
	}
	waitApplied(t, r.api)
	waitStatus("verified", func(serial int64, zonemd, _, _ string) bool { return serial == 1 && zonemd == "verified" })
	harness.Eventually(t, 10*time.Second, func() error { return blocked(bad) })

	// serial 2 adds a policy but keeps serial 1's ZONEMD: the transfer is refused, the last good copy stays
	named.UpdateZone(t, rpzZonemdText(2, bad+" CNAME .", worse+" CNAME .", zonemdLine(t, v1)))
	r.api.Must(http.MethodPost, "/rpz-zones/"+z.ID+"/refresh", map[string]any{}, nil, http.StatusAccepted)
	waitStatus("failed", func(serial int64, zonemd, zonemdErr, lastErr string) bool {
		return serial == 1 && zonemd == "failed" && zonemdErr != "" && strings.Contains(lastErr, "zonemd:")
	})
	wantA(t, query(t, addr, worse, dns.TypeA, qopt{}), "192.0.2.13")
	if err := blocked(bad); err != nil {
		t.Fatal(err)
	}

	// required, against serial 3 without any ZONEMD: still refused. The mode is applied before the
	// zone changes, so the timer refresh under if_present cannot load serial 3 first.
	update("required")
	named.UpdateZone(t, rpzZonemdText(3, bad+" CNAME .", worse+" CNAME ."))
	r.api.Must(http.MethodPost, "/rpz-zones/"+z.ID+"/refresh", map[string]any{}, nil, http.StatusAccepted)
	waitStatus("required", func(serial int64, zonemd, zonemdErr, _ string) bool {
		return serial == 1 && zonemd == "failed" && strings.Contains(zonemdErr, "no apex ZONEMD")
	})
	wantA(t, query(t, addr, worse, dns.TypeA, qopt{}), "192.0.2.13")
	if err := blocked(bad); err != nil {
		t.Fatal(err)
	}

	// off applies serial 3
	update("off")
	r.api.Must(http.MethodPost, "/rpz-zones/"+z.ID+"/refresh", map[string]any{}, nil, http.StatusAccepted)
	waitStatus("off", func(serial int64, zonemd, _, _ string) bool { return serial == 3 && zonemd == "off" })
	harness.Eventually(t, 10*time.Second, func() error { return blocked(worse) })
}
