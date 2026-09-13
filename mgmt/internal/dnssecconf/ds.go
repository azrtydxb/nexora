// Package dnssecconf validates operator input for resolution and DNSSEC settings: DS trust
// anchors, domain names, forward-zone servers and root hints.
package dnssecconf

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/miekg/dns"
)

// RootHint is one root server override.
type RootHint struct {
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
}

// IANARootAnchors are the root KSK-2017 and KSK-2024 DS records seeded by migration 00301.
var IANARootAnchors = []string{
	"20326 8 2 E06D44B80B8F1D39A95C0B0D7C65D08458E880409BBC683457104237C7F8EC8D",
	"38696 8 2 683D2D0ACB8C9B712A1948B27F741219298D0A450D612C483AF444A4C0FB2B16",
}

var digestHexLen = map[uint64]int{1: 40, 2: 64, 4: 96}

// ValidateDS checks "<key tag> <algorithm> <digest type> <hex digest>".
func ValidateDS(ds string) error {
	f := strings.Fields(ds)
	if len(f) != 4 {
		return errors.New("ds must be \"<key tag> <algorithm> <digest type> <digest>\"")
	}
	if _, err := strconv.ParseUint(f[0], 10, 16); err != nil {
		return fmt.Errorf("key tag %q is not 0..65535", f[0])
	}
	if _, err := strconv.ParseUint(f[1], 10, 8); err != nil {
		return fmt.Errorf("algorithm %q is not 0..255", f[1])
	}
	dt, err := strconv.ParseUint(f[2], 10, 8)
	if err != nil {
		return fmt.Errorf("digest type %q is not 0..255", f[2])
	}
	if _, err := hex.DecodeString(f[3]); err != nil {
		return errors.New("digest is not hex")
	}
	if want, ok := digestHexLen[dt]; !ok || len(f[3]) != want {
		return fmt.Errorf("digest length %d does not match digest type %d", len(f[3]), dt)
	}
	return nil
}

// ValidateDomain returns name as a lowercase FQDN.
func ValidateDomain(name string) (string, error) {
	if _, ok := dns.IsDomainName(name); !ok || name == "" {
		return "", fmt.Errorf("%q is not a valid domain name", name)
	}
	return dns.CanonicalName(name), nil
}

// ValidateForwardAddresses requires every address to be ip:port.
func ValidateForwardAddresses(addrs []string) error {
	for i, a := range addrs {
		if !IsIPPort(a) {
			return fmt.Errorf("addresses[%d]: not ip:port: %s", i, a)
		}
	}
	return nil
}

// IsIPPort reports whether a is an IP literal with a non-zero port.
func IsIPPort(a string) bool {
	host, port, err := net.SplitHostPort(a)
	if err != nil {
		return false
	}
	if p, err := strconv.ParseUint(port, 10, 16); err != nil || p == 0 {
		return false
	}
	_, err = netip.ParseAddr(host)
	return err == nil
}

// ValidateRootHints requires FQDN names and bare IP addresses.
func ValidateRootHints(h []RootHint) error {
	for i, hint := range h {
		if _, ok := dns.IsDomainName(hint.Name); !ok || !dns.IsFqdn(hint.Name) {
			return fmt.Errorf("root_hints[%d].name: not a fully qualified domain name: %s", i, hint.Name)
		}
		if len(hint.Addresses) == 0 {
			return fmt.Errorf("root_hints[%d].addresses: at least one address is required", i)
		}
		for j, a := range hint.Addresses {
			if _, err := netip.ParseAddr(a); err != nil {
				return fmt.Errorf("root_hints[%d].addresses[%d]: not an IP address: %s", i, j, a)
			}
		}
	}
	return nil
}
