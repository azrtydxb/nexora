// Isolated forwarding experiment; not a production DNS server.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"time"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go"
)

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
func answer(q *dns.Msg, remote string) *dns.Msg {
	r := new(dns.Msg)
	r.SetReply(q)
	if len(q.Question) != 1 {
		r.Rcode = dns.RcodeFormatError
		return r
	}
	ip, _, err := net.SplitHostPort(remote)
	if err != nil {
		r.Rcode = dns.RcodeServerFailure
		return r
	}
	r.Answer = []dns.RR{&dns.TXT{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 0}, Txt: []string{ip}}}
	log.Printf("peer=%s question=%s", remote, q.Question[0].Name)
	return r
}
func framedRead(r io.Reader) ([]byte, error) {
	var n uint16
	if err := binary.Read(r, binary.BigEndian, &n); err != nil {
		return nil, err
	}
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	return b, err
}
func framedWrite(w io.Writer, b []byte) error {
	if len(b) == 0 || len(b) > 65535 {
		return fmt.Errorf("invalid DNS frame length %d", len(b))
	}
	buf := make([]byte, 2+len(b))
	binary.BigEndian.PutUint16(buf, uint16(len(b)))
	copy(buf[2:], b)
	n, err := w.Write(buf)
	if err == nil && n != len(buf) {
		return io.ErrShortWrite
	}
	return err
}
func serve() {
	cert, err := tls.LoadX509KeyPair(filepath.Join(os.Getenv("FAILOVER_LAB_DIR"), "cert.pem"), filepath.Join(os.Getenv("FAILOVER_LAB_DIR"), "key.pem"))
	must(err)
	tc := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	handler := dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) { must(w.WriteMsg(answer(q, w.RemoteAddr().String()))) })
	for _, network := range []string{"udp", "tcp", "tcp-tls"} {
		addr := ":53"
		if network == "tcp-tls" {
			addr = ":853"
		}
		s := &dns.Server{Addr: addr, Net: network, TLSConfig: tc, Handler: handler}
		go func() { must(s.ListenAndServe()) }()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/dns-query", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(io.LimitReader(r.Body, 65536))
		if err != nil {
			http.Error(w, "read", 400)
			return
		}
		q := new(dns.Msg)
		if q.Unpack(b) != nil {
			http.Error(w, "dns", 400)
			return
		}
		b, err = answer(q, r.RemoteAddr).Pack()
		if err != nil {
			http.Error(w, "pack", 500)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(b)
	})
	hs := &http.Server{Addr: ":443", Handler: mux, TLSConfig: tc.Clone(), ReadHeaderTimeout: 5 * time.Second}
	go func() { must(hs.ListenAndServeTLS("", "")) }()
	qt := tc.Clone()
	qt.NextProtos = []string{"doq"}
	l, err := quic.ListenAddr(":853", qt, &quic.Config{MaxIdleTimeout: 5 * time.Second})
	must(err)
	for {
		c, err := l.Accept(context.Background())
		must(err)
		go func() {
			defer c.CloseWithError(0, "")
			st, err := c.AcceptStream(context.Background())
			if err != nil {
				log.Print(err)
				return
			}
			_ = st.SetDeadline(time.Now().Add(5 * time.Second))
			b, err := framedRead(st)
			if err != nil {
				log.Print(err)
				return
			}
			q := new(dns.Msg)
			if err = q.Unpack(b); err != nil {
				log.Print(err)
				return
			}
			b, err = answer(q, c.RemoteAddr().String()).Pack()
			if err != nil {
				log.Print(err)
				return
			}
			if err = framedWrite(st, b); err != nil {
				log.Print(err)
				return
			}
			_ = st.Close()
			// Wait for the client to consume the stream before closing the connection.
			<-c.Context().Done()
		}()
	}
}

// exchangeProbe sends one original question and records the socket that carried it.
func exchangeProbe(ctx context.Context, q *dns.Msg, transport, address, expected string, tc *tls.Config) (*dns.Msg, socketTuple, error) {
	var r *dns.Msg
	var tuple socketTuple
	var err error
	switch transport {
	case "udp", "tcp", "dot":
		network := transport
		if transport == "dot" {
			network = "tcp-tls"
		}
		dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(expected)}}
		if transport == "udp" {
			dialer.LocalAddr = &net.UDPAddr{IP: net.ParseIP(expected)}
		}
		c := &dns.Client{Net: network, TLSConfig: tc, Timeout: 5 * time.Second, Dialer: dialer}
		conn, e := c.DialContext(ctx, address)
		err = e
		if err == nil {
			tuple, err = captureSocket(conn.LocalAddr(), conn.RemoteAddr())
			if err == nil {
				r, _, err = c.ExchangeWithConnContext(ctx, q, conn)
			}
			_ = conn.Close()
		}
	case "doh":
		b, e := q.Pack()
		if e != nil {
			return nil, tuple, e
		}
		req, e := http.NewRequestWithContext(ctx, "POST", "https://"+address+"/dns-query", bytes.NewReader(b))
		if e != nil {
			return nil, tuple, e
		}
		req.Header.Set("Content-Type", "application/dns-message")
		// Do not make the DNS POST replayable. A second GotConn cancels
		// before another request can be sent, even for an HTTP/2 retry.
		req.GetBody = nil
		requestCtx, stop := context.WithCancel(ctx)
		defer stop()
		connections := 0
		var socketErr error
		trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
			connections++
			if connections != 1 || info.Reused {
				socketErr = fmt.Errorf("DoH connection retry/reuse refused")
				stop()
				return
			}
			tuple, socketErr = captureSocket(info.Conn.LocalAddr(), info.Conn.RemoteAddr())
			if socketErr != nil {
				stop()
			}
		}}
		req = req.WithContext(httptrace.WithClientTrace(requestCtx, trace))
		tr := &http.Transport{TLSClientConfig: tc, ForceAttemptHTTP2: true, DialContext: (&net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(expected)}}).DialContext}
		defer tr.CloseIdleConnections()
		client := &http.Client{Transport: tr, Timeout: 5 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
		resp, e := client.Do(req)
		err = e
		if socketErr != nil {
			err = socketErr
		}
		if resp != nil {
			defer resp.Body.Close()
		}
		if err == nil {
			if resp.StatusCode != 200 || resp.ProtoMajor != 2 || resp.Header.Get("Content-Type") != "application/dns-message" {
				return nil, tuple, fmt.Errorf("invalid DoH response: HTTP %d protocol %s", resp.StatusCode, resp.Proto)
			}
			b, err = io.ReadAll(io.LimitReader(resp.Body, 65536))
			_ = resp.Body.Close()
			if err == nil {
				r = new(dns.Msg)
				err = r.Unpack(b)
			}
			fmt.Printf("HTTP protocol=%s\n", resp.Proto)
		}
	case "doq":
		qt := tc.Clone()
		qt.NextProtos = []string{"doq"}
		socket, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(expected)})
		if e != nil {
			return nil, tuple, e
		}
		defer socket.Close()
		peer, e := net.ResolveUDPAddr("udp4", address)
		if e != nil {
			return nil, tuple, e
		}
		qtTransport := &quic.Transport{Conn: socket}
		c, e := qtTransport.Dial(ctx, peer, qt, &quic.Config{})
		err = e
		if err == nil {
			// This is the packet socket given to QUIC, not an echo or
			// a newly dialed socket. The peer comes from the connection.
			tuple, err = captureSocket(socket.LocalAddr(), c.RemoteAddr())
			if err != nil {
				_ = c.CloseWithError(0, "")
				_ = qtTransport.Close()
				return nil, tuple, err
			}
			st, e := c.OpenStreamSync(ctx)
			err = e
			if err == nil {
				_ = st.SetDeadline(time.Now().Add(5 * time.Second))
				b, e := q.Pack()
				err = e
				if err == nil {
					err = framedWrite(st, b)
				}
				if err == nil {
					err = st.Close()
				}
				if err == nil {
					b, err = framedRead(st)
				}
				if err == nil {
					r = new(dns.Msg)
					err = r.Unpack(b)
				}
			}
			_ = c.CloseWithError(0, "")
		}
		_ = qtTransport.Close()
	default:
		err = fmt.Errorf("unknown probe transport %q", transport)
	}
	return r, tuple, err
}

func probe(target, expected string, transports []string, engineName string, large bool) {
	pem, err := os.ReadFile(filepath.Join(os.Getenv("FAILOVER_LAB_DIR"), "cert.pem"))
	must(err)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		log.Fatal("CA")
	}
	tc := &tls.Config{RootCAs: roots, ServerName: "dsr-lab.test", MinVersion: tls.VersionTLS13}
	for _, transport := range transports {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		q := new(dns.Msg)
		q.SetQuestion(transport+".dsr-lab.test.", dns.TypeTXT)
		q.Id = 0
		if engineName != "" {
			q.SetQuestion(transport+"."+engineName+".dsr-lab.test.", dns.TypeA)
			q.Id = 0
			if large {
				q.SetQuestion(transport+"."+engineName+".mtu.test.", dns.TypeTXT)
				q.Id = 0
				q.SetEdns0(1232, true)
			}
		}
		port := "53"
		if transport == "dot" || transport == "doq" {
			port = "853"
		}
		if transport == "doh" {
			port = "443"
		}
		r, tuple, err := exchangeProbe(ctx, q, transport, net.JoinHostPort(target, port), expected, tc)
		cancel()
		must(err)
		if large {
			if r == nil || r.Id != q.Id || len(r.Question) != 1 || r.Question[0] != q.Question[0] {
				log.Fatal("signed question mismatch")
			}
			must(checkSignedReply(r, transport, pem))
			evidenceJSON(map[string]any{"transport": transport, "client": expected, "question": q.Question[0].Name, "question_type": dns.TypeToString[q.Question[0].Qtype], "socket": tuple, "signed": transport != "udp", "truncated": r.Truncated, "bytes": r.Len()})
			continue
		}
		if r == nil || !r.Response || r.Id != q.Id || r.Truncated || r.Rcode != dns.RcodeSuccess || len(r.Question) != 1 || r.Question[0] != q.Question[0] || len(r.Answer) != 1 {
			log.Fatalf("%s invalid reply", transport)
		}
		if engineName != "" {
			if err := checkEngineReply(r, expected); err != nil {
				log.Fatalf("%s: %v", transport, err)
			}
			evidenceJSON(map[string]any{"transport": transport, "client": expected, "question": q.Question[0].Name, "question_type": dns.TypeToString[q.Question[0].Qtype], "socket": tuple, "answer": r.Answer[0].(*dns.A).A.String()})
			continue
		}
		txt, ok := r.Answer[0].(*dns.TXT)
		if !ok || len(txt.Txt) != 1 || txt.Txt[0] != expected {
			if ok && len(txt.Txt) == 1 {
				evidenceJSON(map[string]string{"control": "source-mismatch", "transport": transport, "observed": txt.Txt[0], "expected": expected})
			}
			log.Fatalf("%s source mismatch: %v expected %s", transport, r.Answer, expected)
		}
		fmt.Printf("PASS transport=%s source=%s target=%s\n", transport, expected, target)
	}
}
func main() {
	if len(os.Args) == 3 && os.Args[1] == "engine-trust-check" {
		must(checkLabTrust(os.Args[2]))
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "lab-tls" {
		must(labTLS(os.Args[2]))
		return
	}
	if len(os.Args) == 6 && os.Args[1] == "engine-prepare" {
		must(prepareEngine(os.Args[2], os.Args[3], os.Args[4], os.Args[5]))
		return
	}
	if len(os.Args) == 4 && os.Args[1] == "engine-collect" {
		must(collectEngine(os.Args[2], os.Args[3]))
		return
	}
	if len(os.Args) == 7 && (os.Args[1] == "engine-probe" || os.Args[1] == "engine-large") {
		must(checkLabTrust(os.Getenv("FAILOVER_LAB_DIR")))
		if os.Args[2] != "198.18.0.100" || (os.Args[3] != "198.18.0.10" && os.Args[3] != "198.18.0.11") || (os.Args[5] != "g1" && os.Args[5] != "g2") || (os.Args[6] != "r0" && os.Args[6] != "r1") {
			log.Fatal("invalid engine probe contract")
		}
		switch os.Args[4] {
		case "udp", "tcp", "dot", "doh", "doq":
		default:
			log.Fatal("unknown transport")
		}
		probe(os.Args[2], os.Args[3], []string{os.Args[4]}, os.Args[6]+"."+os.Args[5], os.Args[1] == "engine-large")
		return
	}
	if os.Getenv("FAILOVER_LAB_DIR") == "" {
		log.Fatal("FAILOVER_LAB_DIR is required")
	}
	if len(os.Args) == 2 && os.Args[1] == "serve" {
		serve()
		return
	}
	if (len(os.Args) == 4 || len(os.Args) == 5) && os.Args[1] == "probe" {
		transports := []string{"udp", "tcp", "dot", "doh", "doq"}
		if len(os.Args) == 5 {
			switch os.Args[4] {
			case "udp", "tcp", "dot", "doh", "doq":
				transports = []string{os.Args[4]}
			default:
				log.Fatal("unknown transport")
			}
		}
		probe(os.Args[2], os.Args[3], transports, "", false)
		return
	}
	log.Fatal("usage: serve | probe target expected-client-ip [transport]")
}
