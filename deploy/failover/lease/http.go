package lease

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrGone = errors.New("pinned Lease absent")

const annotation = "failover.nexora.io/"
const maxBody = 64 << 10

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// HTTPSConfig contains immutable target and private bearer credentials. Roots
// nil uses system trust. There is intentionally no insecure TLS/client hook.
type HTTPSConfig struct {
	Endpoint, Namespace, Name, Token string
	Roots                            *x509.CertPool
	Timeout                          time.Duration
}
type HTTPSAuthority struct {
	client                         *http.Client
	target, namespace, name, token string
}

func NewHTTPS(cfg HTTPSConfig) (*HTTPSAuthority, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.ForceQuery {
		return nil, errors.New("HTTPS origin required")
	}
	if len(cfg.Namespace) > 63 || !dnsLabel.MatchString(cfg.Namespace) || len(cfg.Name) > 253 || cfg.Name == "" {
		return nil, errors.New("invalid immutable Lease target")
	}
	for _, part := range strings.Split(cfg.Name, ".") {
		if len(part) > 63 || !dnsLabel.MatchString(part) {
			return nil, errors.New("invalid Lease name")
		}
	}
	if cfg.Token == "" || strings.ContainsAny(cfg.Token, "\r\n\t ") || len(cfg.Token) > 16384 || cfg.Timeout <= 0 || cfg.Timeout > time.Minute {
		return nil, errors.New("invalid credentials or timeout")
	}
	var roots *x509.CertPool
	if cfg.Roots != nil {
		roots = cfg.Roots.Clone()
	}
	tr := &http.Transport{
		Proxy: nil, DialContext: (&net.Dialer{Timeout: cfg.Timeout}).DialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		TLSHandshakeTimeout: cfg.Timeout, ResponseHeaderTimeout: cfg.Timeout,
		MaxResponseHeaderBytes: 16 << 10, DisableCompression: true,
		// No replayable request bodies or transport retries on reused connections.
		DisableKeepAlives: true,
	}
	return &HTTPSAuthority{client: &http.Client{Transport: tr, Timeout: cfg.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }},
		target:    strings.TrimSuffix(cfg.Endpoint, "/") + "/apis/coordination.k8s.io/v1/namespaces/" + cfg.Namespace + "/leases/" + cfg.Name,
		namespace: cfg.Namespace, name: cfg.Name, token: cfg.Token}, nil
}

func (a *HTTPSAuthority) request(ctx context.Context, method string, body []byte) (Record, error) {
	if len(body) > maxBody {
		return Record{}, errors.New("request too large")
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.target, reader)
	if err != nil {
		return Record{}, errors.New("request construction failed")
	}
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return Record{}, errors.New("HTTPS request failed or ambiguous")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Record{}, ErrGone
	}
	if resp.StatusCode != http.StatusOK {
		return Record{}, errors.New("unexpected Kubernetes status; operation unacknowledged")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(b) > maxBody {
		return Record{}, errors.New("invalid or oversized response")
	}
	v, err := strictJSON(b)
	if err != nil {
		return Record{}, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return Record{}, errors.New("Lease object required")
	}
	return a.parse(m)
}
func (a *HTTPSAuthority) Get(ctx context.Context) (Record, error) {
	return a.request(ctx, http.MethodGet, nil)
}
func (a *HTTPSAuthority) CAS(ctx context.Context, old, next Record) (Record, error) {
	if !valid(old) || !valid(next) || old.raw == nil || old.UID != next.UID || old.RV != next.RV || old.Epoch == ^uint64(0) || next.Epoch != old.Epoch+1 || next.Nonce == old.Nonce || !strong(next.Holder) {
		return Record{}, errors.New("invalid CAS input")
	}
	// Round-trip makes a private copy; preserve server fields and unrelated annotations.
	b, err := json.Marshal(old.raw)
	if err != nil {
		return Record{}, errors.New("invalid source document")
	}
	v, err := strictJSON(b)
	if err != nil {
		return Record{}, err
	}
	m := v.(map[string]any)
	p, err := a.parse(m)
	if err != nil || p.UID != old.UID || p.RV != old.RV || p.Holder != old.Holder || p.Nonce != old.Nonce || p.Epoch != old.Epoch {
		return Record{}, errors.New("source document mismatch")
	}
	meta := m["metadata"].(map[string]any)
	meta["uid"] = old.UID
	meta["resourceVersion"] = old.RV
	anns := meta["annotations"].(map[string]any)
	anns[annotation+"nonce"] = next.Nonce
	anns[annotation+"epoch"] = strconv.FormatUint(next.Epoch, 10)
	m["spec"].(map[string]any)["holderIdentity"] = next.Holder
	b, err = json.Marshal(m)
	if err != nil {
		return Record{}, errors.New("CAS encoding failed")
	}
	out, err := a.request(ctx, http.MethodPut, b)
	if err != nil {
		return Record{}, err
	}
	if out.UID != old.UID || out.RV == old.RV || out.Holder != next.Holder || out.Nonce != next.Nonce || out.Epoch != next.Epoch {
		return Record{}, errors.New("CAS response mismatch")
	}
	return out, nil
}
func stringAt(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func (a *HTTPSAuthority) parse(m map[string]any) (Record, error) {
	bad := errors.New("malformed or unsupported Lease")
	if stringAt(m, "apiVersion") != "coordination.k8s.io/v1" || stringAt(m, "kind") != "Lease" {
		return Record{}, bad
	}
	meta, ok := m["metadata"].(map[string]any)
	if !ok {
		return Record{}, bad
	}
	if stringAt(meta, "namespace") != a.namespace || stringAt(meta, "name") != a.name || meta["deletionTimestamp"] != nil {
		return Record{}, bad
	}
	if gen, exists := meta["generation"]; exists {
		n, ok := gen.(json.Number)
		if !ok {
			return Record{}, bad
		}
		i, e := strconv.ParseInt(string(n), 10, 64)
		if e != nil || i < 0 {
			return Record{}, bad
		}
	}
	anns, ok := meta["annotations"].(map[string]any)
	if !ok {
		return Record{}, bad
	}
	spec, ok := m["spec"].(map[string]any)
	if !ok {
		return Record{}, bad
	}
	if h, exists := spec["holderIdentity"]; exists {
		if _, ok := h.(string); !ok {
			return Record{}, bad
		}
	}
	e := stringAt(anns, annotation+"epoch")
	epoch, err := strconv.ParseUint(e, 10, 64)
	if err != nil || strconv.FormatUint(epoch, 10) != e {
		return Record{}, bad
	}
	r := Record{UID: stringAt(meta, "uid"), RV: stringAt(meta, "resourceVersion"), Holder: stringAt(spec, "holderIdentity"), Protocol: stringAt(anns, annotation+"protocol"), Nonce: stringAt(anns, annotation+"nonce"), Epoch: epoch, raw: m}
	if !valid(r) {
		return Record{}, bad
	}
	return r, nil
}

// Reject duplicate keys, trailing values, excessive nesting and non-JSON numbers.
func strictJSON(b []byte) (any, error) {
	if !utf8.Valid(b) {
		return nil, errors.New("invalid UTF-8 JSON")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 32 {
			return nil, errors.New("JSON nesting limit")
		}
		t, err := d.Token()
		if err != nil {
			return nil, errors.New("invalid JSON")
		}
		if delim, ok := t.(json.Delim); ok {
			switch delim {
			case '{':
				m := map[string]any{}
				for d.More() {
					k, e := d.Token()
					if e != nil {
						return nil, e
					}
					key, ok := k.(string)
					if !ok {
						return nil, errors.New("invalid key")
					}
					if _, exists := m[key]; exists {
						return nil, errors.New("duplicate key")
					}
					v, e := read(depth + 1)
					if e != nil {
						return nil, e
					}
					m[key] = v
				}
				end, e := d.Token()
				if e != nil || end != json.Delim('}') {
					return nil, errors.New("invalid object")
				}
				return m, nil
			case '[':
				a := []any{}
				for d.More() {
					v, e := read(depth + 1)
					if e != nil {
						return nil, e
					}
					a = append(a, v)
				}
				end, e := d.Token()
				if e != nil || end != json.Delim(']') {
					return nil, errors.New("invalid array")
				}
				return a, nil
			default:
				return nil, errors.New("invalid delimiter")
			}
		}
		return t, nil
	}
	v, e := read(0)
	if e != nil {
		return nil, errors.New("invalid JSON document")
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	return v, nil
}
