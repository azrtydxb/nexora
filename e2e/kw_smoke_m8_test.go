package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/e2e/harness"
)

// TestKwSmokeM8 is parent-operated live acceptance. It uses existing HTTPS
// authentication and independently pinned fleet identities. Only UUIDs created
// by this run are deleted; no old smoke objects, identities or trust are reset.
func TestKwSmokeM8(t *testing.T) {
	if os.Getenv("NEXORA_KW_M8") != "1" {
		t.Skip("set NEXORA_KW_M8=1 for parent-operated live M8 smoke")
	}
	if os.Getenv("NEXORA_KW_DNS_ADDR") == "" || os.Getenv("NEXORA_KW_API_URL") == "" {
		t.Fatal("M8 opt-in requires the kw harness environment")
	}
	env := loadKwEnv(t)
	if env.dnsAddr != "192.168.10.136:53" || env.secondDNSAddr != "192.168.10.139:53" {
		t.Fatal("M8 smoke requires both fixed kw DNS identities (.136 and .139)")
	}
	if _, err := exec.LookPath("ldns-verify-zone"); err != nil {
		t.Fatal(err)
	}
	expected, err := kwExpectedFleet(os.Getenv("NEXORA_KW_EXPECTED_ENGINES"), env.engines)
	if err != nil {
		t.Fatal(err)
	}
	api := kwLogin(t, env)
	waitFleet := func() {
		t.Helper()
		kwWaitApplied(t, api, env.engines)
		var fleet []kwFilterEngine
		api.Must(http.MethodGet, "/engines", nil, &fleet, http.StatusOK)
		if err := kwValidateFleet(expected, fleet, nil, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	waitFleet()
	t.Cleanup(waitFleet) // Last cleanup: require the fleet to apply scratch removals.
	// Scope transfers to this runner's observed source addresses; do not broaden
	// fleet ACLs or change the resolver mode to make the fixture work.
	cidrs := []string{kwLocalIP(t, env.dnsAddr) + "/32"}
	if second := kwLocalIP(t, env.secondDNSAddr) + "/32"; second != cidrs[0] {
		cidrs = append(cidrs, second)
	}
	transfer := map[string]any{"allow_cidrs": cidrs}
	var cat catalogView
	catName := kwUniqueName("m8-catalog")
	api.Must(http.MethodPost, "/catalog-zones", map[string]any{"name": catName, "role": "producer", "transfer": transfer}, &cat, http.StatusCreated)
	t.Cleanup(func() {
		api.Must(http.MethodDelete, "/catalog-zones/"+cat.ID, nil, nil, http.StatusNoContent)
		api.Must(http.MethodGet, "/catalog-zones/"+cat.ID, nil, nil, http.StatusNotFound)
		api.Must(http.MethodGet, "/zones/"+cat.ZoneID, nil, nil, http.StatusNotFound)
	})
	members := map[string]string{}
	var queryName string
	for _, signed := range []bool{false, true} {
		name := kwUniqueName(fmt.Sprintf("m8-signed-%t", signed))
		z := createPrimaryZone(t, api, name, map[string]any{"zonemd_generate": true, "transfer": transfer})
		t.Cleanup(func() {
			current := catalogZoneGet(t, api, z.ID)
			api.Must(http.MethodDelete, fmt.Sprintf("/zones/%s?revision=%d", z.ID, current.Revision), nil, nil, http.StatusNoContent)
			api.Must(http.MethodGet, "/zones/"+z.ID, nil, nil, http.StatusNotFound)
		})
		addRecord(t, api, z.ID, "www."+name, "A", "192.0.2.18")
		if signed {
			current := catalogZoneGet(t, api, z.ID)
			api.Must(http.MethodPut, "/zones/"+z.ID+"/dnssec", map[string]any{"revision": current.Revision, "enabled": true, "algorithm": 13, "nsec_mode": "nsec", "key_backend": "kek"}, nil, http.StatusOK)
		}
		current := catalogZoneGet(t, api, z.ID)
		api.Must(http.MethodPatch, "/zones/"+z.ID, map[string]any{"revision": current.Revision, "catalog_zone_id": cat.ID}, nil, http.StatusOK)
		waitFleet()
		// One query and one transfer: failures are evidence, never retried.
		r := harness.MustQuery(t, env.dnsAddr, name, dns.TypeZONEMD, harness.QueryOpts{TCP: true, DO: true, EDNSSize: 4096})
		md := apexZONEMD(r.Answer, name)
		if r.Rcode != dns.RcodeSuccess || !r.Authoritative || md == nil {
			t.Fatalf("ZONEMD query: %v", r)
		}
		rrs := transferZone(t, env.dnsAddr, name)
		transferred := apexZONEMD(rrs, name)
		if transferred == nil || transferred.Serial != apexSOA(t, rrs).Serial || transferred.Scheme != 1 || transferred.Hash != 1 {
			t.Fatalf("invalid AXFR ZONEMD: %v", transferred)
		}
		if signed && (!apexNSECHas(rrs, name, dns.TypeZONEMD) || !hasType(r.Answer, dns.TypeRRSIG)) {
			t.Fatal("signed ZONEMD lacks signature or denial bitmap")
		}
		if !signed && hasType(rrs, dns.TypeRRSIG) {
			t.Fatal("unsigned fixture unexpectedly signed")
		}
		ldnsVerify(t, rrs, signed)
		members[strings.ReplaceAll(z.ID, "-", "")+".zones."+catName] = name
		queryName = "www." + name
	}
	catalogWire(t, env.secondDNSAddr, catName, nil, members)
	kwM8MDNS(t, api)
	kwM8ODoH(t, api, env, queryName, waitFleet)
}

func kwM8MDNS(t *testing.T, api *harness.API) {
	t.Helper()
	g := api.CreateEngineGroup(map[string]any{"name": strings.TrimSuffix(kwUniqueName("m8-mdns"), ".")})
	t.Cleanup(func() {
		current := api.EngineGroup(g.ID)
		if current.EngineCount != 0 {
			t.Fatal("scratch mDNS group acquired engines; refusing deletion")
		}
		api.Must(http.MethodDelete, fmt.Sprintf("/engine-groups/%s?revision=%d", g.ID, current.Revision), nil, nil, http.StatusNoContent)
		api.Must(http.MethodGet, "/engine-groups/"+g.ID, nil, nil, http.StatusNotFound)
	})
	if g.EngineCount != 0 {
		t.Fatal("scratch mDNS group is not empty")
	}
	for _, enabled := range []bool{true, false} {
		want := map[string]any{"enabled": enabled, "interfaces": []any{"lan0"}, "timeout_ms": float64(700), "reflect": enabled, "reflect_interfaces": []any{"lan0", "lan1"}}
		current := api.EngineGroup(g.ID)
		if current.EngineCount != 0 {
			t.Fatal("scratch group acquired engines; refusing mDNS update")
		}
		api.Must(http.MethodPut, "/engine-groups/"+g.ID, map[string]any{"revision": current.Revision, "mdns": want}, nil, http.StatusOK)
		var got struct {
			EngineCount int            `json:"engine_count"`
			Mdns        map[string]any `json:"mdns"`
		}
		api.Must(http.MethodGet, "/engine-groups/"+g.ID, nil, &got, http.StatusOK)
		if got.EngineCount != 0 || !reflect.DeepEqual(got.Mdns, want) {
			t.Fatalf("empty-group mDNS roundtrip: %+v, want %v", got, want)
		}
	}
}

type kwM8OdohSettings struct {
	TargetEnabled    bool                `json:"target_enabled"`
	ProxyEnabled     bool                `json:"proxy_enabled"`
	ProxyTargets     []map[string]string `json:"proxy_targets"`
	ProxyTimeoutMS   int                 `json:"proxy_timeout_ms"`
	KeyRotationHours int                 `json:"key_rotation_hours"`
	Revision         int64               `json:"revision"`
	Keys             []struct {
		ID           string    `json:"id"`
		CreatedAt    time.Time `json:"created_at"`
		PublishAfter time.Time `json:"publish_after"`
	} `json:"keys"`
}

func (s kwM8OdohSettings) update() map[string]any {
	return map[string]any{"revision": s.Revision, "target_enabled": s.TargetEnabled, "proxy_enabled": s.ProxyEnabled, "proxy_targets": s.ProxyTargets, "proxy_timeout_ms": s.ProxyTimeoutMS, "key_rotation_hours": s.KeyRotationHours}
}

func kwM8ODoH(t *testing.T, api *harness.API, env kwEnv, name string, waitFleet func()) {
	t.Helper()
	var original, rotated, enabled kwM8OdohSettings
	api.Must(http.MethodGet, "/odoh", nil, &original, http.StatusOK)
	api.Must(http.MethodPost, "/odoh/rotate-key", nil, &rotated, http.StatusOK)
	if len(rotated.Keys) == 0 {
		t.Fatal("rotation returned no key")
	}
	key := rotated.Keys[0]
	for _, old := range original.Keys {
		if old.ID == key.ID {
			t.Fatal("rotation did not create a new key")
		}
	}
	if key.PublishAfter.Sub(key.CreatedAt) != 5*time.Minute {
		t.Fatal("ODoH publication delay is not five minutes")
	}
	body := original.update()
	body["target_enabled"] = true
	// Register restoration before enabling, but arm it only on success. Use only
	// our returned revision; concurrent settings edits must cause a conflict.
	restoreRevision := int64(0)
	t.Cleanup(func() {
		if restoreRevision == 0 {
			return
		} // Enabling did not return success; do not overwrite another writer.
		restore := original.update()
		restore["revision"] = restoreRevision
		api.Must(http.MethodPut, "/odoh", restore, nil, http.StatusOK)
		var got kwM8OdohSettings
		api.Must(http.MethodGet, "/odoh", nil, &got, http.StatusOK)
		got.Revision = original.Revision
		if !reflect.DeepEqual(got.update(), original.update()) {
			t.Fatal("ODoH settings were not restored")
		}
		waitFleet()
	})
	api.Must(http.MethodPut, "/odoh", body, &enabled, http.StatusOK)
	restoreRevision = enabled.Revision
	waitFleet()
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	client := harness.EncryptedClient{RootCAs: env.dnsRoots, ServerName: env.tlsName}
	hc := client.HTTPClient()
	defer hc.CloseIdleConnections()
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	base := "https://192.168.10.136"
	// Metadata and engine key lists are newest-first. Await the actual publication
	// deadline; never rewrite timestamps or retry an encrypted DNS failure.
	if time.Until(key.PublishAfter) <= 0 {
		t.Fatal("new ODoH key was already published before delay observation")
	}
	beforeConfigs, beforeResponse, err := harness.FetchODoHConfigs(ctx, hc, base)
	if err != nil || beforeResponse == nil || (beforeResponse.StatusCode != http.StatusOK && beforeResponse.StatusCode != http.StatusServiceUnavailable) {
		t.Fatalf("pre-publication configs: response=%v error=%v", beforeResponse, err)
	}
	if !time.Now().Before(key.PublishAfter) {
		t.Fatal("missed pre-publication observation")
	}
	samples := 0
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for time.Now().Before(key.PublishAfter.Add(2 * time.Second)) {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
		for _, addr := range []string{env.dnsAddr, env.secondDNSAddr} {
			r := harness.MustQuery(t, addr, name, dns.TypeA, harness.QueryOpts{})
			if r.Rcode != dns.RcodeSuccess || !r.Authoritative || firstA(r) != "192.0.2.18" {
				t.Fatalf("M8 probe %s: %v", addr, r)
			}
			samples++
		}
	}
	t.Logf("ODoH publication wait: %d scratch DNS probes across both VIPs, zero lost", samples)
	var latest kwM8OdohSettings
	api.Must(http.MethodGet, "/odoh", nil, &latest, http.StatusOK)
	if len(latest.Keys) == 0 || latest.Keys[0].ID != key.ID {
		t.Fatal("concurrent ODoH rotation changed the selected key")
	}
	configs, resp, err := harness.FetchODoHConfigs(ctx, hc, base)
	if err != nil || resp == nil || resp.StatusCode != http.StatusOK || len(configs) == 0 {
		t.Fatalf("published ODoH configs: response=%v error=%v count=%d", resp, err, len(configs))
	}
	for _, old := range beforeConfigs {
		if bytes.Equal(old.KeyID(), configs[0].KeyID()) {
			t.Fatal("rotated key was already public before its deadline, or the engine still serves an old key")
		}
	}
	q := new(dns.Msg)
	q.SetQuestion(name, dns.TypeA)
	answer, response, err := harness.ODoHQuery(ctx, hc, base+"/dns-query", configs[0], q)
	if err != nil || response == nil || response.StatusCode != http.StatusOK || answer == nil || answer.Rcode != dns.RcodeSuccess || !answer.Authoritative || len(answer.Answer) != 1 || firstA(answer) != "192.0.2.18" {
		t.Fatalf("encrypted query: answer=%v response=%v error=%v", answer, response, err)
	}
}
