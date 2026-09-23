package e2e

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

type catalogMemberView struct {
	ZoneID *string `json:"zone_id"`
	Name   string  `json:"name"`
	Label  string  `json:"label"`
	State  string  `json:"state"`
	Issue  string  `json:"issue"`
}

type catalogView struct {
	ID              string              `json:"id"`
	ZoneID          string              `json:"zone_id"`
	BrokenReason    string              `json:"broken_reason"`
	ProcessedSerial *uint32             `json:"processed_serial"`
	ProcessedAt     *time.Time          `json:"processed_at"`
	Members         []catalogMemberView `json:"members"`
}

type catalogZoneView struct {
	zoneResp
	Name      string            `json:"name"`
	Kind      string            `json:"kind"`
	CatalogID *string           `json:"catalog_zone_id"`
	Label     string            `json:"catalog_member_label"`
	Primaries []catalogEndpoint `json:"primaries"`
	Secondary *struct {
		LastError     string     `json:"last_error"`
		LastRefreshAt *time.Time `json:"last_refresh_at"`
		LastSuccessAt *time.Time `json:"last_success_at"`
	} `json:"secondary_status"`
}

type catalogEndpoint struct {
	Address string `json:"address"`
	KeyID   string `json:"tsig_key_id"`
}

// Keep removal checks local and deterministic: only hosted zones are answerable
// from the test client. No recursive lookup of deleted .test names is needed.
func startCatalogStack(t *testing.T) zonemdStack {
	t.Helper()
	st := startZonemdStack(t)
	var ac struct {
		Revision int64 `json:"revision"`
	}
	st.api.Must(http.MethodGet, "/access-control", nil, &ac, http.StatusOK)
	st.api.Must(http.MethodPut, "/access-control", map[string]any{
		"revision":                  ac.Revision,
		"allow_cidrs":               []string{"192.0.2.254/32"},
		"authoritative_allow_cidrs": []string{"127.0.0.1/32"},
	}, nil, http.StatusOK)
	waitLatestApplied(t, st.api, zonemdNode)
	return st
}

func catalogGet(t *testing.T, api *harness.API, id string) catalogView {
	t.Helper()
	var v catalogView
	api.Must(http.MethodGet, "/catalog-zones/"+id, nil, &v, http.StatusOK)
	return v
}

func catalogZoneGet(t *testing.T, api *harness.API, id string) catalogZoneView {
	t.Helper()
	var v catalogZoneView
	api.Must(http.MethodGet, "/zones/"+id, nil, &v, http.StatusOK)
	return v
}

func catalogWait(t *testing.T, api *harness.API, id string, serial uint32, broken string) catalogView {
	t.Helper()
	var v catalogView
	harness.Eventually(t, 30*time.Second, func() error {
		v = catalogGet(t, api, id)
		if v.ProcessedSerial == nil || *v.ProcessedSerial != serial || v.BrokenReason != broken {
			return fmt.Errorf("catalog processing: %+v, want serial %d reason %q", v, serial, broken)
		}
		return nil
	})
	return v
}

func catalogMember(t *testing.T, v catalogView, name, label, state string) catalogMemberView {
	t.Helper()
	for _, m := range v.Members {
		if m.Name == name && m.Label == label && m.State == state {
			if state == "configured" && m.ZoneID == nil || state == "clash" && (m.ZoneID != nil || m.Issue == "") {
				t.Fatalf("invalid catalog member: %+v", m)
			}
			return m
		}
	}
	t.Fatalf("missing %s/%s/%s in %+v", name, label, state, v.Members)
	return catalogMemberView{}
}

// Query without recursion: a removed zone must stop being authoritative, even if
// another resolver could still resolve that name. Transport errors are never success.
func catalogDNS(t *testing.T, addr, zone, ip string) {
	t.Helper()
	for _, network := range []string{"udp", "tcp"} {
		harness.Eventually(t, 30*time.Second, func() error {
			for _, typ := range []uint16{dns.TypeSOA, dns.TypeA} {
				name := zone
				if typ == dns.TypeA {
					name = "www." + zone
				}
				q := new(dns.Msg)
				q.SetQuestion(name, typ)
				q.RecursionDesired = false
				r, _, err := (&dns.Client{Net: network, Timeout: time.Second}).Exchange(q, addr)
				if err != nil {
					return err
				}
				if ip == "" {
					if r.Authoritative || len(r.Answer) != 0 || r.Rcode != dns.RcodeRefused {
						return fmt.Errorf("%s still served or unexpected removal response: %s", name, r)
					}
					continue
				}
				if r.Rcode != dns.RcodeSuccess || !r.Authoritative || len(r.Answer) != 1 {
					return fmt.Errorf("%s %s: %s", network, name, r)
				}
				if typ == dns.TypeA {
					a, ok := r.Answer[0].(*dns.A)
					if !ok || a.A.String() != ip {
						return fmt.Errorf("%s: %v, want %s", name, r.Answer, ip)
					}
				} else if soa, ok := r.Answer[0].(*dns.SOA); !ok || soa.Hdr.Name != zone {
					return fmt.Errorf("%s: invalid SOA %v", zone, r.Answer)
				}
			}
			return nil
		})
	}
}

func catalogTransfer(t *testing.T, addr, name string, key *tsigKeyResp) []dns.RR {
	t.Helper()
	rrs, err := axfr(addr, name, key)
	if err != nil || len(rrs) < 3 {
		t.Fatalf("AXFR %s: %d RRs, %v", name, len(rrs), err)
	}
	first, ok := rrs[0].(*dns.SOA)
	last, lastOK := rrs[len(rrs)-1].(*dns.SOA)
	if !ok || !lastOK || first.String() != last.String() {
		t.Fatalf("incomplete AXFR %s: %v", name, rrs)
	}
	return rrs[:len(rrs)-1]
}

func catalogWire(t *testing.T, addr, name string, key *tsigKeyResp, want map[string]string) {
	t.Helper()
	rrs := catalogTransfer(t, addr, name, key)
	ptrs := map[string]string{}
	versions, ns := 0, 0
	for _, rr := range rrs {
		switch r := rr.(type) {
		case *dns.TXT:
			if r.Hdr.Name != "version."+name || len(r.Txt) != 1 || r.Txt[0] != "2" || r.Hdr.Ttl != 0 {
				t.Fatalf("catalog TXT: %v", r)
			}
			versions++
		case *dns.NS:
			if r.Hdr.Name != name || r.Ns != "invalid." {
				t.Fatalf("catalog NS: %v", r)
			}
			ns++
		case *dns.PTR:
			if r.Hdr.Ttl != 0 {
				t.Fatalf("catalog PTR TTL: %v", r)
			}
			if _, exists := ptrs[r.Hdr.Name]; exists {
				t.Fatalf("duplicate catalog PTR: %v", r)
			}
			ptrs[r.Hdr.Name] = r.Ptr
		}
	}
	if versions != 1 || ns != 1 || !reflect.DeepEqual(ptrs, want) {
		t.Fatalf("catalog AXFR: versions=%d NS=%d PTRs=%v, want %v", versions, ns, ptrs, want)
	}
}

// Assert the DNS error itself; an unavailable server is not proof of authentication.
func catalogRejectTransfer(t *testing.T, addr, name string, key *tsigKeyResp, rcode int, tsigError uint16) {
	t.Helper()
	q := new(dns.Msg)
	q.SetAxfr(name)
	c := &dns.Client{Net: "tcp", Timeout: 2 * time.Second}
	if key != nil {
		q.SetTsig(key.Name, dns.HmacSHA256, 300, time.Now().Unix())
		c.TsigSecret = map[string]string{key.Name: key.Secret}
	}
	r, _, err := c.Exchange(q, addr)
	// BADKEY replies may be unsigned and therefore produce a client TSIG error;
	// they still must carry NOTAUTH and zero transferred records.
	if r == nil || r.Rcode != rcode || len(r.Answer) != 0 {
		t.Fatalf("unauthorized AXFR %s: response=%v error=%v", name, r, err)
	}
	if key != nil && (r.IsTsig() == nil || r.IsTsig().Error != tsigError) {
		t.Fatalf("unauthorized AXFR %s: TSIG error=%v, want %d", name, r.IsTsig(), tsigError)
	}
	if key == nil && err != nil {
		t.Fatalf("unsigned AXFR response: %v", err)
	}
}

func TestCatalogZoneProducer(t *testing.T) {
	st := startCatalogStack(t)
	var key tsigKeyResp
	st.api.Must(http.MethodPost, "/tsig-keys", map[string]any{"name": "cat-key.", "algorithm": "hmac-sha256"}, &key, http.StatusCreated)
	transfer := map[string]any{"allow_cidrs": []string{"127.0.0.1/32"}, "tsig_key_id": key.ID}
	var cat catalogView
	st.api.Must(http.MethodPost, "/catalog-zones", map[string]any{"name": "catalog.test.", "role": "producer", "transfer": transfer}, &cat, http.StatusCreated)
	makeMember := func(name, ip string) zoneResp {
		z := createPrimaryZone(t, st.api, name, map[string]any{"transfer": transfer})
		addRecord(t, st.api, z.ID, "www."+name, "A", ip)
		st.waitEngineSerial(t, z.ID, name)
		// Attach only after content is available: a live BIND consumer may
		// transfer immediately when it observes the new catalog PTR.
		st.api.Must(http.MethodPatch, "/zones/"+z.ID, map[string]any{"revision": st.zone(t, z.ID).Revision, "catalog_zone_id": cat.ID}, nil, http.StatusOK)
		return z
	}
	a := makeMember("a.prod.test.", "192.0.2.21")
	b := makeMember("b.prod.test.", "192.0.2.22")
	st.waitEngineSerial(t, cat.ZoneID, "catalog.test.")
	owner := func(id string) string { return strings.ReplaceAll(id, "-", "") + ".zones.catalog.test." }
	want := map[string]string{owner(a.ID): "a.prod.test.", owner(b.ID): "b.prod.test."}
	catalogWire(t, st.engine.DNS, "catalog.test.", &key, want)
	unknown := key
	unknown.Name = "unknown-key."
	badSignature := key
	if strings.HasPrefix(key.Secret, "A") {
		badSignature.Secret = "B" + key.Secret[1:]
	} else {
		badSignature.Secret = "A" + key.Secret[1:]
	}
	for _, name := range []string{"catalog.test.", "a.prod.test.", "b.prod.test."} {
		catalogRejectTransfer(t, st.engine.DNS, name, nil, dns.RcodeRefused, 0)
		catalogRejectTransfer(t, st.engine.DNS, name, &unknown, dns.RcodeNotAuth, dns.RcodeBadKey)
		catalogRejectTransfer(t, st.engine.DNS, name, &badSignature, dns.RcodeNotAuth, dns.RcodeBadSig)
	}
	named := st.env.StartNamedConfig(harness.NamedConfig{
		Keys:     []harness.NamedKey{{Name: key.Name, Algorithm: "hmac-sha256", SecretB64: key.Secret}},
		Catalogs: []harness.NamedCatalog{{Zone: "catalog.test.", Primary: st.engine.DNS, KeyName: key.Name}},
	})
	catalogDNS(t, named.Addr, "a.prod.test.", "192.0.2.21")
	catalogDNS(t, named.Addr, "b.prod.test.", "192.0.2.22")
	// Register NOTIFY after named has its dynamically allocated address.
	st.api.Must(http.MethodPatch, "/zones/"+cat.ZoneID, map[string]any{"revision": st.zone(t, cat.ZoneID).Revision, "notify": []catalogEndpoint{{Address: named.Addr, KeyID: key.ID}}}, nil, http.StatusOK)
	before := st.zone(t, cat.ZoneID)
	code, reason := st.api.ErrorCode(http.MethodPost, "/zones/"+cat.ZoneID+"/records", map[string]any{"name": "rogue.zones.catalog.test.", "type": "PTR", "ttl": 0, "data": "rogue.prod.test."})
	if code != http.StatusConflict || reason != "catalog_managed" {
		t.Fatalf("catalog record mutation: %d %s", code, reason)
	}
	after := st.zone(t, cat.ZoneID)
	if after.Revision != before.Revision || after.Serial != before.Serial {
		t.Fatal("rejected catalog edit changed revision/serial")
	}
	catalogWire(t, st.engine.DNS, "catalog.test.", &key, want)
	st.api.Must(http.MethodPatch, "/zones/"+b.ID, map[string]any{"revision": st.zone(t, b.ID).Revision, "catalog_zone_id": nil}, nil, http.StatusOK)
	st.waitEngineSerial(t, cat.ZoneID, "catalog.test.")
	delete(want, owner(b.ID))
	catalogWire(t, st.engine.DNS, "catalog.test.", &key, want)
	catalogDNS(t, named.Addr, "b.prod.test.", "")
	catalogDNS(t, st.engine.DNS, "b.prod.test.", "192.0.2.22")
	st.api.Must(http.MethodDelete, fmt.Sprintf("/zones/%s?revision=%d", a.ID, st.zone(t, a.ID).Revision), nil, nil, http.StatusNoContent)
	st.waitEngineSerial(t, cat.ZoneID, "catalog.test.")
	catalogWire(t, st.engine.DNS, "catalog.test.", &key, map[string]string{})
	catalogDNS(t, named.Addr, "a.prod.test.", "")
	a2 := makeMember("a.prod.test.", "192.0.2.31")
	if a2.ID == a.ID {
		t.Fatal("re-created zone reused UUID")
	}
	st.waitEngineSerial(t, cat.ZoneID, "catalog.test.")
	catalogWire(t, st.engine.DNS, "catalog.test.", &key, map[string]string{owner(a2.ID): "a.prod.test."})
	catalogDNS(t, named.Addr, "a.prod.test.", "192.0.2.31")
}

func TestCatalogZoneConsumer(t *testing.T) {
	st := startCatalogStack(t)
	var key, wrong tsigKeyResp
	for name, out := range map[string]*tsigKeyResp{"remote-key.": &key, "unknown-key.": &wrong} {
		st.api.Must(http.MethodPost, "/tsig-keys", map[string]any{"name": name, "algorithm": "hmac-sha256"}, out, http.StatusCreated)
	}
	operator := createPrimaryZone(t, st.api, "c.cat.test.", nil)
	addRecord(t, st.api, operator.ID, "www.c.cat.test.", "A", "192.0.2.99")
	operatorBefore := catalogZoneGet(t, st.api, operator.ID)
	catalogText := func(serial uint32, version, members string) string {
		return fmt.Sprintf("$ORIGIN cat.remote.\n@ 0 IN SOA invalid. invalid. %d 3600 600 86400 0\n@ 0 IN NS invalid.\nversion 0 IN TXT %q\n%s\n", serial, version, members)
	}
	const ab = "la.zones 0 IN PTR a.cat.test.\nlb.zones 0 IN PTR b.cat.test."
	zones := []harness.NamedZone{{Name: "cat.remote.", Type: "primary", Text: catalogText(1, "2", ab), AllowTransferKey: key.Name}}
	for i, name := range []string{"a.cat.test.", "b.cat.test.", "c.cat.test."} {
		zones = append(zones, harness.NamedZone{Name: name, Type: "primary", AllowTransferKey: key.Name, Text: fmt.Sprintf("$ORIGIN %s\n$TTL 60\n@ IN SOA ns. hostmaster. 1 3600 600 86400 60\n@ IN NS ns.\nwww IN A 192.0.2.%d\n", name, 21+i)})
	}
	named := st.env.StartNamedConfig(harness.NamedConfig{Keys: []harness.NamedKey{{Name: key.Name, Algorithm: "hmac-sha256", SecretB64: key.Secret}}, Zones: zones})
	for _, name := range []string{"cat.remote.", "a.cat.test.", "b.cat.test."} {
		catalogTransfer(t, named.Addr, name, &key)
		catalogRejectTransfer(t, named.Addr, name, nil, dns.RcodeRefused, 0)
		catalogRejectTransfer(t, named.Addr, name, &wrong, dns.RcodeNotAuth, dns.RcodeBadKey)
	}
	var cat catalogView
	primaries := []catalogEndpoint{{Address: named.Addr, KeyID: key.ID}}
	st.api.Must(http.MethodPost, "/catalog-zones", map[string]any{"name": "cat.remote.", "role": "consumer", "primaries": primaries}, &cat, http.StatusCreated)
	v := catalogWait(t, st.api, cat.ID, 1, "")
	if len(v.Members) != 2 {
		t.Fatalf("initial members: %+v", v.Members)
	}
	a := catalogMember(t, v, "a.cat.test.", "la", "configured")
	b := catalogMember(t, v, "b.cat.test.", "lb", "configured")
	assertMember := func(m catalogMemberView, ip string) catalogZoneView {
		z := catalogZoneGet(t, st.api, *m.ZoneID)
		if z.Kind != "secondary" || z.CatalogID == nil || *z.CatalogID != cat.ID || z.Label != m.Label || !reflect.DeepEqual(z.Primaries, primaries) {
			t.Fatalf("inherited member settings: %+v", z)
		}
		catalogDNS(t, st.engine.DNS, m.Name, ip)
		return catalogZoneGet(t, st.api, *m.ZoneID)
	}
	assertMember(a, "192.0.2.21")
	assertMember(b, "192.0.2.22")
	refresh := func() { st.api.Must(http.MethodPost, "/zones/"+cat.ZoneID+"/refresh", nil, nil, http.StatusAccepted) }
	publish := func(serial uint32, version, members string) {
		named.UpdateZone(t, catalogText(serial, version, members))
		waitSerial(t, named.Addr, "cat.remote.", serial)
		refresh()
	}
	publish(2, "2", ab+"\nlc.zones 0 IN PTR c.cat.test.")
	v = catalogWait(t, st.api, cat.ID, 2, "")
	if len(v.Members) != 3 {
		t.Fatalf("clash catalog: %+v", v.Members)
	}
	catalogMember(t, v, "c.cat.test.", "lc", "clash")
	if got := catalogZoneGet(t, st.api, operator.ID); !reflect.DeepEqual(got, operatorBefore) {
		t.Fatalf("clash modified operator: before=%+v after=%+v", operatorBefore, got)
	}
	catalogDNS(t, st.engine.DNS, "c.cat.test.", "192.0.2.99")
	publish(3, "2", "la2.zones 0 IN PTR a.cat.test.")
	v = catalogWait(t, st.api, cat.ID, 3, "")
	if len(v.Members) != 1 {
		t.Fatalf("remove/relabel: %+v", v.Members)
	}
	a2 := catalogMember(t, v, "a.cat.test.", "la2", "configured")
	if *a2.ZoneID == *a.ZoneID {
		t.Fatal("changed member label did not recreate zone")
	}
	for _, id := range []string{*a.ZoneID, *b.ZoneID} {
		st.api.Must(http.MethodGet, "/zones/"+id, nil, nil, http.StatusNotFound)
	}
	lastGood := assertMember(a2, "192.0.2.21")
	catalogDNS(t, st.engine.DNS, "b.cat.test.", "")
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		path := "/zones/" + lastGood.ID
		var body any = map[string]any{"revision": lastGood.Revision, "zonemd_verify": "off"}
		if method == http.MethodDelete {
			path += fmt.Sprintf("?revision=%d", lastGood.Revision)
			body = nil
		}
		code, reason := st.api.ErrorCode(method, path, body)
		if code != http.StatusConflict || reason != "catalog_managed" {
			t.Fatalf("%s member: %d %s", method, code, reason)
		}
	}
	unchanged := func() {
		got := catalogZoneGet(t, st.api, lastGood.ID)
		if got.ID != lastGood.ID || got.Revision != lastGood.Revision || got.Serial != lastGood.Serial || got.CatalogID == nil || *got.CatalogID != cat.ID || got.Label != lastGood.Label || !reflect.DeepEqual(got.Primaries, lastGood.Primaries) {
			t.Fatalf("last-good member changed: before=%+v after=%+v", lastGood, got)
		}
		catalogDNS(t, st.engine.DNS, "a.cat.test.", "192.0.2.21")
		catalogDNS(t, st.engine.DNS, "b.cat.test.", "")
		catalogDNS(t, st.engine.DNS, "c.cat.test.", "192.0.2.99")
	}
	unchanged()
	// Broken, empty catalog would delete a if it were processed as a valid catalog.
	publish(4, "1", "")
	broken := catalogWait(t, st.api, cat.ID, 4, `unsupported catalog version "1"`)
	if !reflect.DeepEqual(broken.Members, v.Members) {
		t.Fatal("broken catalog changed membership")
	}
	unchanged()
	publish(5, "2", "la2.zones 0 IN PTR a.cat.test.")
	good := catalogWait(t, st.api, cat.ID, 5, "")
	unchanged()
	// An unchanged serial must not reconcile again. Wait for an actual completed refresh.
	beforeRefresh := catalogZoneGet(t, st.api, cat.ZoneID)
	refresh()
	harness.Eventually(t, 30*time.Second, func() error {
		z := catalogZoneGet(t, st.api, cat.ZoneID)
		if z.Secondary == nil || z.Secondary.LastRefreshAt == nil || beforeRefresh.Secondary == nil || beforeRefresh.Secondary.LastRefreshAt == nil || !z.Secondary.LastRefreshAt.After(*beforeRefresh.Secondary.LastRefreshAt) || z.Secondary.LastError != "" {
			return fmt.Errorf("unchanged refresh not complete: %+v", z.Secondary)
		}
		return nil
	})
	if after := catalogGet(t, st.api, cat.ID); !reflect.DeepEqual(good, after) {
		t.Fatalf("unchanged serial reprocessed: before=%+v after=%+v", good, after)
	}
	// Wrong key prevents an otherwise valid empty catalog from deleting last-good a.
	st.api.Must(http.MethodPatch, "/zones/"+cat.ZoneID, map[string]any{"revision": st.zone(t, cat.ZoneID).Revision, "primaries": []catalogEndpoint{{Address: named.Addr, KeyID: wrong.ID}}}, nil, http.StatusOK)
	beforeFailure := catalogZoneGet(t, st.api, cat.ZoneID)
	publish(6, "2", "")
	harness.Eventually(t, 30*time.Second, func() error {
		z := catalogZoneGet(t, st.api, cat.ZoneID)
		// The deliberately unknown TSIG key name yields NOTAUTH/BADKEY from
		// BIND (ErrAuth), not a known-key signature mismatch (ErrSig).
		if z.Secondary == nil || z.Secondary.LastRefreshAt == nil || beforeFailure.Secondary == nil || beforeFailure.Secondary.LastRefreshAt == nil || !z.Secondary.LastRefreshAt.After(*beforeFailure.Secondary.LastRefreshAt) || !strings.Contains(z.Secondary.LastError, "SOA query: "+dns.ErrAuth.Error()) {
			return fmt.Errorf("failed TSIG refresh not complete: %+v", z.Secondary)
		}
		if !reflect.DeepEqual(z.Secondary.LastSuccessAt, beforeFailure.Secondary.LastSuccessAt) {
			t.Fatal("TSIG failure advanced last success")
		}
		return nil
	})
	if got := st.zone(t, cat.ZoneID); got.Serial != 5 {
		t.Fatalf("unauthenticated catalog replaced last-good serial: %d", got.Serial)
	}
	if after := catalogGet(t, st.api, cat.ID); !reflect.DeepEqual(good, after) {
		t.Fatalf("authentication failure reconciled catalog: %+v", after)
	}
	unchanged()
	st.api.Must(http.MethodPatch, "/zones/"+cat.ZoneID, map[string]any{"revision": st.zone(t, cat.ZoneID).Revision, "primaries": primaries}, nil, http.StatusOK)
	refresh()
	empty := catalogWait(t, st.api, cat.ID, 6, "")
	if len(empty.Members) != 0 {
		t.Fatalf("valid empty catalog did not remove members: %+v", empty.Members)
	}
	st.api.Must(http.MethodGet, "/zones/"+lastGood.ID, nil, nil, http.StatusNotFound)
	catalogDNS(t, st.engine.DNS, "a.cat.test.", "")
	publish(7, "2", "lz.zones 0 IN PTR a.cat.test.")
	v = catalogWait(t, st.api, cat.ID, 7, "")
	kept := assertMember(catalogMember(t, v, "a.cat.test.", "lz", "configured"), "192.0.2.21")
	st.api.Must(http.MethodDelete, "/catalog-zones/"+cat.ID, nil, nil, http.StatusNoContent)
	st.api.Must(http.MethodGet, "/catalog-zones/"+cat.ID, nil, nil, http.StatusNotFound)
	st.api.Must(http.MethodGet, "/zones/"+cat.ZoneID, nil, nil, http.StatusNotFound)
	detached := catalogZoneGet(t, st.api, kept.ID)
	if detached.Kind != "secondary" || detached.CatalogID != nil || detached.Label != "" || detached.Serial != kept.Serial || !reflect.DeepEqual(detached.Primaries, primaries) {
		t.Fatalf("catalog deletion did not preserve plain secondary: %+v", detached)
	}
	catalogDNS(t, st.engine.DNS, "a.cat.test.", "192.0.2.21")
	catalogDNS(t, st.engine.DNS, "c.cat.test.", "192.0.2.99")
	// Ordinary-secondary editing is restored after detaching.
	st.api.Must(http.MethodPatch, "/zones/"+kept.ID, map[string]any{"revision": detached.Revision, "zonemd_verify": "off"}, nil, http.StatusOK)
}
