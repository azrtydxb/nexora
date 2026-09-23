package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// mDNS (RFC 6762) test fixtures: a responder that answers from static records and a one-shot
// querier. Both open multicast sockets, so tests run them only inside the network namespace lab.

const (
	mdnsPort      = 5353
	cacheFlushBit = 0x8000 // RFC 6762 §10.2 (records) and the unicast-response bit (§5.4, questions)
	maxMDNSPacket = 9000   // RFC 6762 §17
)

var (
	mdnsGroup4 = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: mdnsPort}
	mdnsGroup6 = &net.UDPAddr{IP: net.ParseIP("ff02::fb"), Port: mdnsPort}
)

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ", ") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// reuseAddr lets several mDNS processes in one namespace bind port 5353 (RFC 6762 §15.1).
func reuseAddr(_, _ string, c syscall.RawConn) error {
	var serr error
	if err := c.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	}); err != nil {
		return err
	}
	return serr
}

// mdnsResponder answers queries on one interface from static records.
type mdnsResponder struct {
	iface   *net.Interface
	records []dns.RR
	p4      *ipv4.PacketConn
	p6      *ipv6.PacketConn

	mu   sync.Mutex
	sent map[string]bool // every response sent, to recognise a reflected copy (ECHO)
}

func runMDNSResponder(args []string) (func(), string, error) {
	fs := flag.NewFlagSet("mdns-responder", flag.ContinueOnError)
	ifName := fs.String("interface", "", "interface to join the mDNS groups on")
	var recs stringList
	fs.Var(&recs, "record", "record in zone file format (repeatable)")
	if err := fs.Parse(args); err != nil {
		return nil, "", err
	}
	if *ifName == "" || len(recs) == 0 {
		return nil, "", errors.New("--interface and at least one --record are required")
	}
	r := &mdnsResponder{sent: map[string]bool{}}
	for _, s := range recs {
		rr, err := dns.NewRR(s)
		if err != nil || rr == nil {
			return nil, "", fmt.Errorf("--record %q: %v", s, err)
		}
		r.records = append(r.records, rr)
	}
	iface, err := net.InterfaceByName(*ifName)
	if err != nil {
		return nil, "", err
	}
	r.iface = iface
	addr, err := ifaceIPv4(iface)
	if err != nil {
		return nil, "", err
	}
	lc := net.ListenConfig{Control: reuseAddr}
	c4, err := lc.ListenPacket(context.Background(), "udp4", fmt.Sprintf("0.0.0.0:%d", mdnsPort))
	if err != nil {
		return nil, "", err
	}
	c6, err := lc.ListenPacket(context.Background(), "udp6", fmt.Sprintf("[::]:%d", mdnsPort))
	if err != nil {
		_ = c4.Close()
		return nil, "", err
	}
	stop := func() { _ = c4.Close(); _ = c6.Close() }
	r.p4, r.p6 = ipv4.NewPacketConn(c4), ipv6.NewPacketConn(c6)
	if err := r.setup(); err != nil {
		stop()
		return nil, "", err
	}
	go r.serve4()
	go r.serve6()
	return stop, addr.String(), nil
}

func (r *mdnsResponder) setup() error {
	if err := r.p4.JoinGroup(r.iface, mdnsGroup4); err != nil {
		return fmt.Errorf("join %s on %s: %w", mdnsGroup4.IP, r.iface.Name, err)
	}
	if err := r.p6.JoinGroup(r.iface, mdnsGroup6); err != nil {
		return fmt.Errorf("join %s on %s: %w", mdnsGroup6.IP, r.iface.Name, err)
	}
	// Our own multicast responses must not loop back, or they would read as ECHO.
	for _, err := range []error{
		r.p4.SetControlMessage(ipv4.FlagInterface, true), r.p4.SetMulticastInterface(r.iface),
		r.p4.SetMulticastLoopback(false), r.p4.SetMulticastTTL(255),
		r.p6.SetControlMessage(ipv6.FlagInterface, true), r.p6.SetMulticastInterface(r.iface),
		r.p6.SetMulticastLoopback(false), r.p6.SetMulticastHopLimit(255),
	} {
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *mdnsResponder) serve4() {
	buf := make([]byte, maxMDNSPacket)
	for {
		n, cm, src, err := r.p4.ReadFrom(buf)
		if err != nil {
			return
		}
		if cm != nil && cm.IfIndex != r.iface.Index {
			continue
		}
		r.handle(buf[:n], src.(*net.UDPAddr), func(b []byte, to net.Addr) error {
			_, err := r.p4.WriteTo(b, nil, to)
			return err
		}, mdnsGroup4)
	}
}

func (r *mdnsResponder) serve6() {
	buf := make([]byte, maxMDNSPacket)
	for {
		n, cm, src, err := r.p6.ReadFrom(buf)
		if err != nil {
			return
		}
		if cm != nil && cm.IfIndex != r.iface.Index {
			continue
		}
		r.handle(buf[:n], src.(*net.UDPAddr), func(b []byte, to net.Addr) error {
			_, err := r.p6.WriteTo(b, &ipv6.ControlMessage{IfIndex: r.iface.Index}, to)
			return err
		}, mdnsGroup6)
	}
}

func (r *mdnsResponder) handle(pkt []byte, src *net.UDPAddr, send func([]byte, net.Addr) error, group *net.UDPAddr) {
	m := new(dns.Msg)
	if m.Unpack(pkt) != nil {
		return
	}
	if m.Response {
		r.mu.Lock()
		echo := r.sent[string(pkt)]
		r.mu.Unlock()
		if echo {
			fmt.Fprintf(os.Stderr, "ECHO %s\n", src)
		}
		return
	}
	resp := new(dns.Msg)
	resp.Response, resp.Authoritative = true, true
	for _, q := range m.Question {
		fmt.Fprintf(os.Stderr, "GOT %s %s %s\n", src, q.Name, dns.TypeToString[q.Qtype])
		for _, rr := range r.records {
			h := rr.Header()
			if strings.EqualFold(h.Name, q.Name) && (q.Qtype == h.Rrtype || q.Qtype == dns.TypeANY) {
				resp.Answer = append(resp.Answer, withCacheFlush(rr))
			}
		}
	}
	if len(resp.Answer) == 0 {
		return
	}
	to := net.Addr(group)
	if src.Port != mdnsPort {
		// Legacy unicast query (RFC 6762 §6.7): reply to the source, echoing ID and question.
		resp.Id, resp.Question, to = m.Id, m.Question, src
	}
	b, err := resp.Pack()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pack response: %v\n", err)
		return
	}
	r.mu.Lock()
	r.sent[string(b)] = true
	r.mu.Unlock()
	if err := send(b, to); err != nil {
		fmt.Fprintf(os.Stderr, "send to %s: %v\n", to, err)
	}
}

// withCacheFlush returns a copy of rr with the cache-flush bit set when its type is a unique
// record in this fixture (A, AAAA, SRV, TXT).
func withCacheFlush(rr dns.RR) dns.RR {
	c := dns.Copy(rr)
	switch c.Header().Rrtype {
	case dns.TypeA, dns.TypeAAAA, dns.TypeSRV, dns.TypeTXT:
		c.Header().Class |= cacheFlushBit
	}
	return c
}

func ifaceIPv4(iface *net.Interface) (net.IP, error) {
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
			return n.IP.To4(), nil
		}
	}
	return nil, fmt.Errorf("interface %s has no IPv4 address", iface.Name)
}

// runMDNSQuery sends one IPv4 query and prints the answers received within --wait; it returns the
// process exit code.
func runMDNSQuery(args []string) int {
	fs := flag.NewFlagSet("mdns-query", flag.ContinueOnError)
	ifName := fs.String("interface", "", "interface to send the query on")
	name := fs.String("name", "", "query name")
	qtypeName := fs.String("type", "A", "query type")
	wait := fs.Duration("wait", time.Second, "how long to collect responses")
	legacy := fs.Bool("legacy", false, "send from an ephemeral port (a legacy unicast query)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	qtype, ok := dns.StringToType[strings.ToUpper(*qtypeName)]
	if *ifName == "" || *name == "" || !ok || *wait <= 0 {
		fmt.Fprintln(os.Stderr, "mdns-query: --interface, --name, a known --type and a positive --wait are required")
		return 2
	}
	if err := mdnsQuery(*ifName, dns.Fqdn(*name), qtype, *wait, *legacy); err != nil {
		fmt.Fprintf(os.Stderr, "nexora-fixture mdns-query: %v\n", err)
		return 1
	}
	return 0
}

func mdnsQuery(ifName, name string, qtype uint16, wait time.Duration, legacy bool) error {
	iface, err := net.InterfaceByName(ifName)
	if err != nil {
		return err
	}
	var c net.PacketConn
	if legacy {
		c, err = net.ListenPacket("udp4", "0.0.0.0:0")
	} else {
		lc := net.ListenConfig{Control: reuseAddr}
		c, err = lc.ListenPacket(context.Background(), "udp4", fmt.Sprintf("0.0.0.0:%d", mdnsPort))
	}
	if err != nil {
		return err
	}
	defer c.Close()
	p := ipv4.NewPacketConn(c)
	if err := p.SetMulticastInterface(iface); err != nil {
		return err
	}
	if err := p.SetMulticastLoopback(false); err != nil {
		return err
	}
	if !legacy {
		// Multicast responses arrive on the group; only count those received on the interface.
		if err := p.JoinGroup(iface, mdnsGroup4); err != nil {
			return err
		}
		if err := p.SetControlMessage(ipv4.FlagInterface, true); err != nil {
			return err
		}
	}
	q := new(dns.Msg)
	q.SetQuestion(name, qtype)
	q.RecursionDesired = false
	if !legacy {
		q.Id = 0 // RFC 6762 §18.1
	}
	b, err := q.Pack()
	if err != nil {
		return err
	}
	if _, err := p.WriteTo(b, nil, mdnsGroup4); err != nil {
		return err
	}
	if err := p.SetReadDeadline(time.Now().Add(wait)); err != nil {
		return err
	}
	buf := make([]byte, maxMDNSPacket)
	packets := 0
	for {
		n, cm, _, err := p.ReadFrom(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				break
			}
			return err
		}
		if cm != nil && cm.IfIndex != iface.Index {
			continue
		}
		m := new(dns.Msg)
		if m.Unpack(buf[:n]) != nil || !m.Response || !answersQuery(m, name, qtype) {
			continue
		}
		packets++
		for _, rr := range m.Answer {
			rr.Header().Class &^= cacheFlushBit
			fmt.Println("ANSWER " + rr.String())
		}
	}
	fmt.Printf("PACKETS %d\n", packets)
	return nil
}

// answersQuery reports whether a response belongs to the query: a legacy unicast reply echoes the
// question, and a multicast response (which carries no question, RFC 6762 §6) holds an answer for
// the name and type.
func answersQuery(m *dns.Msg, name string, qtype uint16) bool {
	for _, q := range m.Question {
		if strings.EqualFold(q.Name, name) && q.Qtype == qtype {
			return true
		}
	}
	for _, rr := range m.Answer {
		h := rr.Header()
		if strings.EqualFold(h.Name, name) && (h.Rrtype == qtype || qtype == dns.TypeANY) {
			return true
		}
	}
	return false
}
