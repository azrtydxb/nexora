package authhier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

// Hierarchy is a running set of authoritative servers, the forwarder endpoint and the stats
// listener.
type Hierarchy struct {
	zones   []*zone
	servers []*dns.Server
	stats   *http.Server

	mu         sync.Mutex
	queries    map[string]int
	spoofsSent int
}

const (
	// bindAttempts bounds how often the shared port is re-picked when another address already has
	// it in use.
	bindAttempts = 20
	// cnameChain bounds how many CNAMEs one answer follows.
	cnameChain = 8
	spoofDelay = 30 * time.Millisecond
)

var spoofAddr = net.IPv4(6, 6, 6, 6)

// Start builds and signs spec's zones (children before parents, so each parent carries its signed
// children's DS) and serves them until Close or until ctx is done.
func Start(ctx context.Context, spec Spec, now time.Time) (*Hierarchy, Ready, error) {
	h := &Hierarchy{queries: map[string]int{}}
	zones, err := buildHierarchy(spec, now)
	if err != nil {
		return nil, Ready{}, err
	}
	h.zones = zones

	type endpoint struct {
		ip      string
		handler dns.Handler
	}
	var eps []endpoint
	seen := map[string]bool{}
	for i := range spec.Zones {
		z := zones[i]
		if seen[z.spec.ServerIP] {
			return nil, Ready{}, fmt.Errorf("server address %s is used twice", z.spec.ServerIP)
		}
		seen[z.spec.ServerIP] = true
		eps = append(eps, endpoint{z.spec.ServerIP, h.handler(z.spec.ServerIP, z.spec.Spoof, false,
			func(name string, qtype uint16, do bool) *dns.Msg { return z.respond(name, qtype, do) })})
	}
	if spec.ForwarderIP != "" {
		if seen[spec.ForwarderIP] {
			return nil, Ready{}, fmt.Errorf("forwarder address %s is also a zone server", spec.ForwarderIP)
		}
		eps = append(eps, endpoint{spec.ForwarderIP, h.handler(spec.ForwarderIP, false, true, h.recurse)})
	}
	if len(eps) == 0 {
		return nil, Ready{}, errors.New("spec has no zones")
	}

	var conns []net.PacketConn
	var listeners []net.Listener
	closeAll := func() {
		for _, c := range conns {
			_ = c.Close()
		}
		for _, l := range listeners {
			_ = l.Close()
		}
		conns, listeners = nil, nil
	}
	var port int
	for attempt := 1; ; attempt++ {
		port = spec.Port
		err = nil
		for _, ep := range eps {
			var pc net.PacketConn
			pc, err = net.ListenPacket("udp", net.JoinHostPort(ep.ip, strconv.Itoa(port)))
			if err != nil {
				break
			}
			conns = append(conns, pc)
			port = pc.LocalAddr().(*net.UDPAddr).Port
			var l net.Listener
			l, err = net.Listen("tcp", net.JoinHostPort(ep.ip, strconv.Itoa(port)))
			if err != nil {
				break
			}
			listeners = append(listeners, l)
		}
		if err == nil {
			break
		}
		closeAll()
		if spec.Port != 0 || !errors.Is(err, syscall.EADDRINUSE) || attempt == bindAttempts {
			return nil, Ready{}, err
		}
	}
	for i, ep := range eps {
		h.serve(&dns.Server{Net: "udp", PacketConn: conns[i], Handler: ep.handler})
		h.serve(&dns.Server{Net: "tcp", Listener: listeners[i], Handler: ep.handler})
	}

	sl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		h.Close()
		return nil, Ready{}, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(h.Stats())
	})
	h.stats = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = h.stats.Serve(sl) }()
	context.AfterFunc(ctx, h.Close)

	ready := Ready{Port: port, StatsURL: "http://" + sl.Addr().String()}
	if spec.ForwarderIP != "" {
		ready.Forwarder = net.JoinHostPort(spec.ForwarderIP, strconv.Itoa(port))
	}
	for _, z := range zones {
		if z.origin != "." {
			continue
		}
		if z.ksk != nil {
			ready.RootDS = dsText(z.ksk.ToDS(dns.SHA256))
		}
		for _, rr := range z.sets[rrKey{".", dns.TypeNS}] {
			ready.RootHints = append(ready.RootHints, RootHint{Name: rr.(*dns.NS).Ns, Addresses: []string{z.spec.ServerIP}})
		}
	}
	return h, ready, nil
}

// buildHierarchy builds the zones in spec order, signing children first so that a signed child's
// DS can be added to its parent before the parent is signed.
func buildHierarchy(spec Spec, now time.Time) ([]*zone, error) {
	order := make([]int, len(spec.Zones))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		return dns.CountLabel(dns.CanonicalName(spec.Zones[b].Origin)) - dns.CountLabel(dns.CanonicalName(spec.Zones[a].Origin))
	})
	zones := make([]*zone, len(spec.Zones))
	for _, i := range order {
		origin := dns.CanonicalName(spec.Zones[i].Origin)
		var extra []dns.RR
		for _, child := range zones {
			if child == nil || child.ksk == nil || parentOf(spec, child.origin) != origin {
				continue
			}
			ds := child.ksk.ToDS(dns.SHA256)
			extra = append(extra, ds)
		}
		z, err := buildZone(spec.Zones[i], extra, now)
		if err != nil {
			return nil, err
		}
		for _, ds := range extra {
			if !z.cuts[ds.Header().Name] {
				return nil, fmt.Errorf("zone %s: signed child %s has no delegation NS", origin, ds.Header().Name)
			}
		}
		zones[i] = z
	}
	return zones, nil
}

// parentOf returns the origin of the deepest zone in spec that is a proper ancestor of origin.
func parentOf(spec Spec, origin string) string {
	best, bestLabels := "", -1
	for _, zs := range spec.Zones {
		o := dns.CanonicalName(zs.Origin)
		if o != origin && dns.IsSubDomain(o, origin) && dns.CountLabel(o) > bestLabels {
			best, bestLabels = o, dns.CountLabel(o)
		}
	}
	return best
}

func (h *Hierarchy) serve(s *dns.Server) {
	started := make(chan struct{})
	s.NotifyStartedFunc = func() { close(started) }
	h.servers = append(h.servers, s)
	go func() { _ = s.ActivateAndServe() }()
	<-started
}

// Close stops every listener.
func (h *Hierarchy) Close() {
	for _, s := range h.servers {
		_ = s.Shutdown()
	}
	if h.stats != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.stats.Shutdown(ctx)
	}
}

// Stats returns a copy of the counters.
func (h *Hierarchy) Stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	q := make(map[string]int, len(h.queries))
	for k, v := range h.queries {
		q[k] = v
	}
	return Stats{Queries: q, SpoofsSent: h.spoofsSent}
}

type answerFunc func(name string, qtype uint16, do bool) *dns.Msg

// handler counts the query, builds the reply with answer, truncates oversized UDP replies and,
// for spoofing servers, sends the forgeries first. recursive replies carry RA=1 and AA=0.
func (h *Hierarchy) handler(ip string, spoof, recursive bool, answer answerFunc) dns.HandlerFunc {
	return func(w dns.ResponseWriter, req *dns.Msg) {
		h.mu.Lock()
		h.queries[ip]++
		h.mu.Unlock()
		m := new(dns.Msg)
		if len(req.Question) != 1 || req.Opcode != dns.OpcodeQuery {
			m.SetRcode(req, dns.RcodeFormatError)
			_ = w.WriteMsg(m)
			return
		}
		q := req.Question[0]
		opt := req.IsEdns0()
		do := opt != nil && opt.Do()
		body := answer(q.Name, q.Qtype, do)
		m.SetReply(req) // echoes the question with the query's exact casing
		m.Compress = true
		m.Rcode, m.Authoritative = body.Rcode, body.Authoritative
		m.Answer, m.Ns, m.Extra = body.Answer, body.Ns, body.Extra
		if recursive {
			m.Authoritative, m.RecursionAvailable = false, true
		}
		if opt != nil {
			m.SetEdns0(1232, do)
		}
		_, udp := w.LocalAddr().(*net.UDPAddr)
		if udp {
			limit := 512
			if opt != nil && opt.UDPSize() > 512 {
				limit = int(opt.UDPSize())
			}
			if m.Len() > limit {
				m.Truncated = true
				m.Answer, m.Ns, m.Extra = nil, nil, nil
				if opt != nil {
					m.SetEdns0(1232, do)
				}
			}
			if spoof {
				h.sendSpoofs(w, m, ip)
				time.Sleep(spoofDelay)
			}
		}
		_ = w.WriteMsg(m)
	}
}

// sendSpoofs writes forged replies to the querier: a wrong ID, a wrong question name and a
// lower-cased question name through the query's own socket, then a fully matching forgery from
// another source port.
func (h *Hierarchy) sendSpoofs(w dns.ResponseWriter, real *dns.Msg, ip string) {
	forge := func(edit func(*dns.Msg)) *dns.Msg {
		f := real.Copy()
		f.Truncated = false
		edit(f)
		f.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: f.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300}, A: spoofAddr}}
		return f
	}
	sent := 0
	for _, f := range []*dns.Msg{
		forge(func(f *dns.Msg) { f.Id ^= 0xFFFF }),
		forge(func(f *dns.Msg) { f.Question[0].Name = "evil.spoof.test." }),
		forge(func(f *dns.Msg) { f.Question[0].Name = strings.ToLower(f.Question[0].Name) }),
	} {
		if w.WriteMsg(f) == nil {
			sent++
		}
	}
	if other, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(ip)}); err == nil {
		if b, err := forge(func(*dns.Msg) {}).Pack(); err == nil {
			if _, err := other.WriteTo(b, w.RemoteAddr()); err == nil {
				sent++
			}
		}
		_ = other.Close()
	}
	h.mu.Lock()
	h.spoofsSent += sent
	h.mu.Unlock()
}

// recurse answers like a recursive resolver over the hierarchy: from the deepest zone containing
// the name (the parent of that zone for DS at its apex), following CNAMEs across zones.
func (h *Hierarchy) recurse(name string, qtype uint16, do bool) *dns.Msg {
	m := h.deepest(name, qtype).respond(name, qtype, do)
	for range cnameChain {
		if m.Rcode != dns.RcodeSuccess || qtype == dns.TypeCNAME {
			break
		}
		cname, ok := lastData(m.Answer).(*dns.CNAME)
		if !ok {
			break
		}
		next := h.deepest(cname.Target, qtype).respond(cname.Target, qtype, do)
		m.Answer = append(m.Answer, next.Answer...)
		m.Ns, m.Rcode = next.Ns, next.Rcode
	}
	m.Authoritative = false
	return m
}

func lastData(rrs []dns.RR) dns.RR {
	for i := len(rrs) - 1; i >= 0; i-- {
		if rrs[i].Header().Rrtype != dns.TypeRRSIG {
			return rrs[i]
		}
	}
	return nil
}

func (h *Hierarchy) deepest(name string, qtype uint16) *zone {
	name = dns.CanonicalName(name)
	var best *zone
	for _, z := range h.zones {
		if !dns.IsSubDomain(z.origin, name) || (qtype == dns.TypeDS && z.origin == name && name != ".") {
			continue
		}
		if best == nil || dns.CountLabel(z.origin) > dns.CountLabel(best.origin) {
			best = z
		}
	}
	if best == nil {
		// No zone contains the name (a hierarchy without a root): the first zone refuses it.
		return h.zones[0]
	}
	return best
}

// respond answers name/qtype as this zone's authoritative server would: REFUSED outside the
// zone, a referral below a delegation, otherwise an authoritative answer, NODATA or NXDOMAIN
// with DNSSEC records when do is set and the zone is signed.
// debt: no wildcard synthesis; the fixture zones have no wildcards. Revisit when a test needs one.
func (z *zone) respond(name string, qtype uint16, do bool) *dns.Msg {
	m := new(dns.Msg)
	name = dns.CanonicalName(name)
	if !dns.IsSubDomain(z.origin, name) {
		m.Rcode = dns.RcodeRefused
		return m
	}
	secure := do && z.spec.Signed
	if cut := z.cutAbove(name); cut != "" && (cut != name || qtype != dns.TypeDS) {
		z.referral(m, cut, secure)
		return m
	}
	m.Authoritative = true
	z.authoritative(m, name, qtype, secure, 0)
	return m
}

func (z *zone) referral(m *dns.Msg, cut string, secure bool) {
	ns := z.sets[rrKey{cut, dns.TypeNS}]
	m.Ns = append(m.Ns, ns...)
	if secure {
		if ds := z.sets[rrKey{cut, dns.TypeDS}]; len(ds) > 0 {
			m.Ns = append(append(m.Ns, ds...), z.sigs[rrKey{cut, dns.TypeDS}]...)
		} else {
			z.addDenial(m, z.match(cut))
		}
	}
	for _, rr := range ns {
		target := dns.CanonicalName(rr.(*dns.NS).Ns)
		m.Extra = append(m.Extra, z.sets[rrKey{target, dns.TypeA}]...)
		m.Extra = append(m.Extra, z.sets[rrKey{target, dns.TypeAAAA}]...)
	}
}

func (z *zone) authoritative(m *dns.Msg, name string, qtype uint16, secure bool, depth int) {
	withSigs := func(k rrKey) {
		m.Answer = append(m.Answer, z.sets[k]...)
		if secure {
			m.Answer = append(m.Answer, z.sigs[k]...)
		}
	}
	if k := (rrKey{name, qtype}); len(z.sets[k]) > 0 {
		withSigs(k)
		return
	}
	if k := (rrKey{name, dns.TypeCNAME}); qtype != dns.TypeCNAME && len(z.sets[k]) > 0 {
		withSigs(k)
		target := dns.CanonicalName(z.sets[k][0].(*dns.CNAME).Target)
		if depth < cnameChain && dns.IsSubDomain(z.origin, target) && z.cutAbove(target) == "" {
			z.authoritative(m, target, qtype, secure, depth+1)
		}
		return
	}
	soa := dns.Copy(z.soa)
	soa.Header().Ttl = z.negativeTTL()
	m.Ns = append(m.Ns, soa)
	if secure {
		m.Ns = append(m.Ns, z.sigs[rrKey{z.origin, dns.TypeSOA}]...)
	}
	if z.exists[name] {
		if secure {
			if z.spec.NSEC3Iterations >= 0 {
				z.addDenial(m, z.match(name))
			} else if rr := z.match(name); rr != nil {
				z.addDenial(m, rr)
			} else {
				z.addDenial(m, z.cover(name)) // empty non-terminal
			}
		}
		return
	}
	m.Rcode = dns.RcodeNameError
	if !secure {
		return
	}
	ce := name
	for !z.exists[ce] {
		ce = parentName(ce)
	}
	wildcard := "*." + ce
	if ce == "." {
		wildcard = "*."
	}
	if z.spec.NSEC3Iterations >= 0 {
		labels := dns.SplitDomainName(name)
		nextCloser := dns.Fqdn(strings.Join(labels[len(labels)-dns.CountLabel(ce)-1:], "."))
		z.addDenial(m, z.match(ce), z.cover(nextCloser), z.cover(wildcard))
		return
	}
	z.addDenial(m, z.cover(name), z.cover(wildcard))
}

// match returns the denial record for name itself (NSEC owner or NSEC3 hash), or nil.
func (z *zone) match(name string) dns.RR {
	want := name
	if z.spec.NSEC3Iterations >= 0 {
		want = z.hash(name) + "." + z.origin
	}
	for _, rr := range z.denial {
		if strings.EqualFold(rr.Header().Name, want) {
			return rr
		}
	}
	return nil
}

// cover returns the denial record whose interval contains name: the last record ordered before
// it, wrapping to the last record of the chain.
func (z *zone) cover(name string) dns.RR {
	var found dns.RR
	for _, rr := range z.denial {
		var before bool
		if n3, ok := rr.(*dns.NSEC3); ok {
			before = strings.SplitN(n3.Hdr.Name, ".", 2)[0] < z.hash(name)
		} else {
			before = canonicalLess(rr.Header().Name, name)
		}
		if before {
			found = rr
		}
	}
	if found == nil && len(z.denial) > 0 {
		found = z.denial[len(z.denial)-1]
	}
	return found
}

// addDenial appends each record (and its signature) to the authority section once.
func (z *zone) addDenial(m *dns.Msg, rrs ...dns.RR) {
	for _, rr := range rrs {
		if rr == nil || slices.ContainsFunc(m.Ns, func(x dns.RR) bool { return dns.IsDuplicate(x, rr) }) {
			continue
		}
		h := rr.Header()
		m.Ns = append(m.Ns, rr)
		m.Ns = append(m.Ns, z.sigs[rrKey{dns.CanonicalName(h.Name), h.Rrtype}]...)
	}
}
