package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go"
)

// Exercise the same exchange used by original probes, against independently
// observed server sockets. Neither response family contains a source port.
func TestOriginalProbeSocketsAllTransports(t *testing.T) {
	trust := filepath.Join(t.TempDir(), "tls")
	if err := labTLS(trust); err != nil {
		t.Fatal(err)
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(trust, "cert.pem"), filepath.Join(trust, "key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	pem, err := os.ReadFile(filepath.Join(trust, "cert.pem"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		t.Fatal("invalid fixture CA")
	}
	clientTLS := &tls.Config{RootCAs: roots, ServerName: "dsr-lab.test", MinVersion: tls.VersionTLS13}
	serverTLS := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	for _, transport := range []string{"udp", "tcp", "dot", "doh", "doq"} {
		t.Run(transport, func(t *testing.T) {
			type observation struct {
				local, remote, question string
				qtype                   uint16
			}
			seen := make(chan observation, 4)
			failures := make(chan error, 4)
			reply := func(q *dns.Msg, local, remote net.Addr) *dns.Msg {
				seen <- observation{local.String(), remote.String(), q.Question[0].Name, q.Question[0].Qtype}
				if q.Question[0].Qtype == dns.TypeTXT {
					r, e := signedAnswer(q, cert.PrivateKey.(*ecdsa.PrivateKey))
					if e != nil {
						failures <- e
						return new(dns.Msg).SetRcode(q, dns.RcodeServerFailure)
					}
					if transport == "udp" {
						r.Truncate(1232)
					}
					return r
				}
				r := new(dns.Msg).SetReply(q)
				r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.ParseIP("203.0.1.10")}}
				return r
			}
			var address string
			switch transport {
			case "udp", "tcp", "dot":
				server := &dns.Server{Handler: dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) {
					if e := w.WriteMsg(reply(q, w.LocalAddr(), w.RemoteAddr())); e != nil {
						failures <- e
					}
				})}
				if transport == "udp" {
					pc, e := net.ListenPacket("udp4", "127.0.0.1:0")
					if e != nil {
						t.Fatal(e)
					}
					server.PacketConn, server.Net, address = pc, "udp", pc.LocalAddr().String()
				} else {
					l, e := net.Listen("tcp4", "127.0.0.1:0")
					if e != nil {
						t.Fatal(e)
					}
					address = l.Addr().String()
					if transport == "dot" {
						l = tls.NewListener(l, serverTLS)
					}
					server.Listener, server.Net = l, "tcp"
				}
				ready := make(chan struct{})
				server.NotifyStartedFunc = func() { close(ready) }
				go func() {
					if e := server.ActivateAndServe(); e != nil {
						failures <- e
					}
				}()
				<-ready
				t.Cleanup(func() {
					if e := server.Shutdown(); e != nil {
						t.Error(e)
					}
				})
			case "doh":
				listener, e := net.Listen("tcp4", "127.0.0.1:0")
				if e != nil {
					t.Fatal(e)
				}
				server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.ProtoMajor != 2 {
						failures <- fmt.Errorf("not HTTP/2: %s", r.Proto)
					}
					raw, e := io.ReadAll(r.Body)
					if e != nil {
						failures <- e
						return
					}
					q := new(dns.Msg)
					if e = q.Unpack(raw); e != nil {
						failures <- e
						return
					}
					peer, e := net.ResolveTCPAddr("tcp4", r.RemoteAddr)
					if e != nil {
						failures <- e
						return
					}
					raw, e = reply(q, r.Context().Value(http.LocalAddrContextKey).(net.Addr), peer).Pack()
					if e != nil {
						failures <- e
						return
					}
					w.Header().Set("Content-Type", "application/dns-message")
					if _, e = w.Write(raw); e != nil {
						failures <- e
					}
				})}}
				server.TLS, server.EnableHTTP2 = serverTLS.Clone(), true
				server.StartTLS()
				t.Cleanup(server.Close)
				address = server.Listener.Addr().String()
			case "doq":
				tc := serverTLS.Clone()
				tc.NextProtos = []string{"doq"}
				l, e := quic.ListenAddr("127.0.0.1:0", tc, &quic.Config{})
				if e != nil {
					t.Fatal(e)
				}
				address = l.Addr().String()
				t.Cleanup(func() { _ = l.Close() })
				go func() {
					// Exactly two original connections: policy and signed payload.
					for i := 0; i < 2; i++ {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						c, e := l.Accept(ctx)
						if e != nil {
							cancel()
							failures <- e
							return
						}
						st, e := c.AcceptStream(ctx)
						if e == nil {
							e = st.SetDeadline(time.Now().Add(5 * time.Second))
						}
						var raw []byte
						if e == nil {
							raw, e = framedRead(st)
						}
						q := new(dns.Msg)
						if e == nil {
							e = q.Unpack(raw)
						}
						if e == nil {
							raw, e = reply(q, c.LocalAddr(), c.RemoteAddr()).Pack()
						}
						if e == nil {
							e = framedWrite(st, raw)
						}
						if e == nil {
							e = st.Close()
						}
						if e != nil {
							failures <- e
						}
						select {
						case <-c.Context().Done():
						case <-ctx.Done():
						}
						_ = c.CloseWithError(0, "")
						cancel()
					}
				}()
			}
			for _, large := range []bool{false, true} {
				q := new(dns.Msg)
				q.SetQuestion(transport+".r0.g1.dsr-lab.test.", dns.TypeA)
				if large {
					q.SetQuestion(transport+".r0.g1.mtu.test.", dns.TypeTXT)
					q.SetEdns0(1232, true)
				}
				q.Id = 0
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				r, tuple, e := exchangeProbe(ctx, q, transport, address, "127.0.0.1", clientTLS)
				cancel()
				if e != nil {
					t.Fatal(e)
				}
				if r == nil || len(r.Question) != 1 || r.Question[0] != q.Question[0] {
					t.Fatal("question changed")
				}
				if large {
					if e = checkSignedReply(r, transport, pem); e != nil {
						t.Fatal(e)
					}
				}
				select {
				case got := <-seen:
					if got.remote != net.JoinHostPort(tuple.LocalIP, fmt.Sprint(tuple.LocalPort)) || got.local != net.JoinHostPort(tuple.RemoteIP, fmt.Sprint(tuple.RemotePort)) || got.question != q.Question[0].Name || got.qtype != q.Question[0].Qtype {
						t.Fatalf("socket/question mismatch: server=%+v client=%+v", got, tuple)
					}
				case <-time.After(time.Second):
					t.Fatal("missing server observation")
				}
			}
			select {
			case extra := <-seen:
				t.Fatalf("query retried: %+v", extra)
			default:
			}
			select {
			case e := <-failures:
				t.Fatal(e)
			default:
			}
		})
	}
}
