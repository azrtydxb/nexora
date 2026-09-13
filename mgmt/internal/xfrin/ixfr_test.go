package xfrin

import (
	"testing"

	"github.com/miekg/dns"
)

func rr(t *testing.T, s string) dns.RR {
	t.Helper()
	r, err := dns.NewRR(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func soa(t *testing.T, serial string) dns.RR {
	return rr(t, "up.test. 300 IN SOA ns.up.test. h.up.test. "+serial+" 3600 600 86400 300")
}

func TestInterpretIncremental(t *testing.T) {
	a1, a2 := rr(t, "a.up.test. 300 IN A 192.0.2.1"), rr(t, "b.up.test. 300 IN A 192.0.2.2")
	stream := []dns.RR{soa(t, "3"), soa(t, "1"), a1, soa(t, "2"), soa(t, "2"), soa(t, "3"), a2, soa(t, "3")}
	ans, err := Interpret(stream, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if ans.UpToDate || ans.Full != nil || len(ans.Diffs) != 2 || ans.Serial != 3 {
		t.Fatalf("%+v", ans)
	}
	if ans.Diffs[0].FromSerial != 1 || ans.Diffs[0].ToSerial != 2 || len(ans.Diffs[0].Deleted) != 1 || len(ans.Diffs[0].Added) != 0 {
		t.Fatalf("diff 0: %+v", ans.Diffs[0])
	}
	if ans.Diffs[1].FromSerial != 2 || len(ans.Diffs[1].Added) != 1 {
		t.Fatalf("diff 1: %+v", ans.Diffs[1])
	}
}

func TestInterpretAXFRStyleAndUpToDate(t *testing.T) {
	full := []dns.RR{soa(t, "9"), rr(t, "up.test. 300 IN NS ns.up.test."), rr(t, "ns.up.test. 300 IN A 192.0.2.53"), soa(t, "9")}
	ans, err := Interpret(full, 1, true)
	if err != nil || ans.Full == nil || len(ans.Full) != 3 || ans.Serial != 9 {
		t.Fatalf("axfr-style: %+v %v", ans, err)
	}
	ans, err = Interpret([]dns.RR{soa(t, "9")}, 9, true)
	if err != nil || !ans.UpToDate {
		t.Fatalf("up to date: %+v %v", ans, err)
	}
	if _, err := Interpret([]dns.RR{soa(t, "9"), rr(t, "x.up.test. 300 IN A 192.0.2.9")}, 1, false); err == nil {
		t.Fatal("AXFR without closing SOA accepted")
	}
	if _, err := Interpret([]dns.RR{soa(t, "3"), soa(t, "1"), soa(t, "2"), soa(t, "5"), soa(t, "3")}, 1, true); err == nil {
		t.Fatal("non-contiguous IXFR accepted")
	}
}
