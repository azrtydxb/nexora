package harness

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cloudflare/circl/hpke"
	"github.com/miekg/dns"
)

// RFC 9230 Oblivious DoH client: config parsing, query encryption and response decryption.

const (
	odohContentType = "application/oblivious-dns-message"
	odohVersion     = 0x0001
	odohQueryType   = 0x01
	odohReplyType   = 0x02
	odohKeyLen      = 16 // Nk of AES-128-GCM
	odohNonceLen    = 12 // Nn of AES-128-GCM
)

// ODoHConfig is one ObliviousDoHConfigContents of version 0x0001 (RFC 9230 §6).
type ODoHConfig struct {
	KemID, KdfID, AeadID uint16
	PublicKey            []byte
}

// FetchODoHConfigs GETs baseURL/.well-known/odohconfigs. On a non-200 status it returns the
// response and no configs.
func FetchODoHConfigs(ctx context.Context, hc *http.Client, baseURL string) ([]ODoHConfig, *http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+"/.well-known/odohconfigs", nil)
	if err != nil {
		return nil, nil, err
	}
	resp, body, err := odohDo(hc, req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, resp, err
	}
	cfgs, err := ParseODoHConfigs(body)
	return cfgs, resp, err
}

// ParseODoHConfigs parses an ObliviousDoHConfigs body, skipping configs of unknown versions.
func ParseODoHConfigs(b []byte) ([]ODoHConfig, error) {
	list, rest, ok := odohVector(b)
	if !ok || len(rest) != 0 {
		return nil, errors.New("odoh: malformed config list")
	}
	var cfgs []ODoHConfig
	for len(list) > 0 {
		if len(list) < 2 {
			return nil, errors.New("odoh: truncated config")
		}
		version := binary.BigEndian.Uint16(list)
		contents, next, ok := odohVector(list[2:])
		if !ok {
			return nil, errors.New("odoh: truncated config")
		}
		list = next
		if version != odohVersion {
			continue
		}
		if len(contents) < 6 {
			return nil, errors.New("odoh: truncated config contents")
		}
		pub, tail, ok := odohVector(contents[6:])
		if !ok || len(tail) != 0 || len(pub) == 0 {
			return nil, errors.New("odoh: malformed config contents")
		}
		cfgs = append(cfgs, ODoHConfig{
			KemID:     binary.BigEndian.Uint16(contents),
			KdfID:     binary.BigEndian.Uint16(contents[2:]),
			AeadID:    binary.BigEndian.Uint16(contents[4:]),
			PublicKey: bytes.Clone(pub),
		})
	}
	return cfgs, nil
}

// KeyID is Expand(Extract("", config), "odoh key id", Nh) over the serialised contents (RFC 9230 §6).
func (c ODoHConfig) KeyID() []byte {
	contents := binary.BigEndian.AppendUint16(nil, c.KemID)
	contents = binary.BigEndian.AppendUint16(contents, c.KdfID)
	contents = binary.BigEndian.AppendUint16(contents, c.AeadID)
	contents = odohAppendVector(contents, c.PublicKey)
	return hkdfKeyID(contents)
}

func hkdfKeyID(contents []byte) []byte {
	prk, err := hkdf.Extract(sha256.New, contents, nil)
	if err != nil {
		panic(err) // unreachable: SHA-256 extract has no failure mode
	}
	id, err := hkdf.Expand(sha256.New, prk, "odoh key id", sha256.Size)
	if err != nil {
		panic(err) // unreachable: 32 octets is within the HKDF output limit
	}
	return id
}

// ODoHQuery encrypts q for c, POSTs it to url (a target, or a proxy URL with targethost/targetpath),
// and decrypts a 200 response. On a non-200 status it returns the response and a nil message.
func ODoHQuery(ctx context.Context, hc *http.Client, url string, c ODoHConfig, q *dns.Msg) (*dns.Msg, *http.Response, error) {
	if c.KemID != uint16(hpke.KEM_X25519_HKDF_SHA256) || c.KdfID != uint16(hpke.KDF_HKDF_SHA256) || c.AeadID != uint16(hpke.AEAD_AES128GCM) {
		return nil, nil, fmt.Errorf("odoh: unsupported suite %04x/%04x/%04x", c.KemID, c.KdfID, c.AeadID)
	}
	q = q.Copy()
	q.Id = 0
	wire, err := q.Pack()
	if err != nil {
		return nil, nil, err
	}
	pk, err := hpke.KEM_X25519_HKDF_SHA256.Scheme().UnmarshalBinaryPublicKey(c.PublicKey)
	if err != nil {
		return nil, nil, fmt.Errorf("odoh: public key: %w", err)
	}
	sender, err := hpke.NewSuite(hpke.KEM_X25519_HKDF_SHA256, hpke.KDF_HKDF_SHA256, hpke.AEAD_AES128GCM).NewSender(pk, []byte("odoh query"))
	if err != nil {
		return nil, nil, err
	}
	enc, sealer, err := sender.Setup(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	plain := odohAppendVector(nil, wire)
	plain = odohAppendVector(plain, nil) // no padding
	keyID := c.KeyID()
	aad := odohAppendVector([]byte{odohQueryType}, keyID)
	ct, err := sealer.Seal(plain, aad)
	if err != nil {
		return nil, nil, err
	}
	msg := odohAppendVector(aad, append(enc, ct...))
	resp, err := ODoHRaw(ctx, hc, url, odohContentType, msg)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, resp, err
	}
	if ct := resp.Header.Get("Content-Type"); ct != odohContentType {
		return nil, resp, fmt.Errorf("odoh: response content type %q", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp, err
	}
	m, err := odohOpenResponse(sealer.Export([]byte("odoh response"), odohKeyLen), plain, body)
	return m, resp, err
}

// odohOpenResponse decrypts an ObliviousDoHMessage of type 0x02 (RFC 9230 §6.4, §7).
func odohOpenResponse(secret, queryPlain, body []byte) (*dns.Msg, error) {
	if len(body) < 1 || body[0] != odohReplyType {
		return nil, errors.New("odoh: not a response message")
	}
	nonce, rest, ok := odohVector(body[1:])
	if !ok {
		return nil, errors.New("odoh: truncated response nonce")
	}
	ct, tail, ok := odohVector(rest)
	if !ok || len(tail) != 0 {
		return nil, errors.New("odoh: malformed response")
	}
	salt := odohAppendVector(bytes.Clone(queryPlain), nonce)
	prk, err := hkdf.Extract(sha256.New, secret, salt)
	if err != nil {
		return nil, err
	}
	key, err := hkdf.Expand(sha256.New, prk, "odoh key", odohKeyLen)
	if err != nil {
		return nil, err
	}
	aeadNonce, err := hkdf.Expand(sha256.New, prk, "odoh nonce", odohNonceLen)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, aeadNonce, ct, odohAppendVector([]byte{odohReplyType}, nonce))
	if err != nil {
		return nil, fmt.Errorf("odoh: response decryption: %w", err)
	}
	wire, rest, ok := odohVector(plain)
	if !ok || len(wire) == 0 {
		return nil, errors.New("odoh: malformed response plaintext")
	}
	padding, tail, ok := odohVector(rest)
	if !ok || len(tail) != 0 {
		return nil, errors.New("odoh: malformed response padding")
	}
	for _, p := range padding {
		if p != 0 {
			return nil, errors.New("odoh: non-zero response padding")
		}
	}
	m := new(dns.Msg)
	if err := m.Unpack(wire); err != nil {
		return nil, err
	}
	return m, nil
}

// ODoHRaw POSTs body with contentType and returns the response (for 400/401/415 checks). The body
// is read in full, so callers may read resp.Body without closing a connection.
func ODoHRaw(ctx context.Context, hc *http.Client, url, contentType string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", odohContentType)
	resp, _, err := odohDo(hc, req)
	return resp, err
}

// odohDo sends req, reads the body in full and leaves a re-readable copy on the response.
func odohDo(hc *http.Client, req *http.Request) (*http.Response, []byte, error) {
	resp, err := hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, body, err
}

// odohVector splits a u16-length-prefixed vector off b.
func odohVector(b []byte) (vec, rest []byte, ok bool) {
	if len(b) < 2 {
		return nil, nil, false
	}
	n := int(binary.BigEndian.Uint16(b))
	if len(b)-2 < n {
		return nil, nil, false
	}
	return b[2 : 2+n], b[2+n:], true
}

func odohAppendVector(dst, v []byte) []byte {
	return append(binary.BigEndian.AppendUint16(dst, uint16(len(v))), v...)
}
