package e2e

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

// zonemdZone is the part of a zone view the ZONEMD tests read.
type zonemdZone struct {
	ID              string `json:"id"`
	Serial          uint32 `json:"serial"`
	Revision        int64  `json:"revision"`
	ZonemdStatus    string `json:"zonemd_status"`
	ZonemdError     string `json:"zonemd_error"`
	SecondaryStatus *struct {
		LastError     string  `json:"last_error"`
		LastSuccessAt *string `json:"last_success_at"`
	} `json:"secondary_status"`
}

// zonemdStack is a management plane with key storage, the admin API and one managed engine.
type zonemdStack struct {
	authEnv
	engine *harness.Engine
}

const zonemdNode = "zonemd-1"

func startZonemdStack(t *testing.T) zonemdStack {
	t.Helper()
	e := startAuthEnv(t, []string{"NEXORA_KEK_FILE=" + harness.WriteKEK(t)}, zonemdNode)
	return zonemdStack{authEnv: e, engine: e.engines[0]}
}

func (st zonemdStack) zone(t *testing.T, id string) zonemdZone {
	t.Helper()
	var z zonemdZone
	st.api.Must(http.MethodGet, "/zones/"+id, nil, &z, http.StatusOK)
	return z
}

// waitZone polls the zone view until ok holds.
func (st zonemdStack) waitZone(t *testing.T, id string, ok func(zonemdZone) bool) zonemdZone {
	t.Helper()
	var z zonemdZone
	harness.Eventually(t, 30*time.Second, func() error {
		z = st.zone(t, id)
		if !ok(z) {
			return fmt.Errorf("zone %+v", z)
		}
		return nil
	})
	return z
}

// waitEngineSerial waits until the engine serves the zone at the serial the API reports now (a
// signed zone's maintenance may publish another version meanwhile, so the API is read each time).
func (st zonemdStack) waitEngineSerial(t *testing.T, id, name string) {
	t.Helper()
	harness.Eventually(t, 15*time.Second, func() error {
		want := st.zone(t, id).Serial
		got, err := soaSerial(st.engine.DNS, name)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("engine serial %d, API %d", got, want)
		}
		return nil
	})
}

func (st zonemdStack) createSecondaryZone(t *testing.T, name, primary string, extra map[string]any) string {
	t.Helper()
	body := map[string]any{"name": name, "kind": "secondary", "primaries": []map[string]any{{"address": primary}}}
	for k, v := range extra {
		body[k] = v
	}
	var z zonemdZone
	st.api.Must(http.MethodPost, "/zones", body, &z, http.StatusCreated)
	return z.ID
}

// transferZone runs an AXFR of name from addr without TSIG and drops the closing SOA.
func transferZone(t *testing.T, addr, name string) []dns.RR {
	t.Helper()
	rrs, err := axfr(addr, name, nil)
	if err != nil || len(rrs) < 2 || rrs[0].Header().Rrtype != dns.TypeSOA || rrs[len(rrs)-1].Header().Rrtype != dns.TypeSOA {
		t.Fatalf("AXFR %s: %d records, err=%v", name, len(rrs), err)
	}
	return rrs[:len(rrs)-1]
}

type ixfrDiff struct{ Deleted, Added []dns.RR }

// transferIncremental runs an IXFR of name from serial and fails unless the answer is incremental.
func transferIncremental(t *testing.T, addr, name string, serial uint32) ixfrDiff {
	t.Helper()
	m := new(dns.Msg)
	m.SetIxfr(name, serial, ".", ".")
	tr := &dns.Transfer{DialTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second}
	ch, err := tr.In(m, addr)
	if err != nil {
		t.Fatalf("IXFR %s: %v", name, err)
	}
	var rrs []dns.RR
	for env := range ch {
		if env.Error != nil {
			t.Fatalf("IXFR %s: %v", name, env.Error)
		}
		rrs = append(rrs, env.RR...)
	}
	if len(rrs) < 3 || rrs[0].Header().Rrtype != dns.TypeSOA || rrs[1].Header().Rrtype != dns.TypeSOA {
		t.Fatalf("IXFR %s from %d is not incremental: %v", name, serial, rrs)
	}
	// Each difference sequence opens with the old SOA (deletions) and then the new SOA (additions).
	var d ixfrDiff
	adding := true
	for _, rr := range rrs[1 : len(rrs)-1] {
		if rr.Header().Rrtype == dns.TypeSOA {
			adding = !adding
			continue
		}
		if adding {
			d.Added = append(d.Added, rr)
		} else {
			d.Deleted = append(d.Deleted, rr)
		}
	}
	return d
}

func hasType(rrs []dns.RR, rtype uint16) bool {
	return slices.ContainsFunc(rrs, func(rr dns.RR) bool { return rr.Header().Rrtype == rtype })
}

func apexSOA(t *testing.T, rrs []dns.RR) *dns.SOA {
	t.Helper()
	soa, ok := rrs[0].(*dns.SOA)
	if !ok {
		t.Fatalf("zone does not start with its SOA: %v", rrs[0])
	}
	return soa
}

func apexZONEMD(rrs []dns.RR, name string) *dns.ZONEMD {
	for _, rr := range rrs {
		if z, ok := rr.(*dns.ZONEMD); ok && strings.EqualFold(z.Hdr.Name, name) {
			return z
		}
	}
	return nil
}

func apexNSECHas(rrs []dns.RR, name string, rtype uint16) bool {
	for _, rr := range rrs {
		if n, ok := rr.(*dns.NSEC); ok && strings.EqualFold(n.Hdr.Name, name) {
			return slices.Contains(n.TypeBitMap, rtype)
		}
	}
	return false
}

// ldnsVerify checks the transferred zone with ldns-verify-zone, an independent ZONEMD (and, for
// signed zones, DNSSEC) implementation: -Z for an unsigned zone, -ZZ for a signed one.
func ldnsVerify(t *testing.T, rrs []dns.RR, signed bool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "zone")
	var b strings.Builder
	for _, rr := range rrs {
		b.WriteString(rr.String() + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	flag := "-Z"
	if signed {
		flag = "-ZZ"
	}
	if out, err := exec.Command("ldns-verify-zone", flag, path).CombinedOutput(); err != nil {
		t.Fatalf("ldns-verify-zone %s: %v\n%s\n%s", flag, err, out, b.String())
	}
}

// readVector returns an RFC 8976 appendix zone from testdata without its out-of-zone record.
func readVector(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "rfc8976", name+".zone"))
	if err != nil {
		t.Fatal(err)
	}
	var keep []string
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "foo.test.") {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n")
}

var vectorSerial = regexp.MustCompile(`(SOA\s+\S+\s+\S+\s+)\d+`)

// bumpSerialKeepZonemd sets the SOA serial of zoneText and appends record, leaving the ZONEMD
// (and its serial) of the old version in place.
func bumpSerialKeepZonemd(zoneText string, serial uint32, record string) string {
	return vectorSerial.ReplaceAllString(zoneText, fmt.Sprintf("${1}%d", serial)) + "\n" + record + "\n"
}

const plainZone = "$TTL 60\n@ SOA ns.plain.test. h.plain.test. 7 3600 600 86400 60\n@ NS ns.plain.test.\nns A 192.0.2.53\n"

func TestZonemdGeneratedForPrimaryZones(t *testing.T) {
	st := startZonemdStack(t)
	ids := map[string]string{}
	for _, signed := range []bool{false, true} {
		name := fmt.Sprintf("zmd%v.test.", signed)
		z := createPrimaryZone(t, st.api, name, map[string]any{"zonemd_generate": true, "transfer": map[string]any{"allow_cidrs": []string{"127.0.0.1/32"}}})
		ids[name] = z.ID
		if signed {
			st.api.Must(http.MethodPut, "/zones/"+z.ID+"/dnssec", map[string]any{"revision": st.zone(t, z.ID).Revision, "enabled": true, "algorithm": 13, "nsec_mode": "nsec", "key_backend": "kek"}, nil, http.StatusOK)
		}
		addRecord(t, st.api, z.ID, "www."+name, "A", "192.0.2.10")
		st.waitEngineSerial(t, z.ID, name)
		first := transferZone(t, st.engine.DNS, name)
		ldnsVerify(t, first, signed)
		addRecord(t, st.api, z.ID, "mail."+name, "A", "192.0.2.11")
		st.waitEngineSerial(t, z.ID, name)
		second := transferZone(t, st.engine.DNS, name)
		ldnsVerify(t, second, signed)
		md, s1, s2 := apexZONEMD(second, name), apexSOA(t, first).Serial, apexSOA(t, second).Serial
		if md == nil || s1 == s2 || md.Serial != s2 || md.Scheme != 1 || md.Hash != 1 {
			t.Fatalf("%s: ZONEMD %v, SOA serials %d -> %d", name, md, s1, s2)
		}
		diff := transferIncremental(t, st.engine.DNS, name, s1)
		if !hasType(diff.Deleted, dns.TypeZONEMD) || !hasType(diff.Added, dns.TypeZONEMD) {
			t.Fatalf("%s: IXFR does not replace ZONEMD: %+v", name, diff)
		}
		if signed && !apexNSECHas(second, name, dns.TypeZONEMD) {
			t.Fatalf("%s: apex NSEC bitmap without ZONEMD", name)
		}
	}
	// ZONEMD is generated, never managed; rejection must not publish a new version.
	before := st.zone(t, ids["zmdfalse.test."])
	version := st.api.LatestVersion()
	var recordsBefore, recordsAfter any
	st.api.Must(http.MethodGet, "/zones/"+before.ID+"/records", nil, &recordsBefore, http.StatusOK)
	code, reason := st.api.ErrorCode(http.MethodPost, "/zones/"+ids["zmdfalse.test."]+"/records", map[string]any{"name": "zmdfalse.test.", "type": "ZONEMD", "ttl": 60, "data": "1 1 1 " + strings.Repeat("00", 48)})
	if code != http.StatusBadRequest || reason != "invalid_request" {
		t.Fatalf("ZONEMD record: %d %s, want 400 invalid_request", code, reason)
	}
	st.api.Must(http.MethodGet, "/zones/"+before.ID+"/records", nil, &recordsAfter, http.StatusOK)
	if after := st.zone(t, before.ID); !reflect.DeepEqual(before, after) || st.api.LatestVersion() != version || !reflect.DeepEqual(recordsBefore, recordsAfter) {
		t.Fatal("rejected ZONEMD changed zone, records or config version")
	}
}

func TestZonemdSecondaryVerification(t *testing.T) {
	st := startZonemdStack(t)
	a2 := readVector(t, "a2")
	named := st.env.StartNamedConfig(harness.NamedConfig{Zones: []harness.NamedZone{{Name: "example.", Type: "primary", Text: a2}}})
	id := st.createSecondaryZone(t, "example.", named.Addr, map[string]any{"zonemd_verify": "if_present"})
	st.waitZone(t, id, func(z zonemdZone) bool { return z.ZonemdStatus == "verified" && z.Serial == 2018031900 })
	harness.WaitDNSAnswer(t, st.engine.DNS, "ns1.example.", dns.TypeA, 15*time.Second, func(m *dns.Msg) bool {
		return m.Authoritative && len(m.Answer) == 1 && m.Answer[0].(*dns.A).A.String() == "203.0.113.63"
	})

	named.UpdateZone(t, bumpSerialKeepZonemd(a2, 2018031901, "added 3600 IN A 192.0.2.99"))
	harness.Eventually(t, 10*time.Second, func() error { // named reloaded the new version
		got, err := soaSerial(named.Addr, "example.")
		if err == nil && got != 2018031901 {
			err = fmt.Errorf("named serial %d", got)
		}
		return err
	})
	st.api.Must(http.MethodPost, "/zones/"+id+"/refresh", nil, nil, http.StatusAccepted)
	failed := st.waitZone(t, id, func(z zonemdZone) bool {
		return z.ZonemdStatus == "failed" && z.SecondaryStatus != nil && strings.Contains(z.SecondaryStatus.LastError, "zonemd:")
	})
	if failed.Serial != 2018031900 || !strings.Contains(failed.ZonemdError, "serial") {
		t.Fatalf("failing transfer: serial %d, zonemd_error %q", failed.Serial, failed.ZonemdError)
	}
	waitLatestApplied(t, st.api, zonemdNode)
	if r := harness.MustQuery(t, st.engine.DNS, "added.example.", dns.TypeA, harness.QueryOpts{TCP: true}); len(r.Answer) != 0 {
		t.Fatalf("a transfer failing ZONEMD was applied: %v", r)
	}

	st.api.Must(http.MethodPatch, "/zones/"+id, map[string]any{"revision": failed.Revision, "zonemd_verify": "off"}, nil, http.StatusOK)
	st.api.Must(http.MethodPost, "/zones/"+id+"/refresh", nil, nil, http.StatusAccepted)
	st.waitZone(t, id, func(z zonemdZone) bool { return z.Serial == 2018031901 && z.ZonemdStatus == "off" })
	harness.WaitDNSAnswer(t, st.engine.DNS, "added.example.", dns.TypeA, 15*time.Second, func(m *dns.Msg) bool { return len(m.Answer) == 1 })

	plain := st.env.StartNamedConfig(harness.NamedConfig{Zones: []harness.NamedZone{{Name: "plain.test.", Type: "primary", Text: plainZone}}})
	req := st.createSecondaryZone(t, "plain.test.", plain.Addr, map[string]any{"zonemd_verify": "required"})
	got := st.waitZone(t, req, func(z zonemdZone) bool { return z.ZonemdStatus == "failed" })
	if got.Serial == 7 || got.SecondaryStatus == nil || got.SecondaryStatus.LastSuccessAt != nil || !strings.Contains(got.ZonemdError, "no apex ZONEMD") {
		t.Fatalf("required without ZONEMD loaded: %+v %+v", got, got.SecondaryStatus)
	}
	waitLatestApplied(t, st.api, zonemdNode)
	if r := harness.MustQuery(t, st.engine.DNS, "ns.plain.test.", dns.TypeA, harness.QueryOpts{TCP: true}); len(r.Answer) != 0 {
		t.Fatalf("engine serves a zone that failed required ZONEMD: %v", r)
	}
}
