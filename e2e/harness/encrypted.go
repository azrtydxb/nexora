package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go"
)

type EncryptedClient struct {
	RootCAs    *x509.CertPool
	ServerName string
	LocalIP    net.IP
}

func (c EncryptedClient) tlsConfig(alpn ...string) *tls.Config {
	return &tls.Config{RootCAs: c.RootCAs, ServerName: c.ServerName, NextProtos: alpn, MinVersion: tls.VersionTLS12}
}

func (c EncryptedClient) dialer() *net.Dialer {
	d := &net.Dialer{Timeout: 5 * time.Second}
	if c.LocalIP != nil {
		d.LocalAddr = &net.TCPAddr{IP: c.LocalIP}
	}
	return d
}

func (c EncryptedClient) DialDoT(ctx context.Context, addr string) (*dns.Conn, error) {
	td := &tls.Dialer{NetDialer: c.dialer(), Config: c.tlsConfig("dot")}
	conn, err := td.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	return &dns.Conn{Conn: conn}, nil
}

func (c EncryptedClient) DoT(ctx context.Context, addr string, q *dns.Msg) (*dns.Msg, tls.ConnectionState, error) {
	conn, err := c.DialDoT(ctx, addr)
	if err != nil {
		return nil, tls.ConnectionState{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMsg(q); err != nil {
		return nil, tls.ConnectionState{}, err
	}
	r, err := conn.ReadMsg()
	return r, conn.Conn.(*tls.Conn).ConnectionState(), err
}

// HTTPClient returns a client limited to one connection per host, so concurrent
// requests must share one HTTP/2 connection.
func (c EncryptedClient) HTTPClient() *http.Client {
	tr := &http.Transport{
		DialContext:       c.dialer().DialContext,
		TLSClientConfig:   &tls.Config{RootCAs: c.RootCAs, ServerName: c.ServerName, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
		MaxConnsPerHost:   1,
	}
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

func DoH(ctx context.Context, hc *http.Client, url, method string, q *dns.Msg) (*dns.Msg, *http.Response, error) {
	q = q.Copy()
	q.Id = 0
	wire, err := q.Pack()
	if err != nil {
		return nil, nil, err
	}
	var req *http.Request
	switch method {
	case http.MethodGet:
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, url+"?dns="+base64.RawURLEncoding.EncodeToString(wire), nil)
	case http.MethodPost:
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(wire))
		if req != nil {
			req.Header.Set("Content-Type", "application/dns-message")
		}
	default:
		return nil, nil, fmt.Errorf("doh: unsupported method %s", method)
	}
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/dns-message")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, resp, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp, fmt.Errorf("doh: status %d", resp.StatusCode)
	}
	m := new(dns.Msg)
	return m, resp, m.Unpack(body)
}

func (c EncryptedClient) DialDoQ(ctx context.Context, addr string) (*quic.Conn, error) {
	conf := &quic.Config{MaxIdleTimeout: 30 * time.Second}
	if c.LocalIP == nil {
		return quic.DialAddr(ctx, addr, c.tlsConfig("doq"), conf)
	}
	remote, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: c.LocalIP})
	if err != nil {
		return nil, err
	}
	return quic.Dial(ctx, pc, remote, c.tlsConfig("doq"), conf)
}

func (c EncryptedClient) DoQ(ctx context.Context, addr string, q *dns.Msg) (*dns.Msg, tls.ConnectionState, error) {
	conn, err := c.DialDoQ(ctx, addr)
	if err != nil {
		return nil, tls.ConnectionState{}, err
	}
	defer conn.CloseWithError(0, "")
	m, err := DoQQuery(ctx, conn, q)
	return m, conn.ConnectionState().TLS, err
}

func DoQQuery(ctx context.Context, conn *quic.Conn, q *dns.Msg) (*dns.Msg, error) {
	q = q.Copy()
	q.Id = 0
	wire, err := q.Pack()
	if err != nil {
		return nil, err
	}
	frame := make([]byte, 2+len(wire))
	binary.BigEndian.PutUint16(frame, uint16(len(wire)))
	copy(frame[2:], wire)
	resp, err := DoQRaw(ctx, conn, frame)
	if err != nil {
		return nil, err
	}
	if len(resp) < 2 || int(binary.BigEndian.Uint16(resp)) != len(resp)-2 {
		return nil, fmt.Errorf("doq: bad framing (%d bytes)", len(resp))
	}
	m := new(dns.Msg)
	return m, m.Unpack(resp[2:])
}

// DoQRaw sends one framed message on a new stream, closes the send side, and reads to FIN.
func DoQRaw(ctx context.Context, conn *quic.Conn, framed []byte) ([]byte, error) {
	st, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := st.Write(framed); err != nil {
		return nil, err
	}
	if err := st.Close(); err != nil {
		return nil, err
	}
	_ = st.SetReadDeadline(time.Now().Add(5 * time.Second))
	return io.ReadAll(st)
}

func WriteProxyV2(w io.Writer, src, dst *net.TCPAddr) error {
	sig := []byte("\r\n\r\n\x00\r\nQUIT\n")
	var b bytes.Buffer
	b.Write(sig)
	if s4, d4 := src.IP.To4(), dst.IP.To4(); s4 != nil && d4 != nil {
		b.Write([]byte{0x21, 0x11, 0x00, 0x0c})
		b.Write(s4)
		b.Write(d4)
	} else {
		b.Write([]byte{0x21, 0x21, 0x00, 0x24})
		b.Write(src.IP.To16())
		b.Write(dst.IP.To16())
	}
	_ = binary.Write(&b, binary.BigEndian, uint16(src.Port))
	_ = binary.Write(&b, binary.BigEndian, uint16(dst.Port))
	_, err := w.Write(b.Bytes())
	return err
}

func EventuallyTrue(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	Eventually(t, timeout, func() error {
		if cond() {
			return nil
		}
		return fmt.Errorf("not yet: %s", msg)
	})
}

func Fingerprint(cs tls.ConnectionState) string {
	if len(cs.PeerCertificates) == 0 {
		return ""
	}
	sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
	return hex.EncodeToString(sum[:])
}

// AnswerSet renders answer RRs with TTL zeroed, sorted, so transports can be compared.
func AnswerSet(m *dns.Msg) []string {
	out := make([]string, 0, len(m.Answer)+1)
	out = append(out, dns.RcodeToString[m.Rcode])
	for _, rr := range m.Answer {
		c := dns.Copy(rr)
		c.Header().Ttl = 0
		out = append(out, c.String())
	}
	sort.Strings(out[1:])
	return out
}
