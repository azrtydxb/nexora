// Package blocklist fetches filter-list subscriptions, parses hosts / AdBlock / domain-list
// formats and stores them as normalised, zstd-compressed blobs.
package blocklist

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
	"golang.org/x/net/idna"
)

// MaxListBytes bounds the size of a downloaded list.
const MaxListBytes = 256 << 20

// ParseStats counts the accepted domains and the rejected lines of a list.
type ParseStats struct {
	Entries, Invalid int
}

var (
	domainRE = regexp.MustCompile(`^[a-z0-9_]{1}([a-z0-9_-]{0,61}[a-z0-9_])?(\.[a-z0-9_]{1}([a-z0-9_-]{0,61}[a-z0-9_])?)+$`)

	sinkAddrs    = map[string]bool{"0.0.0.0": true, "127.0.0.1": true, "::": true, "::1": true}
	ignoredHosts = map[string]bool{"localhost": true, "localhost.localdomain": true, "broadcasthost": true, "0.0.0.0": true}
)

// Parse reads a hosts file, AdBlock-style list or plain domain list and returns the domains in
// input order (not de-duplicated). Comments, headers and blank lines are skipped; every other
// line that yields no valid domain is counted in Invalid.
func Parse(r io.Reader) ([]string, ParseStats, error) {
	var domains []string
	var stats ParseStats
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	add := func(name string) {
		if ignoredHosts[strings.ToLower(name)] {
			return
		}
		if d, ok := normalizeDomain(name); ok {
			domains = append(domains, d)
			stats.Entries++
		} else {
			stats.Invalid++
		}
	}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == '!' || strings.HasPrefix(strings.ToLower(line), "[adblock") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "||"); ok {
			name, opts, found := strings.Cut(rest, "^")
			if !found || (opts != "" && opts[0] != '$') {
				stats.Invalid++
				continue
			}
			add(name)
			continue
		}
		// Hosts files commonly carry trailing comments: "0.0.0.0 ads.example # tracker".
		if i := strings.Index(line, " #"); i >= 0 {
			line = line[:i]
		} else if i := strings.Index(line, "\t#"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		switch {
		case len(fields) > 1 && sinkAddrs[fields[0]]:
			for _, name := range fields[1:] {
				add(name)
			}
		case len(fields) == 1:
			// Wildcard lists (OISD domainswild) prefix each entry with "*."; an entry already covers
			// every subdomain. A bare "*." stays invalid.
			name := fields[0]
			if rest, ok := strings.CutPrefix(name, "*."); ok && rest != "" {
				name = rest
			}
			add(name)
		default:
			stats.Invalid++
		}
	}
	return domains, stats, sc.Err()
}

// normalizeDomain converts name to lowercase ASCII (punycode) without a trailing dot and reports
// whether the result is a valid multi-label domain name.
func normalizeDomain(name string) (string, bool) {
	name = strings.TrimSuffix(name, ".")
	if !isASCII(name) {
		// Only non-ASCII names go through IDNA: its STD3 rules would reject the underscores
		// that real lists contain.
		ascii, err := idna.Lookup.ToASCII(name)
		if err != nil {
			return "", false
		}
		name = ascii
	}
	name = strings.ToLower(name)
	return name, len(name) <= 253 && domainRE.MatchString(name)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// Normalize returns the sorted, de-duplicated domains, one per line, each line newline-terminated.
func Normalize(domains []string) []byte {
	sorted := slices.Clone(domains)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	var b strings.Builder
	for _, d := range sorted {
		b.WriteString(d)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// Compress zstd-compresses text deterministically and returns the data and the hex SHA-256 of it.
func Compress(text []byte) (data []byte, sha256hex string, err error) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, "", err
	}
	data = enc.EncodeAll(text, nil)
	if err := enc.Close(); err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}
