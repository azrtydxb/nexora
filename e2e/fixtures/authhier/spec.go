// Package authhier serves a private, optionally DNSSEC-signed DNS hierarchy (root, TLD and leaf
// zones) on loopback addresses sharing one port, for recursion and validation tests that must not
// touch the internet.
package authhier

import "fmt"

// ZoneSpec describes one authoritative zone and the address its server listens on.
type ZoneSpec struct {
	Origin          string `json:"origin"`
	ServerIP        string `json:"server_ip"`
	Signed          bool   `json:"signed"`
	NSEC3Iterations int    `json:"nsec3_iterations"` // -1: NSEC; >= 0: NSEC3 with this iteration count
	BreakSignatures bool   `json:"break_signatures"` // flips the last byte of every RRSIG over A/AAAA/TXT
	Spoof           bool   `json:"spoof"`
	// OmitWildcardProof serves synthesised wildcard answers without the next-closer denial.
	OmitWildcardProof bool     `json:"omit_wildcard_proof"`
	Records           []string `json:"records"` // presentation format, owner names absolute
}

// Spec is a whole hierarchy.
type Spec struct {
	Port        int        `json:"port"` // 0: bind the first zone's address on a kernel-chosen port, then every other address on it
	ForwarderIP string     `json:"forwarder_ip"`
	Zones       []ZoneSpec `json:"zones"`
}

// Ready is what a started hierarchy reports to its users.
type Ready struct {
	Port      int        `json:"port"`
	RootDS    string     `json:"root_ds"` // "<key tag> 13 2 <HEX digest>" of the root KSK; empty when the root is unsigned
	RootHints []RootHint `json:"root_hints"`
	Forwarder string     `json:"forwarder"` // ForwarderIP:Port
	StatsURL  string     `json:"stats_url"`
}

// RootHint is one root server name with its addresses.
type RootHint struct {
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
}

// Stats counts queries per server address and spoofed replies sent.
type Stats struct {
	Queries    map[string]int `json:"queries"`
	SpoofsSent int            `json:"spoofs_sent"`
}

const (
	defaultForwarderIP = "127.0.53.100"
	nsec               = -1
)

// DefaultSpec is the hierarchy the M3 acceptance tests use, every server on 127.0.53.0/24:
//
//	.              127.0.53.1  NSEC signed; delegates test.
//	test.          127.0.53.2  NSEC signed; delegates good, bad, n3, wild (signed, with DS), plain, glueless, spoof, poison, lame
//	good.test.     127.0.53.3  NSEC signed; www A, alias CNAME, big TXT (> 1232 octets), *.w A and TXT
//	bad.test.      127.0.53.4  NSEC signed with broken signatures
//	n3.test.       127.0.53.8  NSEC3 signed, 0 iterations; *.w A
//	wild.test.     127.0.53.12 NSEC signed; *.w A, answered without the next-closer proof
//	plain.test.    127.0.53.5  unsigned; also serves ns2.plain.test., the glueless name server
//	glueless.test. 127.0.53.6  unsigned; delegated to ns2.plain.test. without glue
//	spoof.test.    127.0.53.7  unsigned; sends forged replies before the real one
//	poison.test.   127.0.53.9  unsigned; refers sub.poison.test. with out-of-bailiwick glue
//	lame.test.     127.0.53.11 unsigned; its other name servers are unreachable (127.0.53.10, nothing
//	                           listens) and lame (ns.plain.test. answers REFUSED)
//
// and a forwarder endpoint on 127.0.53.100 answering recursively from the hierarchy.
func DefaultSpec(port int) Spec {
	big := make([]string, 0, 40)
	for i := range 40 {
		// 40 distinct 60-octet strings: about 2.9 KB of answer, above the 1232-octet EDNS size.
		big = append(big, fmt.Sprintf("big.good.test. 300 IN TXT \"%02d%058d\"", i, i))
	}
	return Spec{
		Port:        port,
		ForwarderIP: defaultForwarderIP,
		Zones: []ZoneSpec{
			{Origin: ".", ServerIP: "127.0.53.1", Signed: true, NSEC3Iterations: nsec, Records: []string{
				". 300 IN SOA rootns.test. hostmaster.test. 1 3600 600 86400 300",
				". 300 IN NS rootns.test.",
				"rootns.test. 300 IN A 127.0.53.1",
				"test. 300 IN NS ns.test.",
				"ns.test. 300 IN A 127.0.53.2",
			}},
			{Origin: "test.", ServerIP: "127.0.53.2", Signed: true, NSEC3Iterations: nsec, Records: []string{
				"test. 300 IN SOA ns.test. hostmaster.test. 1 3600 600 86400 300",
				"test. 300 IN NS ns.test.",
				"ns.test. 300 IN A 127.0.53.2",
				"rootns.test. 300 IN A 127.0.53.1",
				"good.test. 300 IN NS ns.good.test.",
				"ns.good.test. 300 IN A 127.0.53.3",
				"bad.test. 300 IN NS ns.bad.test.",
				"ns.bad.test. 300 IN A 127.0.53.4",
				"n3.test. 300 IN NS ns.n3.test.",
				"ns.n3.test. 300 IN A 127.0.53.8",
				"plain.test. 300 IN NS ns.plain.test.",
				"ns.plain.test. 300 IN A 127.0.53.5",
				"glueless.test. 300 IN NS ns2.plain.test.",
				"spoof.test. 300 IN NS ns.spoof.test.",
				"ns.spoof.test. 300 IN A 127.0.53.7",
				"poison.test. 300 IN NS ns.poison.test.",
				"ns.poison.test. 300 IN A 127.0.53.9",
				"lame.test. 300 IN NS ns.lame.test.",
				"lame.test. 300 IN NS dead.lame.test.",
				"lame.test. 300 IN NS ns.plain.test.",
				"ns.lame.test. 300 IN A 127.0.53.11",
				"dead.lame.test. 300 IN A 127.0.53.10",
				"wild.test. 300 IN NS ns.wild.test.",
				"ns.wild.test. 300 IN A 127.0.53.12",
			}},
			{Origin: "good.test.", ServerIP: "127.0.53.3", Signed: true, NSEC3Iterations: nsec, Records: append([]string{
				"good.test. 300 IN SOA ns.good.test. hostmaster.good.test. 1 3600 600 86400 300",
				"good.test. 300 IN NS ns.good.test.",
				"ns.good.test. 300 IN A 127.0.53.3",
				"www.good.test. 300 IN A 192.0.2.10",
				"alias.good.test. 300 IN CNAME www.good.test.",
				"*.w.good.test. 300 IN A 192.0.2.60",
				"*.w.good.test. 300 IN TXT \"wild\"",
			}, big...)},
			{Origin: "bad.test.", ServerIP: "127.0.53.4", Signed: true, NSEC3Iterations: nsec, BreakSignatures: true, Records: []string{
				"bad.test. 300 IN SOA ns.bad.test. hostmaster.bad.test. 1 3600 600 86400 300",
				"bad.test. 300 IN NS ns.bad.test.",
				"ns.bad.test. 300 IN A 127.0.53.4",
				"www.bad.test. 300 IN A 192.0.2.11",
			}},
			{Origin: "n3.test.", ServerIP: "127.0.53.8", Signed: true, NSEC3Iterations: 0, Records: []string{
				"n3.test. 300 IN SOA ns.n3.test. hostmaster.n3.test. 1 3600 600 86400 300",
				"n3.test. 300 IN NS ns.n3.test.",
				"ns.n3.test. 300 IN A 127.0.53.8",
				"www.n3.test. 300 IN A 192.0.2.40",
				"*.w.n3.test. 300 IN A 192.0.2.61",
			}},
			{Origin: "wild.test.", ServerIP: "127.0.53.12", Signed: true, NSEC3Iterations: nsec, OmitWildcardProof: true, Records: []string{
				"wild.test. 300 IN SOA ns.wild.test. hostmaster.wild.test. 1 3600 600 86400 300",
				"wild.test. 300 IN NS ns.wild.test.",
				"ns.wild.test. 300 IN A 127.0.53.12",
				"*.w.wild.test. 300 IN A 192.0.2.62",
			}},
			{Origin: "plain.test.", ServerIP: "127.0.53.5", NSEC3Iterations: nsec, Records: []string{
				"plain.test. 300 IN SOA ns.plain.test. hostmaster.plain.test. 1 3600 600 86400 300",
				"plain.test. 300 IN NS ns.plain.test.",
				"ns.plain.test. 300 IN A 127.0.53.5",
				"www.plain.test. 300 IN A 192.0.2.12",
				"mail.plain.test. 300 IN A 192.0.2.13",
				"ip.plain.test. 300 IN A 192.0.2.66",
				"pass.plain.test. 300 IN A 192.0.2.14",
				"later.plain.test. 300 IN A 192.0.2.15",
				"ns2.plain.test. 300 IN A 127.0.53.6",
			}},
			{Origin: "glueless.test.", ServerIP: "127.0.53.6", NSEC3Iterations: nsec, Records: []string{
				"glueless.test. 300 IN SOA ns2.plain.test. hostmaster.glueless.test. 1 3600 600 86400 300",
				"glueless.test. 300 IN NS ns2.plain.test.",
				"www.glueless.test. 300 IN A 192.0.2.20",
			}},
			{Origin: "spoof.test.", ServerIP: "127.0.53.7", NSEC3Iterations: nsec, Spoof: true, Records: []string{
				"spoof.test. 300 IN SOA ns.spoof.test. hostmaster.spoof.test. 1 3600 600 86400 300",
				"spoof.test. 300 IN NS ns.spoof.test.",
				"ns.spoof.test. 300 IN A 127.0.53.7",
				"www.spoof.test. 300 IN A 192.0.2.77",
			}},
			{Origin: "poison.test.", ServerIP: "127.0.53.9", NSEC3Iterations: nsec, Records: []string{
				"poison.test. 300 IN SOA ns.poison.test. hostmaster.poison.test. 1 3600 600 86400 300",
				"poison.test. 300 IN NS ns.poison.test.",
				"ns.poison.test. 300 IN A 127.0.53.9",
				"www.poison.test. 300 IN A 192.0.2.30",
				"sub.poison.test. 300 IN NS ns.good.test.",
				// Out-of-bailiwick glue a resolver must not accept: the real ns.good.test. is 127.0.53.3.
				"ns.good.test. 300 IN A 127.0.53.66",
			}},
			{Origin: "lame.test.", ServerIP: "127.0.53.11", NSEC3Iterations: nsec, Records: []string{
				"lame.test. 300 IN SOA ns.lame.test. hostmaster.lame.test. 1 3600 600 86400 300",
				"lame.test. 300 IN NS ns.lame.test.",
				"lame.test. 300 IN NS dead.lame.test.",
				"lame.test. 300 IN NS ns.plain.test.",
				"ns.lame.test. 300 IN A 127.0.53.11",
				"dead.lame.test. 300 IN A 127.0.53.10",
				"www.lame.test. 300 IN A 192.0.2.50",
			}},
		},
	}
}
