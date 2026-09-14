package authhier

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func start(t *testing.T) (*Hierarchy, Ready) {
	t.Helper()
	h, r, err := Start(context.Background(), DefaultSpec(0), time.Now())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(h.Close)
	if r.Port == 0 {
		t.Fatal("Ready.Port is 0: the shared port was not reported")
	}
	return h, r
}

func ask(t *testing.T, server string, port int, name string, qtype uint16, do bool, network string) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(name, qtype)
	m.RecursionDesired = false
	m.SetEdns0(1232, do)
	c := &dns.Client{Net: network, Timeout: 2 * time.Second}
	r, _, err := c.Exchange(m, net.JoinHostPort(server, strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("%s %s @%s: %v", name, dns.TypeToString[qtype], server, err)
	}
	return r
}

func keysOf(r *dns.Msg) []*dns.DNSKEY {
	var out []*dns.DNSKEY
	for _, rr := range r.Answer {
		if k, ok := rr.(*dns.DNSKEY); ok {
			out = append(out, k)
		}
	}
	return out
}

func verifyA(t *testing.T, r *dns.Msg, keys []*dns.DNSKEY) error {
	t.Helper()
	var set []dns.RR
	var sig *dns.RRSIG
	for _, rr := range r.Answer {
		switch v := rr.(type) {
		case *dns.A:
			set = append(set, v)
		case *dns.RRSIG:
			sig = v
		}
	}
	if sig == nil {
		t.Fatal("no RRSIG")
	}
	for _, k := range keys {
		if k.KeyTag() == sig.KeyTag {
			return sig.Verify(k, set)
		}
	}
	t.Fatal("no key with matching tag")
	return nil
}

func TestRootReferralCarriesSignedDSAndRootDSMatches(t *testing.T) {
	_, ready := start(t)
	r := ask(t, "127.0.53.1", ready.Port, "www.good.test.", dns.TypeA, true, "udp")
	if r.Authoritative || len(r.Ns) == 0 {
		t.Fatalf("expected referral, got %v", r)
	}
	var hasDS, hasSig bool
	for _, rr := range r.Ns {
		hasDS = hasDS || rr.Header().Rrtype == dns.TypeDS
		hasSig = hasSig || rr.Header().Rrtype == dns.TypeRRSIG
	}
	if !hasDS || !hasSig {
		t.Fatalf("referral lacks DS/RRSIG: %v", r.Ns)
	}
	keys := ask(t, "127.0.53.1", ready.Port, ".", dns.TypeDNSKEY, true, "udp")
	var matched bool
	for _, k := range keysOf(keys) {
		ds := k.ToDS(dns.SHA256)
		text := fmt.Sprintf("%d %d %d %s", ds.KeyTag, ds.Algorithm, ds.DigestType, strings.ToUpper(ds.Digest))
		if k.Flags == 257 && text == ready.RootDS {
			matched = true
		}
	}
	if !matched {
		t.Fatalf("root DS %q matches no root KSK", ready.RootDS)
	}
}

func TestSignedGoodVerifiesAndBadDoesNot(t *testing.T) {
	_, ready := start(t)
	good := ask(t, "127.0.53.3", ready.Port, "www.good.test.", dns.TypeA, true, "udp")
	if err := verifyA(t, good, keysOf(ask(t, "127.0.53.3", ready.Port, "good.test.", dns.TypeDNSKEY, true, "udp"))); err != nil {
		t.Fatalf("good.test signature: %v", err)
	}
	bad := ask(t, "127.0.53.4", ready.Port, "www.bad.test.", dns.TypeA, true, "udp")
	if err := verifyA(t, bad, keysOf(ask(t, "127.0.53.4", ready.Port, "bad.test.", dns.TypeDNSKEY, true, "udp"))); err == nil {
		t.Fatal("bad.test signature unexpectedly verifies")
	}
}

func TestNegativeAnswersCarryDenialProofs(t *testing.T) {
	_, ready := start(t)
	nx := ask(t, "127.0.53.3", ready.Port, "nope.good.test.", dns.TypeA, true, "udp")
	if nx.Rcode != dns.RcodeNameError || countType(nx.Ns, dns.TypeNSEC) < 2 {
		t.Fatalf("NSEC NXDOMAIN proof missing: %v", nx)
	}
	n3 := ask(t, "127.0.53.8", ready.Port, "nope.n3.test.", dns.TypeA, true, "udp")
	if n3.Rcode != dns.RcodeNameError || countType(n3.Ns, dns.TypeNSEC3) < 2 {
		t.Fatalf("NSEC3 NXDOMAIN proof missing: %v", n3)
	}
	ds := ask(t, "127.0.53.2", ready.Port, "plain.test.", dns.TypeDS, true, "udp")
	if len(ds.Answer) != 0 || countType(ds.Ns, dns.TypeNSEC) != 1 {
		t.Fatalf("insecure delegation proof missing: %v", ds)
	}
}

// TestWildcardAnswerCarriesExpandedSignatureAndNextCloserProof checks RFC 1034 section 4.3.3
// synthesis: the expanded answer keeps the wildcard's signature (labels below the owner's count)
// and carries the NSEC covering the next closer name, NODATA below the wildcard is proven, and a
// zone with OmitWildcardProof leaves the proof out.
func TestWildcardAnswerCarriesExpandedSignatureAndNextCloserProof(t *testing.T) {
	_, ready := start(t)
	r := ask(t, "127.0.53.3", ready.Port, "x.w.good.test.", dns.TypeA, true, "udp")
	if r.Rcode != dns.RcodeSuccess {
		t.Fatalf("x.w.good.test. A rcode = %s, want NOERROR", dns.RcodeToString[r.Rcode])
	}
	var as []dns.RR
	var sig *dns.RRSIG
	for _, rr := range r.Answer {
		switch v := rr.(type) {
		case *dns.A:
			as = append(as, v)
		case *dns.RRSIG:
			sig = v
		}
	}
	if len(as) != 1 || as[0].Header().Name != "x.w.good.test." || as[0].(*dns.A).A.String() != "192.0.2.60" {
		t.Fatalf("synthesised answer = %v, want one A 192.0.2.60 owned by x.w.good.test.", as)
	}
	if sig == nil || sig.Hdr.Name != "x.w.good.test." || sig.Labels != 3 {
		t.Fatalf("expanded RRSIG = %v, want owner x.w.good.test. with labels 3", sig)
	}
	var zsk *dns.DNSKEY
	for _, k := range keysOf(ask(t, "127.0.53.3", ready.Port, "good.test.", dns.TypeDNSKEY, true, "udp")) {
		if k.Flags == 256 && k.KeyTag() == sig.KeyTag {
			zsk = k
		}
	}
	if zsk == nil {
		t.Fatal("no good.test. ZSK matches the wildcard signature")
	}
	// Verify checks that the RRset and RRSIG owners agree, so both go back to the wildcard owner.
	wildcard, wildSig := dns.Copy(as[0]), dns.Copy(sig).(*dns.RRSIG)
	wildcard.Header().Name, wildSig.Hdr.Name = "*.w.good.test.", "*.w.good.test."
	if err := wildSig.Verify(zsk, []dns.RR{wildcard}); err != nil {
		t.Fatalf("wildcard signature does not verify: %v", err)
	}
	var proof bool
	for _, rr := range r.Ns {
		if n, ok := rr.(*dns.NSEC); ok && canonicalLess(n.Hdr.Name, "x.w.good.test.") && canonicalLess("x.w.good.test.", n.NextDomain) {
			proof = true
		}
	}
	if !proof {
		t.Fatalf("no NSEC covering the next closer name x.w.good.test. in %v", r.Ns)
	}

	nodata := ask(t, "127.0.53.3", ready.Port, "x.w.good.test.", dns.TypeAAAA, true, "udp")
	if nodata.Rcode != dns.RcodeSuccess || len(nodata.Answer) != 0 || countType(nodata.Ns, dns.TypeSOA) != 1 || countType(nodata.Ns, dns.TypeNSEC) == 0 {
		t.Fatalf("wildcard NODATA: %v", nodata)
	}

	// The wildcard owner itself is an exact match, and no name below an existing name is synthesised.
	if own := ask(t, "127.0.53.3", ready.Port, "*.w.good.test.", dns.TypeTXT, true, "udp"); countType(own.Answer, dns.TypeTXT) != 1 || own.Answer[0].Header().Name != "*.w.good.test." {
		t.Fatalf("wildcard owner TXT: %v", own)
	}
	if below := ask(t, "127.0.53.3", ready.Port, "x.www.good.test.", dns.TypeA, true, "udp"); below.Rcode != dns.RcodeNameError {
		t.Fatalf("x.www.good.test. rcode = %s, want NXDOMAIN", dns.RcodeToString[below.Rcode])
	}

	n3 := ask(t, "127.0.53.8", ready.Port, "x.w.n3.test.", dns.TypeA, true, "udp")
	if countType(n3.Answer, dns.TypeA) != 1 || countType(n3.Ns, dns.TypeNSEC3) == 0 {
		t.Fatalf("NSEC3 wildcard answer lacks the answer or the next-closer NSEC3: %v", n3)
	}

	bare := ask(t, "127.0.53.12", ready.Port, "x.w.wild.test.", dns.TypeA, true, "udp")
	if countType(bare.Answer, dns.TypeA) != 1 || countType(bare.Ns, dns.TypeNSEC) != 0 {
		t.Fatalf("wild.test. must answer without the next-closer NSEC: %v", bare)
	}
}

func countType(rrs []dns.RR, t uint16) int {
	n := 0
	for _, rr := range rrs {
		if rr.Header().Rrtype == t {
			n++
		}
	}
	return n
}

func TestBigAnswerTruncatesOverUDPAndCompletesOverTCP(t *testing.T) {
	_, ready := start(t)
	udp := ask(t, "127.0.53.3", ready.Port, "big.good.test.", dns.TypeTXT, false, "udp")
	if !udp.Truncated {
		t.Fatal("expected TC=1 over UDP")
	}
	tcp := ask(t, "127.0.53.3", ready.Port, "big.good.test.", dns.TypeTXT, false, "tcp")
	if tcp.Truncated || len(tcp.Answer) != 40 {
		t.Fatalf("tcp answer: tc=%v n=%d", tcp.Truncated, len(tcp.Answer))
	}
}

func TestSpoofServerSendsForgeriesBeforeRealAnswer(t *testing.T) {
	h, ready := start(t)
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP("127.0.53.7"), Port: ready.Port})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	q := new(dns.Msg)
	q.SetQuestion("wWw.SpOof.test.", dns.TypeA)
	q.Id = 4242
	b, _ := q.Pack()
	if _, err := conn.Write(b); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var got []*dns.Msg
	buf := make([]byte, 1500)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			break
		}
		m := new(dns.Msg)
		if m.Unpack(buf[:n]) == nil {
			got = append(got, m)
		}
	}
	if len(got) != 4 {
		t.Fatalf("received %d replies on the query socket, want 4 (wrong id, wrong question, wrong case, real)", len(got))
	}
	last := got[len(got)-1]
	if last.Id != 4242 || last.Question[0].Name != "wWw.SpOof.test." || last.Answer[0].(*dns.A).A.String() != "192.0.2.77" {
		t.Fatalf("real answer wrong: %v", last)
	}
	if h.Stats().SpoofsSent != 4 {
		t.Fatalf("spoofs_sent = %d, want 4 (3 on socket + 1 from another port)", h.Stats().SpoofsSent)
	}
}

func TestForwarderEndpointAnswersRecursivelyWithSignatures(t *testing.T) {
	_, ready := start(t)
	host, portStr, _ := net.SplitHostPort(ready.Forwarder)
	port, _ := strconv.Atoi(portStr)
	m := new(dns.Msg)
	m.SetQuestion("www.good.test.", dns.TypeA)
	m.SetEdns0(1232, true)
	r, _, err := (&dns.Client{Timeout: 2 * time.Second}).Exchange(m, net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil || !r.RecursionAvailable || countType(r.Answer, dns.TypeRRSIG) != 1 {
		t.Fatalf("forwarder answer: %v %v", r, err)
	}
}

func TestLameDelegationHasUnreachableLameAndWorkingServers(t *testing.T) {
	_, ready := start(t)
	ref := ask(t, "127.0.53.2", ready.Port, "www.lame.test.", dns.TypeA, false, "udp")
	if countType(ref.Ns, dns.TypeNS) != 3 {
		t.Fatalf("lame.test. referral should name 3 servers: %v", ref)
	}
	if r := ask(t, "127.0.53.5", ready.Port, "www.lame.test.", dns.TypeA, false, "udp"); r.Rcode != dns.RcodeRefused {
		t.Fatalf("lame server rcode = %s, want REFUSED", dns.RcodeToString[r.Rcode])
	}
	m := new(dns.Msg)
	m.SetQuestion("www.lame.test.", dns.TypeA)
	if _, _, err := (&dns.Client{Timeout: 300 * time.Millisecond}).Exchange(m, net.JoinHostPort("127.0.53.10", strconv.Itoa(ready.Port))); err == nil {
		t.Fatal("127.0.53.10 must not answer")
	}
	if r := ask(t, "127.0.53.11", ready.Port, "www.lame.test.", dns.TypeA, false, "udp"); !r.Authoritative || len(r.Answer) != 1 {
		t.Fatalf("working lame.test. server: %v", r)
	}
}

// TestDelvValidatesTheHierarchyIndependently checks the signing with BIND's delv (no engine):
// through the forwarder endpoint, under the per-run root DS, good.test. validates, bad.test.
// fails, the NSEC3 denial of n3.test. validates, plain.test. is provably insecure, wildcard answers
// and wildcard NODATA validate and wild.test.'s wildcard answer without its next-closer proof fails.
func TestDelvValidatesTheHierarchyIndependently(t *testing.T) {
	delv, err := exec.LookPath("delv")
	if err != nil {
		t.Skip("delv (bind9-dnsutils) is not installed")
	}
	_, ready := start(t)
	tag, rest, _ := strings.Cut(ready.RootDS, " ")
	anchors := filepath.Join(t.TempDir(), "anchors.conf")
	fields := strings.Fields(rest)
	conf := fmt.Sprintf("trust-anchors { . static-ds %s %s %s \"%s\"; };\n", tag, fields[0], fields[1], fields[2])
	if err := os.WriteFile(anchors, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	host, port, _ := net.SplitHostPort(ready.Forwarder)
	for _, c := range []struct{ name, qtype, want string }{
		{"www.good.test.", "A", "; fully validated"},
		{"alias.good.test.", "A", "; fully validated"},
		{"nope.good.test.", "A", "; negative response, fully validated"},
		{"www.n3.test.", "A", "; fully validated"},
		{"nope.n3.test.", "A", "; negative response, fully validated"},
		{"www.plain.test.", "A", "; unsigned answer"},
		{"www.glueless.test.", "A", "; unsigned answer"},
		{"www.lame.test.", "A", "; unsigned answer"},
		{"www.bad.test.", "A", "resolution failed"},
		{"x.w.good.test.", "A", "; fully validated"},
		{"x.w.good.test.", "AAAA", "; negative response, fully validated"},
		{"x.w.n3.test.", "A", "; fully validated"},
		{"x.w.wild.test.", "A", "resolution failed"},
	} {
		// delv is resolved from $PATH and every argument is built by this test.
		out, _ := exec.Command(delv, "@"+host, "-p", port, "-a", anchors, "+root=.", c.name, c.qtype).CombinedOutput() // nosemgrep: dangerous-exec-command
		if !strings.Contains(string(out), c.want) {
			t.Errorf("delv %s %s: want %q in\n%s", c.name, c.qtype, c.want, out)
		}
	}
}
