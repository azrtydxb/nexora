package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// The lab-only TLS public key also pins this fixture's DNSKEY. This checks signed
// payload integrity, not DNSSEC chain validation by the engine.
func signingKey(pub *ecdsa.PublicKey) *dns.DNSKEY {
	raw := append(pub.X.FillBytes(make([]byte, 32)), pub.Y.FillBytes(make([]byte, 32))...)
	return &dns.DNSKEY{Hdr: dns.RR_Header{Name: "mtu.test.", Rrtype: dns.TypeDNSKEY, Class: dns.ClassINET, Ttl: 60}, Flags: 257, Protocol: 3, Algorithm: dns.ECDSAP256SHA256, PublicKey: base64.StdEncoding.EncodeToString(raw)}
}
func signedAnswer(q *dns.Msg, key *ecdsa.PrivateKey) (*dns.Msg, error) {
	r := new(dns.Msg)
	r.SetReply(q)
	if len(q.Question) != 1 || q.Question[0].Qtype != dns.TypeTXT || !strings.HasSuffix(q.Question[0].Name, ".mtu.test.") {
		r.Rcode = dns.RcodeRefused
		return r, nil
	}
	txt := &dns.TXT{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 60}, Txt: make([]string, 12)}
	for i := range txt.Txt {
		txt.Txt[i] = strings.Repeat(fmt.Sprintf("%02d", i), 100)
	}
	dk := signingKey(&key.PublicKey)
	sig := &dns.RRSIG{Hdr: dns.RR_Header{Name: txt.Hdr.Name, Rrtype: dns.TypeRRSIG, Class: dns.ClassINET, Ttl: 60}, Algorithm: dk.Algorithm, KeyTag: dk.KeyTag(), SignerName: dk.Hdr.Name, Inception: uint32(time.Now().Add(-time.Minute).Unix()), Expiration: uint32(time.Now().Add(time.Hour).Unix())}
	if err := sig.Sign(key, []dns.RR{txt}); err != nil {
		return nil, err
	}
	r.Answer = []dns.RR{txt, sig}
	return r, nil
}
func serveSigned(tlsDir string) error {
	pair, err := tls.LoadX509KeyPair(filepath.Join(tlsDir, "cert.pem"), filepath.Join(tlsDir, "key.pem"))
	if err != nil {
		return err
	}
	key, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return fmt.Errorf("lab-tls ECDSA P256 key required")
	}
	l, err := net.Listen("tcp", "198.19.1.2:5353")
	if err != nil {
		return err
	}
	s := &dns.Server{Listener: l, Net: "tcp", Handler: dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) { r, err := signedAnswer(q, key); must(err); must(w.WriteMsg(r)) })}
	go func() { must(s.ActivateAndServe()) }()
	return nil
}
func checkSignedReply(r *dns.Msg, transport string, certPEM []byte) error {
	if r == nil || !r.Response || r.Rcode != dns.RcodeSuccess {
		return fmt.Errorf("invalid signed response")
	}
	if transport == "udp" {
		if !r.Truncated || r.Len() > 1232 {
			return fmt.Errorf("UDP must truncate at advertised 1232 bytes, got TC=%v size=%d", r.Truncated, r.Len())
		}
		return nil // No fallback: stream transports are separate original requests.
	}
	if r.Truncated || r.Len() < 2400 || len(r.Answer) != 2 {
		return fmt.Errorf("missing large signed payload")
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return fmt.Errorf("missing lab certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return fmt.Errorf("lab ECDSA P256 public key required")
	}
	txt, ok := r.Answer[0].(*dns.TXT)
	if !ok || len(r.Question) != 1 || txt.Hdr.Name != r.Question[0].Name {
		return fmt.Errorf("missing or wrong-name TXT")
	}
	sig, ok := r.Answer[1].(*dns.RRSIG)
	if !ok {
		return fmt.Errorf("missing RRSIG")
	}
	if !sig.ValidityPeriod(time.Now()) {
		return fmt.Errorf("expired lab signature")
	}
	return sig.Verify(signingKey(pub), []dns.RR{txt})
}
