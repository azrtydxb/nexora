package nzf

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/miekg/dns"
)

var update = flag.Bool("update", false, "rewrite testdata/nzf goldens")

func golden(name string) string { return filepath.Join("..", "..", "..", "testdata", "nzf", name) }

// basicZone is shared with the engine tests (engine/src/authoritative/*_tests.rs)
// and with the DNSSEC signer goldens (Task 12). Do not reorder or edit without
// regenerating every golden.
func basicZone(serial uint32, variant string) []string {
	rrs := []string{
		fmt.Sprintf("example.test. 3600 IN SOA ns1.example.test. hostmaster.example.test. %d 7200 3600 1209600 300", serial),
		"example.test. 3600 IN NS ns1.example.test.",
		"example.test. 3600 IN NS ns2.example.test.",
		"example.test. 3600 IN MX 10 mail.example.test.",
		"ns1.example.test. 3600 IN A 192.0.2.1",
		"ns2.example.test. 3600 IN AAAA 2001:db8::2",
		"mail.example.test. 3600 IN A 192.0.2.25",
		"www.example.test. 300 IN A 192.0.2.10",
		"alias.example.test. 300 IN CNAME www.example.test.",
		"loop1.example.test. 300 IN CNAME loop2.example.test.",
		"loop2.example.test. 300 IN CNAME loop1.example.test.",
		"*.wild.example.test. 300 IN TXT \"wildcard\"",
		"a.b.c.example.test. 300 IN A 192.0.2.20",
		"sub.example.test. 3600 IN NS ns.sub.example.test.",
		"sub.example.test. 3600 IN DS 60485 13 2 D4B7D520E7BB5F0F67674A0CCEB1E3E0614B93C4F9E99B8383F6A1E4469DA50A",
		"ns.sub.example.test. 3600 IN A 192.0.2.53",
		"insecure.example.test. 3600 IN NS ns.insecure.example.test.",
		"ns.insecure.example.test. 3600 IN A 192.0.2.54",
		"dn.example.test. 300 IN DNAME example.net.",
		"_sip._tcp.example.test. 300 IN SRV 10 60 5060 sip.example.test.",
	}
	for i := 0; i < 40; i++ {
		rrs = append(rrs, fmt.Sprintf("big.example.test. 300 IN TXT \"%03d%s\"", i, bytes.Repeat([]byte("x"), 100)))
	}
	if variant == "before" {
		rrs = append(rrs, "www.example.test. 300 IN A 192.0.2.11")
	} else {
		rrs = append(rrs, "new.example.test. 300 IN A 192.0.2.12")
	}
	return rrs
}

func records(t *testing.T, lines []string) []Record {
	t.Helper()
	var out []Record
	for _, l := range lines {
		rr, err := dns.NewRR(l)
		if err != nil {
			t.Fatalf("%q: %v", l, err)
		}
		r, err := FromRR(rr)
		if err != nil {
			t.Fatalf("%q: %v", l, err)
		}
		out = append(out, r)
	}
	return out
}

func wire(t *testing.T, name string) []byte {
	t.Helper()
	b := make([]byte, 256)
	n, err := dns.PackDomainName(name, b, 0, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	return b[:n]
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	if *update {
		if err := os.WriteFile(golden(name), got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden(name))
	if err != nil {
		t.Fatalf("read golden %s (run with -update once): %v", name, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from golden (%d vs %d bytes)", name, len(got), len(want))
	}
}

func TestCanonicalOrderRFC4034(t *testing.T) {
	ordered := []string{"example.", "a.example.", "yljkjljk.a.example.", "Z.a.example.",
		"zABC.a.EXAMPLE.", "z.example.", "\\001.z.example.", "*.z.example.", "\\200.z.example."}
	for i := 1; i < len(ordered); i++ {
		a, b := CanonicalKey(wire(t, ordered[i-1])), CanonicalKey(wire(t, ordered[i]))
		if bytes.Compare(a, b) >= 0 {
			t.Fatalf("%s must sort before %s", ordered[i-1], ordered[i])
		}
	}
}

func TestFullImageRoundTripAndGolden(t *testing.T) {
	img := Image{Origin: wire(t, "example.test."), Serial: 2026091301, Records: records(t, basicZone(2026091301, "before"))}
	raw, err := EncodeFull(img)
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "basic-full.nzf", raw)
	kind, back, _, err := Decode(raw)
	if err != nil || kind != KindFull {
		t.Fatalf("decode: kind=%d err=%v", kind, err)
	}
	if back.Serial != 2026091301 || len(back.Records) != len(img.Records) {
		t.Fatalf("round trip: serial=%d records=%d", back.Serial, len(back.Records))
	}
	for i := 1; i < len(back.Records); i++ {
		if bytes.Compare(CanonicalKey(back.Records[i-1].Owner), CanonicalKey(back.Records[i].Owner)) > 0 {
			t.Fatalf("records not in canonical order at %d", i)
		}
	}
}

func TestDeltaGoldenAndAfterImage(t *testing.T) {
	d := Delta{
		Origin: wire(t, "example.test."), FromSerial: 2026091301, ToSerial: 2026091302,
		Deleted: records(t, []string{basicZone(2026091301, "before")[0], "www.example.test. 300 IN A 192.0.2.11"}),
		Added:   records(t, []string{basicZone(2026091302, "after")[0], "new.example.test. 300 IN A 192.0.2.12"}),
	}
	raw, err := EncodeDelta(d)
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "basic-delta.nzf", raw)
	after, err := EncodeFull(Image{Origin: wire(t, "example.test."), Serial: 2026091302, Records: records(t, basicZone(2026091302, "after"))})
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "basic-after.nzf", after)
	var big []string
	big = append(big, "big.test. 3600 IN SOA ns1.big.test. hostmaster.big.test. 1 7200 3600 1209600 300", "big.test. 3600 IN NS ns1.big.test.")
	for i := 0; i < 2000; i++ {
		big = append(big, fmt.Sprintf("h%04d.big.test. 300 IN A 198.51.%d.%d", i, i/256, i%256))
	}
	bigRaw, err := EncodeFull(Image{Origin: wire(t, "big.test."), Serial: 1, Records: records(t, big)})
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "big-full.nzf", bigRaw)
}

func TestDecodeRejectsMalformed(t *testing.T) {
	raw, _ := EncodeFull(Image{Origin: wire(t, "example.test."), Serial: 1, Records: records(t, basicZone(1, "before"))})
	for name, mutate := range map[string]func([]byte) []byte{
		"truncated":        func(b []byte) []byte { return b[:len(b)-1] },
		"trailing":         func(b []byte) []byte { return append(b, 0) },
		"bad magic":        func(b []byte) []byte { b[0] = 'X'; return b },
		"huge count":       func(b []byte) []byte { o := 7 + int(b[6]) + 8; b[o], b[o+1] = 0xff, 0xff; return b },
		"compressed owner": func(b []byte) []byte { o := 7 + int(b[6]) + 16; b[o+1] = 0xc0; return b },
	} {
		c := mutate(append([]byte(nil), raw...))
		if _, _, _, err := Decode(c); err == nil {
			t.Errorf("%s: Decode accepted malformed input", name)
		}
	}
}

func TestOutOfZoneOwnerRefused(t *testing.T) {
	_, err := EncodeFull(Image{Origin: wire(t, "example.test."), Serial: 1, Records: records(t, []string{"www.example.org. 300 IN A 192.0.2.1"})})
	if err == nil {
		t.Fatal("record outside origin accepted")
	}
}

func TestCompressIsContentAddressed(t *testing.T) {
	raw, _ := EncodeFull(Image{Origin: wire(t, "example.test."), Serial: 1, Records: records(t, basicZone(1, "before"))})
	data, sum, err := Compress(raw)
	if err != nil || len(sum) != 64 {
		t.Fatalf("compress: %v %q", err, sum)
	}
	back, err := Decompress(data, 1<<30)
	if err != nil || !bytes.Equal(back, raw) {
		t.Fatalf("decompress: %v", err)
	}
}
