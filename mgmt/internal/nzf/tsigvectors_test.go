package nzf

import (
	"encoding/base64"
	"os"
	"testing"

	"github.com/miekg/dns"
)

const tsigTime = 1757750400

func tsigSecret(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// TestTSIGVectors writes miekg-signed TSIG messages the engine's TSIG tests verify and reproduce
// (-update); otherwise it checks the files exist.
func TestTSIGVectors(t *testing.T) {
	q := new(dns.Msg)
	q.Id = 0x1234
	q.SetQuestion("xfr.test.", dns.TypeSOA)
	q.SetTsig("xfr-key.", dns.HmacSHA256, 300, tsigTime)
	qwire, qmac, err := dns.TsigGenerate(q, tsigSecret(32), "", false)
	if err != nil {
		t.Fatal(err)
	}
	r := new(dns.Msg)
	r.SetReply(q)
	r.Authoritative = true
	soa, _ := dns.NewRR("xfr.test. 300 IN SOA ns1.xfr.test. hostmaster.xfr.test. 7 7200 3600 1209600 300")
	r.Answer = []dns.RR{soa}
	r.Extra = nil
	r.SetTsig("xfr-key.", dns.HmacSHA256, 300, tsigTime)
	rwire, _, err := dns.TsigGenerate(r, tsigSecret(32), qmac, false)
	if err != nil {
		t.Fatal(err)
	}
	q5 := new(dns.Msg)
	q5.Id = 0x4321
	q5.SetQuestion("xfr.test.", dns.TypeAXFR)
	q5.SetTsig("sha512-key.", dns.HmacSHA512, 300, tsigTime)
	q5wire, _, err := dns.TsigGenerate(q5, tsigSecret(64), "", false)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"query-hmac-sha256.bin": qwire, "response-hmac-sha256.bin": rwire, "query-hmac-sha512.bin": q5wire}
	for name, data := range files {
		path := "../../../testdata/tsig/" + name
		if *update {
			if err := os.MkdirAll("../../../testdata/tsig", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s missing (run with -update once): %v", name, err)
		}
	}
}
