package dnssec

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

// zoneText renders served as a zone file.
func zoneText(served []dns.RR) string {
	var b strings.Builder
	for _, rr := range served {
		b.WriteString(rr.String())
		b.WriteByte('\n')
	}
	return b.String()
}

// TestSignedZonesValidateWithBIND checks the signer's output with BIND's own tools: dnssec-verify
// accepts the whole zone offline, and delv (trusting only the KSK) fully validates positive
// answers, NSEC/NSEC3 denials, wildcard proofs, DS and CDS data served by named from that zone.
func TestSignedZonesValidateWithBIND(t *testing.T) {
	verify, err := exec.LookPath("dnssec-verify")
	if err != nil {
		t.Skip("dnssec-verify (bind9-dnsutils) is not installed")
	}
	delv, err := exec.LookPath("delv")
	if err != nil {
		t.Skip("delv (bind9-dnsutils) is not installed")
	}
	if _, err := exec.LookPath("named"); err != nil {
		t.Skip("named (bind9) is not installed")
	}
	positive, negative := "; fully validated", "; negative response, fully validated"
	cases := []struct{ name, qtype, want string }{
		{"www.example.test.", "A", positive},
		{"alias.example.test.", "A", positive},
		{"anything.wild.example.test.", "TXT", positive},
		{"sub.example.test.", "DS", positive},
		{"example.test.", "CDS", positive},
		{"example.test.", "CDNSKEY", positive},
		{"nope.example.test.", "A", negative},
		{"x.nope.example.test.", "A", negative},
		{"www.example.test.", "MX", negative},
		{"b.c.example.test.", "A", negative},
		{"anything.wild.example.test.", "A", negative},
		{"insecure.example.test.", "DS", negative},
	}
	for _, v := range []struct {
		label string
		alg   uint8
		nsec3 bool
	}{{"ecdsa-nsec", dns.ECDSAP256SHA256, false}, {"ecdsa-nsec3", dns.ECDSAP256SHA256, true}, {"rsa-nsec3", dns.RSASHA256, true}} {
		t.Run(v.label, func(t *testing.T) {
			keys := testKeysAlg(t, v.alg)
			out, err := Sign(Input{Origin: "example.test.", Records: basicRecords(t), Keys: keys, NSEC3: v.nsec3, Now: time.Now()})
			if err != nil {
				t.Fatal(err)
			}
			text := zoneText(out.Served)
			dir := t.TempDir()
			file := filepath.Join(dir, "example.test.db")
			if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			// Every argument is built by this test; the binaries come from $PATH.
			if res, err := exec.Command(verify, "-o", "example.test.", file).CombinedOutput(); err != nil { // nosemgrep: dangerous-exec-command
				t.Fatalf("dnssec-verify: %v\n%s", err, res)
			}
			ksk := keys[0].DNSKEY
			anchors := filepath.Join(dir, "anchors.conf")
			conf := fmt.Sprintf("trust-anchors { example.test. static-key %d %d %d %q; };\n", ksk.Flags, ksk.Protocol, ksk.Algorithm, ksk.PublicKey)
			if err := os.WriteFile(anchors, []byte(conf), 0o600); err != nil {
				t.Fatal(err)
			}
			named := harness.New(t).StartNamedConfig(harness.NamedConfig{Zones: []harness.NamedZone{{Name: "example.test.", Type: "primary", Text: text}}})
			host, port, _ := strings.Cut(named.Addr, ":")
			for _, c := range cases {
				res, _ := exec.Command(delv, "@"+host, "-p", port, "-a", anchors, "+root=example.test", c.name, c.qtype).CombinedOutput() // nosemgrep: dangerous-exec-command
				if !strings.Contains("\n"+string(res), "\n"+c.want+"\n") {
					t.Errorf("delv %s %s: want %q, got\n%s", c.name, c.qtype, c.want, res)
				}
			}
		})
	}
}
