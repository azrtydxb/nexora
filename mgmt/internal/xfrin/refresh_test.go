package xfrin_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"github.com/piwi3910/nexora/mgmt/internal/tsigkey"
	"github.com/piwi3910/nexora/mgmt/internal/xfrin"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

var actor = auth.Actor{Type: "user", ID: "t", Name: "t"}

type fakePrimary struct {
	mu      sync.Mutex
	serial  uint32
	records []string
	history map[uint32][2][]string // from serial -> (deleted, added) to serial+1
	queries []uint16
	signed  bool // requests must carry a valid TSIG; responses are signed
}

func (p *fakePrimary) soa() dns.RR {
	r, _ := dns.NewRR("up.test. 300 IN SOA ns.up.test. h.up.test. " + itoa(p.serial) + " 3600 600 86400 300")
	return r
}

func (p *fakePrimary) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	p.mu.Lock()
	defer p.mu.Unlock()
	q := req.Question[0]
	p.queries = append(p.queries, q.Qtype)
	ts := req.IsTsig()
	if p.signed && (ts == nil || w.TsigStatus() != nil) {
		m := new(dns.Msg)
		m.SetRcode(req, dns.RcodeNotAuth)
		_ = w.WriteMsg(m)
		return
	}
	switch q.Qtype {
	case dns.TypeSOA:
		m := new(dns.Msg)
		m.SetReply(req)
		m.Authoritative = true
		m.Answer = []dns.RR{p.soa()}
		if p.signed {
			m.SetTsig(ts.Hdr.Name, ts.Algorithm, 300, time.Now().Unix())
		}
		_ = w.WriteMsg(m)
	case dns.TypeAXFR, dns.TypeIXFR:
		var rrs []dns.RR
		rrs = append(rrs, p.soa())
		if q.Qtype == dns.TypeIXFR {
			from := req.Ns[0].(*dns.SOA).Serial
			if h, ok := p.history[from]; ok && from+1 == p.serial {
				old, _ := dns.NewRR("up.test. 300 IN SOA ns.up.test. h.up.test. " + itoa(from) + " 3600 600 86400 300")
				rrs = append(rrs, old)
				rrs = append(rrs, parse(h[0])...)
				rrs = append(rrs, p.soa())
				rrs = append(rrs, parse(h[1])...)
				rrs = append(rrs, p.soa())
				ch := make(chan *dns.Envelope, 1)
				ch <- &dns.Envelope{RR: rrs}
				close(ch)
				_ = (&dns.Transfer{}).Out(w, req, ch)
				return
			}
		}
		rrs = append(rrs, parse(p.records)...)
		rrs = append(rrs, p.soa())
		ch := make(chan *dns.Envelope, 1)
		ch <- &dns.Envelope{RR: rrs}
		close(ch)
		_ = (&dns.Transfer{}).Out(w, req, ch)
	}
}

func TestRefreshAXFRThenIXFRThenUpToDate(t *testing.T) {
	ctx := context.Background()
	p := &fakePrimary{serial: 10, records: []string{"up.test. 300 IN NS ns.up.test.", "ns.up.test. 300 IN A 192.0.2.53", "a.up.test. 300 IN A 192.0.2.1"}, history: map[uint32][2][]string{}}
	addr := startPrimary(t, p)
	st := storetest.New(t)
	zs := &zone.Service{Store: st, Now: time.Now}
	z, err := zs.CreateZone(ctx, actor, zone.CreateZoneInput{Name: "up.test.", Kind: "secondary", Primaries: []zone.Endpoint{{Address: addr}}})
	if err != nil {
		t.Fatal(err)
	}
	r := &xfrin.Refresher{Store: st, Zones: zs, Now: time.Now, Dial: 2 * time.Second}
	if err := r.Refresh(ctx, z.ID, "timer"); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	got, _ := zs.GetZone(ctx, z.ID)
	if !got.Loaded || got.Serial != 10 || got.LastTrigger != "timer" || got.NextRefreshAt.Sub(*got.LastSuccessAt) != 3600*time.Second || got.ExpiresAt.Sub(*got.LastSuccessAt) != 86400*time.Second {
		t.Fatalf("after AXFR: %+v", got)
	}

	p.mu.Lock()
	p.history[10] = [2][]string{{"a.up.test. 300 IN A 192.0.2.1"}, {"b.up.test. 300 IN A 192.0.2.2"}}
	p.records = []string{"up.test. 300 IN NS ns.up.test.", "ns.up.test. 300 IN A 192.0.2.53", "b.up.test. 300 IN A 192.0.2.2"}
	p.serial = 11
	p.queries = nil
	p.mu.Unlock()
	if err := r.Refresh(ctx, z.ID, "notify"); err != nil {
		t.Fatalf("ixfr refresh: %v", err)
	}
	recs, _, _ := zs.ListRecords(ctx, z.ID, "b.up.test.", "A", "", 10)
	gone, _, _ := zs.ListRecords(ctx, z.ID, "a.up.test.", "A", "", 10)
	got, _ = zs.GetZone(ctx, z.ID)
	p.mu.Lock()
	queries := append([]uint16(nil), p.queries...)
	p.mu.Unlock()
	if got.Serial != 11 || len(recs) != 1 || len(gone) != 0 || queries[len(queries)-1] != dns.TypeIXFR {
		t.Fatalf("after IXFR: serial=%d b=%d a=%d queries=%v", got.Serial, len(recs), len(gone), queries)
	}
	var from, to int64
	_ = st.Pool.QueryRow(ctx, `SELECT from_serial, to_serial FROM zone_journal WHERE zone_id=$1 ORDER BY seq DESC LIMIT 1`, z.ID).Scan(&from, &to)
	if from != 10 || to != 11 {
		t.Fatalf("journal keeps the primary's serials: %d->%d", from, to)
	}

	p.mu.Lock()
	p.queries = nil
	p.mu.Unlock()
	if err := r.Refresh(ctx, z.ID, "timer"); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queries) != 1 || p.queries[0] != dns.TypeSOA {
		t.Fatalf("up-to-date refresh must stop after the SOA query: %v", p.queries)
	}
}

func TestRefreshFailureRetriesAndExpires(t *testing.T) {
	ctx := context.Background()
	p := &fakePrimary{serial: 5, records: []string{"up.test. 300 IN NS ns.up.test."}}
	addr := startPrimary(t, p)
	st := storetest.New(t)
	zs := &zone.Service{Store: st, Now: time.Now}
	z, _ := zs.CreateZone(ctx, actor, zone.CreateZoneInput{Name: "up.test.", Kind: "secondary", Primaries: []zone.Endpoint{{Address: addr}}})
	r := &xfrin.Refresher{Store: st, Zones: zs, Now: time.Now, Dial: 500 * time.Millisecond}
	if err := r.Refresh(ctx, z.ID, "timer"); err != nil {
		t.Fatal(err)
	}
	dead := deadAddr(t)
	_, _ = st.Pool.Exec(ctx, `UPDATE zones SET primaries = jsonb_build_array(jsonb_build_object('address', $2::text)), expires_at = now() - interval '1 second' WHERE id=$1`, z.ID, dead)
	if err := r.Refresh(ctx, z.ID, "timer"); err == nil {
		t.Fatal("refresh against a dead primary succeeded")
	}
	got, _ := zs.GetZone(ctx, z.ID)
	if !got.Expired || got.LastError == "" || got.NextRefreshAt.Sub(*got.LastRefreshAt) != 600*time.Second {
		t.Fatalf("after failure: expired=%v err=%q next=%v", got.Expired, got.LastError, got.NextRefreshAt)
	}
}

func TestRefreshWithTSIGRequiresSignedPrimary(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	keys := &tsigkey.Service{Store: st, Box: kekBox(t)}
	secret := base64.StdEncoding.EncodeToString([]byte("xfrin-test-secret-0123456789abcdef"))
	key, err := keys.Create(ctx, actor, "xfr-in.", "hmac-sha256", secret)
	if err != nil {
		t.Fatal(err)
	}
	signed := &fakePrimary{serial: 3, records: []string{"up.test. 300 IN NS ns.up.test."}, signed: true}
	good := startPrimaryWithKeys(t, signed, map[string]string{"xfr-in.": secret})
	// A primary that does not know the key answers unsigned: the refresh must not accept it.
	bad := startPrimary(t, &fakePrimary{serial: 4, records: []string{"up.test. 300 IN NS ns.up.test."}})
	zs := &zone.Service{Store: st, Now: time.Now}
	r := &xfrin.Refresher{Store: st, Zones: zs, TSIG: keys, Now: time.Now, Dial: time.Second}

	z, err := zs.CreateZone(ctx, actor, zone.CreateZoneInput{Name: "up.test.", Kind: "secondary", Primaries: []zone.Endpoint{{Address: bad, TSIGKeyID: &key.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Refresh(ctx, z.ID, "timer"); err == nil || !strings.Contains(err.Error(), "TSIG") {
		t.Fatalf("unsigned primary accepted: %v", err)
	}
	if got, _ := zs.GetZone(ctx, z.ID); got.Loaded {
		t.Fatal("zone loaded from an unsigned answer")
	}

	_, _ = st.Pool.Exec(ctx, `UPDATE zones SET primaries = jsonb_build_array(jsonb_build_object('address', $2::text, 'tsig_key_id', $3::text)) WHERE id=$1`, z.ID, good, key.ID)
	if err := r.Refresh(ctx, z.ID, "timer"); err != nil {
		t.Fatalf("signed refresh: %v", err)
	}
	if got, _ := zs.GetZone(ctx, z.ID); !got.Loaded || got.Serial != 3 {
		t.Fatalf("after signed AXFR: %+v", got)
	}
}

func kekBox(t *testing.T) *secrets.Box {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.Open(secrets.Config{KEKFile: p})
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func itoa(v uint32) string { return strconv.FormatUint(uint64(v), 10) }

func parse(in []string) []dns.RR {
	out := make([]dns.RR, 0, len(in))
	for _, s := range in {
		r, err := dns.NewRR(s)
		if err != nil {
			panic(err)
		}
		out = append(out, r)
	}
	return out
}

// startPrimary serves p on UDP and TCP at one loopback port and returns "127.0.0.1:port".
func startPrimary(t *testing.T, p *fakePrimary) string {
	t.Helper()
	return startPrimaryWithKeys(t, p, nil)
}

// startPrimaryWithKeys is startPrimary with TSIG secrets (key name -> base64 secret).
func startPrimaryWithKeys(t *testing.T, p *fakePrimary, keys map[string]string) string {
	t.Helper()
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udp, err := net.ListenPacket("udp", tcp.Addr().String())
	if err != nil {
		_ = tcp.Close()
		t.Fatal(err)
	}
	for _, srv := range []*dns.Server{{Listener: tcp, Handler: p, TsigSecret: keys}, {PacketConn: udp, Handler: p, TsigSecret: keys}} {
		started := make(chan struct{})
		srv.NotifyStartedFunc = func() { close(started) }
		go func() { _ = srv.ActivateAndServe() }()
		<-started
		t.Cleanup(func() { _ = srv.Shutdown() })
	}
	return tcp.Addr().String()
}

// deadAddr returns the address of a TCP listener that was closed again.
func deadAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}
