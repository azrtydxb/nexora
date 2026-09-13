package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

type dnsConfig struct {
	UDP, TCP, DoT, DoH, Control, CertDir string
}

// dnsFixture is an upstream DNS server over UDP, TCP, DoT and DoH whose answers depend on the
// first label of the query name, with a JSON control API.
type dnsFixture struct {
	dnsServers  []*dns.Server
	httpServers []*http.Server

	mu     sync.Mutex
	counts map[string]int
	total  int
	mode   string
	delay  time.Duration
}

const maxDNSMessage = 65535

func runDNS(args []string) (func(), error) {
	fs := flag.NewFlagSet("dns", flag.ContinueOnError)
	var cfg dnsConfig
	fs.StringVar(&cfg.UDP, "udp", "", "UDP listen address")
	fs.StringVar(&cfg.TCP, "tcp", "", "TCP listen address")
	fs.StringVar(&cfg.DoT, "dot", "", "DoT listen address")
	fs.StringVar(&cfg.DoH, "doh", "", "DoH listen address")
	fs.StringVar(&cfg.Control, "control", "", "control API listen address")
	fs.StringVar(&cfg.CertDir, "cert-dir", "", "directory for the generated CA and server certificate")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	fx, err := startDNSFixture(cfg)
	if err != nil {
		return nil, err
	}
	return fx.Close, nil
}

func startDNSFixture(cfg dnsConfig) (*dnsFixture, error) {
	if cfg.UDP == "" || cfg.TCP == "" || cfg.DoT == "" || cfg.DoH == "" || cfg.Control == "" || cfg.CertDir == "" {
		return nil, errors.New("--udp, --tcp, --dot, --doh, --control and --cert-dir are required")
	}
	_, cert, err := writeCerts(cfg.CertDir)
	if err != nil {
		return nil, err
	}
	f := &dnsFixture{counts: map[string]int{}, mode: "normal"}
	fail := func(err error) (*dnsFixture, error) {
		f.Close()
		return nil, err
	}

	pc, err := net.ListenPacket("udp", cfg.UDP)
	if err != nil {
		return fail(err)
	}
	f.serveDNS(&dns.Server{Net: "udp", PacketConn: pc})
	tl, err := net.Listen("tcp", cfg.TCP)
	if err != nil {
		return fail(err)
	}
	f.serveDNS(&dns.Server{Net: "tcp", Listener: tl})
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	dl, err := tls.Listen("tcp", cfg.DoT, tlsCfg)
	if err != nil {
		return fail(err)
	}
	f.serveDNS(&dns.Server{Net: "tcp-tls", Listener: dl})

	doh := http.NewServeMux()
	doh.HandleFunc("POST /dns-query", f.dohPost)
	doh.HandleFunc("GET /dns-query", f.dohGet)
	hl, err := net.Listen("tcp", cfg.DoH)
	if err != nil {
		return fail(err)
	}
	dohSrv := &http.Server{Handler: doh, ReadHeaderTimeout: 5 * time.Second,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}, MinVersion: tls.VersionTLS12}}
	f.httpServers = append(f.httpServers, dohSrv)
	go func() { _ = dohSrv.ServeTLS(hl, "", "") }()

	ctl := http.NewServeMux()
	ctl.HandleFunc("GET /stats", f.stats)
	ctl.HandleFunc("POST /reset", f.reset)
	ctl.HandleFunc("POST /mode", f.setModeHTTP)
	ctl.HandleFunc("POST /delay", f.setDelayHTTP)
	cl, err := net.Listen("tcp", cfg.Control)
	if err != nil {
		return fail(err)
	}
	ctlSrv := &http.Server{Handler: ctl, ReadHeaderTimeout: 5 * time.Second}
	f.httpServers = append(f.httpServers, ctlSrv)
	go func() { _ = ctlSrv.Serve(cl) }()
	return f, nil
}

func (f *dnsFixture) serveDNS(s *dns.Server) {
	s.Handler = dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		_, udp := w.LocalAddr().(*net.UDPAddr)
		if reply := f.respond(req, udp); reply != nil {
			_ = w.WriteMsg(reply)
		}
	})
	f.dnsServers = append(f.dnsServers, s)
	go func() { _ = s.ActivateAndServe() }()
}

// Close stops every listener.
func (f *dnsFixture) Close() {
	for _, s := range f.dnsServers {
		_ = s.Shutdown()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, s := range f.httpServers {
		_ = s.Shutdown(ctx)
	}
}

func (f *dnsFixture) count(name string, qtype uint16) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[countKey(name, qtype)]
}

func (f *dnsFixture) setMode(mode string) {
	f.mu.Lock()
	f.mode = mode
	f.mu.Unlock()
}

func countKey(name string, qtype uint16) string {
	return strings.ToLower(name) + "|" + strconv.Itoa(int(qtype))
}

// respond records the query and builds its reply; nil means no reply (blackhole mode or a
// query without exactly one question).
func (f *dnsFixture) respond(req *dns.Msg, udp bool) *dns.Msg {
	if len(req.Question) != 1 {
		return nil
	}
	q := req.Question[0]
	f.mu.Lock()
	f.counts[countKey(q.Name, q.Qtype)]++
	f.total++
	mode, delay := f.mode, f.delay
	f.mu.Unlock()
	if mode == "blackhole" {
		return nil
	}
	time.Sleep(delay)
	m := new(dns.Msg)
	m.SetReply(req)
	m.Compress = true
	if mode == "servfail" {
		m.Rcode = dns.RcodeServerFailure
		return m
	}
	label := strings.ToLower(strings.SplitN(q.Name, ".", 2)[0])
	hdr := func(name string, t uint16, ttl uint32) dns.RR_Header {
		return dns.RR_Header{Name: name, Rrtype: t, Class: dns.ClassINET, Ttl: ttl}
	}
	a := func(ip net.IP, ttl uint32) dns.RR { return &dns.A{Hdr: hdr(q.Name, dns.TypeA, ttl), A: ip} }
	hundredA := func() {
		for i := range 100 {
			m.Answer = append(m.Answer, a(net.IPv4(198, 51, 100, byte(i+1)), 300))
		}
	}
	switch {
	case strings.HasPrefix(label, "nx-"):
		m.Rcode = dns.RcodeNameError
		m.Ns = []dns.RR{&dns.SOA{Hdr: hdr("example.", dns.TypeSOA, 900), Ns: "ns.example.", Mbox: "h.example.",
			Serial: 1, Refresh: 2, Retry: 3, Expire: 4, Minttl: 120}}
	case strings.HasPrefix(label, "tc-") && udp:
		m.Truncated = true
	case strings.HasPrefix(label, "tc-") || strings.HasPrefix(label, "big-"):
		hundredA()
	case strings.HasPrefix(label, "cloak-"):
		const target = "cdn.tracker.blocked.test."
		m.Answer = []dns.RR{
			&dns.CNAME{Hdr: hdr(q.Name, dns.TypeCNAME, 300), Target: target},
			&dns.A{Hdr: hdr(target, dns.TypeA, 300), A: net.IPv4(192, 0, 2, 9)},
		}
	case q.Qtype == dns.TypeA && strings.HasPrefix(label, "zero-"):
		m.Answer = []dns.RR{a(net.IPv4(192, 0, 2, 1), 0)}
	case q.Qtype == dns.TypeA:
		m.Answer = []dns.RR{a(net.IPv4(192, 0, 2, 1), 300)}
	case q.Qtype == dns.TypeAAAA:
		m.Answer = []dns.RR{&dns.AAAA{Hdr: hdr(q.Name, dns.TypeAAAA, 300), AAAA: net.ParseIP("2001:db8::1")}}
	}
	return m
}

func (f *dnsFixture) dohPost(w http.ResponseWriter, r *http.Request) {
	wire, err := io.ReadAll(io.LimitReader(r.Body, maxDNSMessage+1))
	if err != nil || len(wire) > maxDNSMessage || r.Header.Get("Content-Type") != "application/dns-message" {
		http.Error(w, "bad DoH request", http.StatusBadRequest)
		return
	}
	f.doh(w, r, wire)
}

func (f *dnsFixture) dohGet(w http.ResponseWriter, r *http.Request) {
	wire, err := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("dns"))
	if err != nil || len(wire) > maxDNSMessage {
		http.Error(w, "bad dns parameter", http.StatusBadRequest)
		return
	}
	f.doh(w, r, wire)
}

func (f *dnsFixture) doh(w http.ResponseWriter, r *http.Request, wire []byte) {
	req := new(dns.Msg)
	if err := req.Unpack(wire); err != nil {
		http.Error(w, "malformed DNS message", http.StatusBadRequest)
		return
	}
	reply := f.respond(req, false)
	if reply == nil {
		<-r.Context().Done() // blackhole: hold the request until the client gives up
		return
	}
	out, err := reply.Pack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/dns-message")
	_, _ = w.Write(out)
}

func (f *dnsFixture) stats(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	body := map[string]any{"total": f.total, "queries": f.counts}
	out, err := json.Marshal(body)
	f.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

func (f *dnsFixture) reset(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	f.counts, f.total, f.mode, f.delay = map[string]int{}, 0, "normal", 0
	f.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (f *dnsFixture) setModeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch body.Mode {
	case "normal", "blackhole", "servfail":
		f.setMode(body.Mode)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "mode must be normal, blackhole or servfail", http.StatusBadRequest)
	}
}

func (f *dnsFixture) setDelayHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MS int `json:"ms"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || body.MS < 0 {
		http.Error(w, "body must be {\"ms\": n} with n >= 0", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.delay = time.Duration(body.MS) * time.Millisecond
	f.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}
