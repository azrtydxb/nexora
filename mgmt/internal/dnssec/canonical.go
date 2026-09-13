package dnssec

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/mgmt/internal/nzf"
)

// canonKey is the RFC 4034 §6.1 canonical sort key of an absolute name.
func canonKey(name string) ([]byte, error) {
	var buf [256]byte
	n, err := dns.PackDomainName(name, buf[:], 0, nil, false)
	if err != nil {
		return nil, fmt.Errorf("name %q: %w", name, err)
	}
	return nzf.CanonicalKey(buf[:n]), nil
}

// sortCanonical orders lowercase absolute names canonically.
func sortCanonical(names []string) error {
	keys := make(map[string][]byte, len(names))
	for _, n := range names {
		k, err := canonKey(n)
		if err != nil {
			return err
		}
		keys[n] = k
	}
	slices.SortFunc(names, func(a, b string) int { return bytes.Compare(keys[a], keys[b]) })
	return nil
}

// packCanonical packs rr uncompressed with a lowercase owner.
func packCanonical(rr dns.RR) ([]byte, error) {
	c := dns.Copy(rr)
	c.Header().Name = strings.ToLower(c.Header().Name)
	buf := make([]byte, dns.MaxMsgSize+256)
	n, err := dns.PackRR(c, buf, 0, nil, false)
	if err != nil {
		return nil, fmt.Errorf("pack %s/%s: %w", rr.Header().Name, dns.TypeToString[rr.Header().Rrtype], err)
	}
	return buf[:n:n], nil
}

// rrsetDigest is SHA-256 over the RRset's records packed canonically (TTL included), sorted
// bytewise and length-prefixed: equal digests mean an existing signature still covers the set.
func rrsetDigest(set []dns.RR) ([32]byte, error) {
	wires := make([][]byte, 0, len(set))
	for _, rr := range set {
		w, err := packCanonical(rr)
		if err != nil {
			return [32]byte{}, err
		}
		wires = append(wires, w)
	}
	slices.SortFunc(wires, bytes.Compare)
	h := sha256.New()
	var l [4]byte
	for _, w := range wires {
		binary.BigEndian.PutUint32(l[:], uint32(len(w)))
		h.Write(l[:])
		h.Write(w)
	}
	var d [32]byte
	h.Sum(d[:0])
	return d, nil
}

// parent returns the name one label up ("" for the root).
func parent(name string) string {
	if name == "." {
		return ""
	}
	i, end := dns.NextLabel(name, 0)
	if end {
		return "."
	}
	return name[i:]
}

// cutAnalysis classifies the owners of a zone: cuts are non-apex owners with NS, and a name is
// occluded when it lies strictly below a cut (glue and anything else under a delegation).
type cutAnalysis struct {
	origin string
	cuts   map[string]bool
}

func (c cutAnalysis) occluded(name string) bool {
	for p := parent(name); len(p) > len(c.origin); p = parent(p) {
		if c.cuts[p] {
			return true
		}
	}
	return false
}
