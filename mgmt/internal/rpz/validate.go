// Package rpz validates and packs RPZ zone files and names the purpose of sealed RPZ TSIG secrets.
package rpz

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
	"github.com/miekg/dns"
)

// Summary describes a validated zone.
type Summary struct {
	Serial  uint32
	Records int
}

// MaxZoneBytes bounds uploaded zone text (API bodies are capped at 4 MiB).
const MaxZoneBytes = 3 << 20

var encoder, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1))

// ValidateZone parses content as a master file for origin. $INCLUDE is refused; the apex SOA is
// required and supplies Serial; Records counts the records below the apex.
func ValidateZone(origin, content string) (Summary, error) {
	if len(content) > MaxZoneBytes {
		return Summary{}, fmt.Errorf("zone file exceeds %d bytes", MaxZoneBytes)
	}
	for _, line := range strings.Split(content, "\n") {
		if t := strings.TrimSpace(line); len(t) >= 8 && strings.EqualFold(t[:8], "$INCLUDE") {
			return Summary{}, errors.New("$INCLUDE is not allowed in RPZ zones")
		}
	}
	apex := dns.CanonicalName(origin)
	zp := dns.NewZoneParser(strings.NewReader(content), apex, "")
	zp.SetIncludeAllowed(false)
	var s Summary
	soa := false
	for rr, ok := zp.Next(); ok; rr, ok = zp.Next() {
		h := rr.Header()
		if dns.CanonicalName(h.Name) == apex {
			if v, isSOA := rr.(*dns.SOA); isSOA {
				s.Serial, soa = v.Serial, true
			}
			continue
		}
		s.Records++
	}
	if err := zp.Err(); err != nil {
		return Summary{}, err // a *dns.ParseError names the line and column
	}
	if !soa {
		return Summary{}, errors.New("zone has no SOA at the apex")
	}
	return s, nil
}

// Pack zstd-compresses content deterministically.
func Pack(content string) []byte {
	return encoder.EncodeAll([]byte(content), nil)
}

// TsigPurpose is the envelope purpose of the TSIG secret of RPZ zone id.
func TsigPurpose(zoneID uuid.UUID) string {
	return "nexora/rpz-tsig/v1:" + zoneID.String()
}
