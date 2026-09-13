package harness

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestNamedHarnessServesTSIGAXFRAndReloads(t *testing.T) {
	env := New(t)
	zone := "$TTL 60\n@ SOA ns.rpz.axfr.test. h.rpz.axfr.test. 1 2 1 30 60\n@ NS ns.rpz.axfr.test.\nns A 127.0.0.1\nblocked-axfr.plain.test CNAME .\n"
	n := env.StartNamed("rpz.axfr.test.", zone)
	axfr := func() []dns.RR {
		tr := &dns.Transfer{TsigSecret: map[string]string{n.KeyName: n.KeySecretB64}}
		m := new(dns.Msg)
		m.SetAxfr("rpz.axfr.test.")
		m.SetTsig(n.KeyName, dns.HmacSHA256, 300, time.Now().Unix())
		ch, err := tr.In(m, n.Addr)
		if err != nil {
			t.Fatalf("axfr: %v", err)
		}
		var rrs []dns.RR
		for env := range ch {
			if env.Error != nil {
				t.Fatalf("axfr envelope: %v", env.Error)
			}
			rrs = append(rrs, env.RR...)
		}
		return rrs
	}
	if got := len(axfr()); got != 5 {
		t.Fatalf("axfr records = %d, want 5 (SOA NS A CNAME SOA)", got)
	}
	n.UpdateZone(t, "$TTL 60\n@ SOA ns.rpz.axfr.test. h.rpz.axfr.test. 2 2 1 30 60\n@ NS ns.rpz.axfr.test.\nns A 127.0.0.1\nblocked-axfr.plain.test CNAME .\nnew.plain.test CNAME .\n")
	Eventually(t, 5*time.Second, func() error {
		if got := len(axfr()); got != 6 {
			return fmt.Errorf("after reload axfr records = %d, want 6", got)
		}
		return nil
	})
	// ixfr-from-differences journals the reload: an IXFR from serial 1 is incremental (the
	// second record is the old SOA), not a full zone.
	ixfr := new(dns.Msg)
	ixfr.SetIxfr("rpz.axfr.test.", 1, "ns.rpz.axfr.test.", "h.rpz.axfr.test.")
	ixfr.SetTsig(n.KeyName, dns.HmacSHA256, 300, time.Now().Unix())
	ich, err := (&dns.Transfer{TsigSecret: map[string]string{n.KeyName: n.KeySecretB64}}).In(ixfr, n.Addr)
	if err != nil {
		t.Fatalf("ixfr: %v", err)
	}
	var inc []dns.RR
	for e := range ich {
		if e.Error != nil {
			t.Fatalf("ixfr envelope: %v", e.Error)
		}
		inc = append(inc, e.RR...)
	}
	if soa, ok := inc[min(1, len(inc)-1)].(*dns.SOA); len(inc) != 5 || !ok || soa.Serial != 1 {
		t.Fatalf("ixfr from serial 1 = %v, want SOA 2, SOA 1, SOA 2, the added CNAME, SOA 2", inc)
	}
	unsigned := new(dns.Msg)
	unsigned.SetAxfr("rpz.axfr.test.")
	ch, err := (&dns.Transfer{}).In(unsigned, n.Addr)
	if err == nil {
		for e := range ch {
			if e.Error == nil && len(e.RR) > 0 {
				t.Fatal("unsigned AXFR must be refused")
			}
		}
	}
}

func TestWriteKEKIs32Base64Bytes(t *testing.T) {
	raw, err := os.ReadFile(WriteKEK(t))
	if err != nil {
		t.Fatal(err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != 32 {
		t.Fatalf("KEK file holds %d bytes (%v)", len(key), err)
	}
}
