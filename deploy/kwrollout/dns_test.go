package kwrollout

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestCheckDNSBothTransports(t *testing.T) {
	p := dnsFixture(t, func(_ dns.ResponseWriter, _ *dns.Msg, _ *dns.Msg) {})
	var samples []DNSSample
	if err := CheckDNS(context.Background(), []DNSProbe{p}, func(s DNSSample) { samples = append(samples, s) }); err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 || samples[0].Transport != "udp" || samples[1].Transport != "tcp" || samples[0].Err != nil || samples[1].Err != nil {
		t.Fatalf("samples = %+v", samples)
	}
}

func TestCheckDNSIPv6Answer(t *testing.T) {
	p := dnsFixture(t, func(_ dns.ResponseWriter, q *dns.Msg, reply *dns.Msg) {
		reply.Answer = []dns.RR{&dns.AAAA{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET}, AAAA: net.ParseIP("2001:db8::10")}}
	})
	p.Expected = netip.MustParseAddr("2001:db8::10")
	if err := CheckDNS(context.Background(), []DNSProbe{p}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCheckDNSRejectsIncorrectAnswers(t *testing.T) {
	for name, mutate := range map[string]func(*dns.Msg){
		"servfail":  func(m *dns.Msg) { m.Rcode = dns.RcodeServerFailure },
		"nxdomain":  func(m *dns.Msg) { m.Rcode = dns.RcodeNameError },
		"empty":     func(m *dns.Msg) { m.Answer = nil },
		"truncated": func(m *dns.Msg) { m.Truncated = true },
		"wrong-ip":  func(m *dns.Msg) { m.Answer[0].(*dns.A).A = net.ParseIP("192.0.2.11") },
		"wrong-name": func(m *dns.Msg) {
			m.Answer[0].Header().Name = "other.example."
		},
		"wrong-question": func(m *dns.Msg) { m.Question[0].Name = "other.example." },
		"extra-address": func(m *dns.Msg) {
			m.Answer = append(m.Answer, &dns.A{Hdr: *m.Answer[0].Header(), A: net.ParseIP("192.0.2.11")})
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := dnsFixture(t, func(_ dns.ResponseWriter, _ *dns.Msg, reply *dns.Msg) { mutate(reply) })
			var samples []DNSSample
			err := CheckDNS(context.Background(), []DNSProbe{p}, func(s DNSSample) { samples = append(samples, s) })
			if err == nil || len(samples) != 1 || samples[0].Err == nil {
				t.Fatalf("failure was hidden: err=%v samples=%+v", err, samples)
			}
		})
	}
}

func TestCheckDNSTCPFailureIsNotHiddenByUDP(t *testing.T) {
	p := dnsFixture(t, func(w dns.ResponseWriter, _ *dns.Msg, reply *dns.Msg) {
		if w.RemoteAddr().Network() == "tcp" {
			reply.Rcode = dns.RcodeServerFailure
		}
	})
	var samples []DNSSample
	err := CheckDNS(context.Background(), []DNSProbe{p}, func(s DNSSample) { samples = append(samples, s) })
	if err == nil || len(samples) != 2 || samples[0].Err != nil || samples[1].Err == nil {
		t.Fatalf("err=%v samples=%+v", err, samples)
	}
}

func TestMonitorDNSStopsAtFirstFailure(t *testing.T) {
	var requests atomic.Int32
	p := dnsFixture(t, func(_ dns.ResponseWriter, _ *dns.Msg, reply *dns.Msg) {
		if requests.Add(1) == 3 {
			reply.Rcode = dns.RcodeServerFailure
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var samples []DNSSample
	err := MonitorDNS(ctx, []DNSProbe{p}, time.Millisecond, func(s DNSSample) { samples = append(samples, s) })
	if err == nil || errors.Is(err, context.DeadlineExceeded) || len(samples) != 3 || samples[2].Err == nil || requests.Load() != 3 {
		t.Fatalf("err=%v samples=%+v requests=%d", err, samples, requests.Load())
	}
}

func TestDNSCancellationInterruptsPendingRead(t *testing.T) {
	for _, transport := range []string{"udp", "tcp"} {
		t.Run(transport, func(t *testing.T) {
			received := make(chan struct{}, 1)
			p := dnsFixture(t, func(_ dns.ResponseWriter, _ *dns.Msg, reply *dns.Msg) {
				received <- struct{}{}
				reply.Answer = nil // fixture suppresses the reply when Response is false
				reply.Response = false
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- p.query(ctx, transport) }()
			select {
			case <-received:
			case <-time.After(time.Second):
				t.Fatal("probe did not reach fixture")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation did not interrupt DNS read")
			}
		})
	}
}

func TestDNSValidation(t *testing.T) {
	for _, probes := range [][]DNSProbe{
		nil,
		{{Address: "0.0.0.0:53", Name: "probe.example.", Expected: netip.MustParseAddr("192.0.2.10")}},
		{{Address: "224.0.0.251:5353", Name: "probe.example.", Expected: netip.MustParseAddr("192.0.2.10")}},
		{{Address: "resolver.example:53", Name: "probe.example.", Expected: netip.MustParseAddr("192.0.2.10")}},
		{{Address: "127.0.0.1:0", Name: "probe.example.", Expected: netip.MustParseAddr("192.0.2.10")}},
		{{Address: "127.0.0.1:53", Name: "", Expected: netip.MustParseAddr("192.0.2.10")}},
		{{Address: "127.0.0.1:53", Name: "probe.example."}},
	} {
		if err := CheckDNS(context.Background(), probes, func(DNSSample) { t.Fatal("invalid probe sent a query") }); err == nil {
			t.Fatalf("accepted %+v", probes)
		}
	}
}

func dnsFixture(t *testing.T, mutate func(dns.ResponseWriter, *dns.Msg, *dns.Msg)) DNSProbe {
	t.Helper()
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tcp.Close() })
	udp, err := net.ListenPacket("udp", tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = udp.Close() })
	handler := dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) {
		reply := new(dns.Msg)
		reply.SetReply(q)
		reply.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 1}, A: net.ParseIP("192.0.2.10")}}
		mutate(w, q, reply)
		if reply.Response {
			_ = w.WriteMsg(reply)
		}
	})
	for _, server := range []*dns.Server{{Listener: tcp, Handler: handler}, {PacketConn: udp, Handler: handler}} {
		ready, done := make(chan struct{}), make(chan error, 1)
		server.NotifyStartedFunc = func() { close(ready) }
		go func() { done <- server.ActivateAndServe() }()
		select {
		case <-ready:
		case err := <-done:
			t.Fatalf("start DNS fixture: %v", err)
		}
		t.Cleanup(func() {
			if err := server.Shutdown(); err != nil {
				t.Error(err)
			}
			if err := <-done; err != nil {
				t.Error(err)
			}
		})
	}
	return DNSProbe{Address: tcp.Addr().String(), Name: "probe.example.", Expected: netip.MustParseAddr("192.0.2.10")}
}
