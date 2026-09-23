package zonemd

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/miekg/dns"
)

func vector(t *testing.T, name string) []dns.RR {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	src, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../../e2e/testdata/rfc8976", name+".zone"))
	if err != nil {
		t.Fatal(err)
	}
	zp := dns.NewZoneParser(strings.NewReader(string(src)), "example.", name)
	var rrs []dns.RR
	for rr, ok := zp.Next(); ok; rr, ok = zp.Next() {
		rrs = append(rrs, rr)
	}
	if err := zp.Err(); err != nil {
		t.Fatal(err)
	}
	return rrs
}

func TestDigestMatchesRFC8976AppendixA(t *testing.T) {
	checked := 0
	for _, name := range []string{"a1", "a2", "a3"} {
		rrs := vector(t, name)
		for _, rr := range rrs {
			z, ok := rr.(*dns.ZONEMD)
			if !ok || !strings.EqualFold(z.Hdr.Name, "example.") || z.Scheme != SchemeSimple || (z.Hash != HashSHA384 && z.Hash != HashSHA512) {
				continue
			}
			got, err := Digest("example.", rrs, z.Hash)
			if err != nil {
				t.Fatal(err)
			}
			if hex.EncodeToString(got) != strings.ToLower(z.Digest) {
				t.Fatalf("%s hash %d: digest %x, RFC %s", name, z.Hash, got, z.Digest)
			}
			checked++
		}
		if r := Verify("example.", rrs, ModeRequired); r.Status != StatusVerified {
			t.Fatalf("%s: %+v", name, r)
		}
	}
	if checked != 4 { // A.1 SHA-384, A.2 SHA-384, A.3 SHA-384 and SHA-512
		t.Fatalf("checked %d digests, want 4", checked)
	}
	t.Logf("checked %d", checked)
}

func TestVerifyRules(t *testing.T) {
	base := vector(t, "a1")
	apex := func(rrs []dns.RR) *dns.ZONEMD {
		for _, rr := range rrs {
			if z, ok := rr.(*dns.ZONEMD); ok && z.Hdr.Name == "example." {
				return z
			}
		}
		return nil
	}
	clone := func() []dns.RR {
		out := make([]dns.RR, len(base))
		for i, rr := range base {
			out[i] = dns.Copy(rr)
		}
		return out
	}
	without := func() []dns.RR {
		var out []dns.RR
		for _, rr := range clone() {
			if _, ok := rr.(*dns.ZONEMD); !ok {
				out = append(out, rr)
			}
		}
		return out
	}
	cases := []struct {
		name   string
		rrs    func() []dns.RR
		mode   Mode
		status Status
	}{
		{"valid", clone, ModeIfPresent, StatusVerified},
		{"off ignores a broken digest", func() []dns.RR { r := clone(); apex(r).Digest = strings.Repeat("00", 48); return r }, ModeOff, StatusOff},
		{"serial mismatch", func() []dns.RR { r := clone(); apex(r).Serial++; return r }, ModeIfPresent, StatusFailed},
		{"one bit changed", func() []dns.RR { r := clone(); r[len(r)-2].(*dns.A).A[3] ^= 1; return r }, ModeIfPresent, StatusFailed},
		{"unsupported hash only", func() []dns.RR { r := clone(); apex(r).Hash = 200; return r }, ModeIfPresent, StatusFailed},
		{"short digest", func() []dns.RR { r := clone(); apex(r).Digest = "c68090d90a7aed716bc459f9"; return r }, ModeIfPresent, StatusFailed},
		{"duplicate tuple", func() []dns.RR {
			r := clone()
			d := dns.Copy(apex(r)).(*dns.ZONEMD)
			d.Digest = strings.Repeat("ab", 48)
			return append(r, d)
		}, ModeIfPresent, StatusFailed},
		{"absent if_present", without, ModeIfPresent, StatusAbsent},
		{"absent required", without, ModeRequired, StatusFailed},
		{"non-apex only", func() []dns.RR {
			r := without()
			z := dns.Copy(apex(clone())).(*dns.ZONEMD)
			z.Hdr.Name = "ns1.example."
			return append(r, z)
		}, ModeRequired, StatusFailed},
	}
	for _, c := range cases {
		if got := Verify("example.", c.rrs(), c.mode); got.Status != c.status {
			t.Fatalf("%s: %+v, want %s", c.name, got, c.status)
		}
	}
	// Apply on a zone with a placeholder produces a digest Verify accepts.
	rrs := append(without(), Placeholder("example.", 0, 86400))
	if err := Apply("example.", rrs); err != nil {
		t.Fatal(err)
	}
	if got := Verify("example.", rrs, ModeRequired); got.Status != StatusVerified || apex(rrs).Serial != 2018031900 {
		t.Fatalf("applied: %+v serial %d", got, apex(rrs).Serial)
	}
}
