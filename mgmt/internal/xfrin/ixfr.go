// Package xfrin keeps secondary zones in step with their external primaries: SOA checks, AXFR and
// IXFR pulls, SOA refresh/retry/expire timers and NOTIFY-triggered refreshes.
package xfrin

import (
	"errors"
	"fmt"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

// Diff is one IXFR difference sequence (RFC 1995 §4) from FromSerial to ToSerial.
type Diff struct {
	FromSerial, ToSerial uint32
	Deleted, Added       []dns.RR
}

// Answer is an interpreted transfer: up to date, a full zone (SOA first) or incremental diffs.
type Answer struct {
	UpToDate bool
	Full     []dns.RR
	Diffs    []Diff
	Serial   uint32
}

// Interpret validates the answer RRs of an AXFR or IXFR response stream requested at serial
// requested and returns what they mean.
func Interpret(rrs []dns.RR, requested uint32, ixfr bool) (*Answer, error) {
	if len(rrs) == 0 {
		return nil, errors.New("empty transfer")
	}
	first, ok := rrs[0].(*dns.SOA)
	if !ok {
		return nil, errors.New("transfer does not start with SOA")
	}
	serial := first.Serial
	if len(rrs) == 1 {
		if ixfr && !zone.SerialLess(requested, serial) {
			return &Answer{UpToDate: true, Serial: serial}, nil
		}
		return nil, errors.New("transfer truncated after first SOA")
	}
	last, ok := rrs[len(rrs)-1].(*dns.SOA)
	if !ok || last.Serial != serial {
		return nil, errors.New("transfer does not end with the starting SOA")
	}
	if _, second := rrs[1].(*dns.SOA); !ixfr || !second {
		body := rrs[1 : len(rrs)-1]
		for _, r := range body {
			if r.Header().Rrtype == dns.TypeSOA {
				return nil, errors.New("SOA inside AXFR body")
			}
		}
		return &Answer{Full: append([]dns.RR{first}, body...), Serial: serial}, nil
	}
	var diffs []Diff
	i := 1
	expect := requested
	for i < len(rrs)-1 {
		from, ok := rrs[i].(*dns.SOA)
		if !ok || from.Serial != expect {
			return nil, fmt.Errorf("IXFR sequence does not continue from serial %d", expect)
		}
		d := Diff{FromSerial: from.Serial}
		i++
		for ; i < len(rrs)-1 && rrs[i].Header().Rrtype != dns.TypeSOA; i++ {
			d.Deleted = append(d.Deleted, rrs[i])
		}
		to, ok := rrs[i].(*dns.SOA)
		if !ok || i == len(rrs)-1 {
			return nil, errors.New("IXFR sequence without new SOA")
		}
		d.ToSerial = to.Serial
		i++
		for ; i < len(rrs)-1 && rrs[i].Header().Rrtype != dns.TypeSOA; i++ {
			d.Added = append(d.Added, rrs[i])
		}
		diffs = append(diffs, d)
		expect = d.ToSerial
	}
	if expect != serial {
		return nil, fmt.Errorf("IXFR ends at %d, SOA says %d", expect, serial)
	}
	return &Answer{Diffs: diffs, Serial: serial}, nil
}
