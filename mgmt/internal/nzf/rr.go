package nzf

import (
	"encoding/binary"
	"fmt"

	"github.com/miekg/dns"
)

// FromRR packs a miekg RR (uncompressed) into a Record; the owner keeps its case.
func FromRR(rr dns.RR) (Record, error) {
	buf := make([]byte, 65535+255+10)
	n, err := dns.PackRR(rr, buf, 0, nil, false)
	if err != nil {
		return Record{}, fmt.Errorf("pack %s: %w", rr.Header().Name, err)
	}
	var tmp [256]byte
	ol, err := dns.PackDomainName(rr.Header().Name, tmp[:], 0, nil, false)
	if err != nil {
		return Record{}, fmt.Errorf("pack owner %s: %w", rr.Header().Name, err)
	}
	if n < ol+10 {
		return Record{}, fmt.Errorf("pack %s: short record", rr.Header().Name)
	}
	b := buf[:n]
	rdlen := int(binary.BigEndian.Uint16(b[ol+8:]))
	if ol+10+rdlen != n {
		return Record{}, fmt.Errorf("pack %s: rdlength mismatch", rr.Header().Name)
	}
	return Record{
		Owner: append([]byte(nil), b[:ol]...),
		Type:  binary.BigEndian.Uint16(b[ol:]),
		Class: binary.BigEndian.Uint16(b[ol+2:]),
		TTL:   binary.BigEndian.Uint32(b[ol+4:]),
		RData: append([]byte(nil), b[ol+10:]...),
	}, nil
}

// ToRR re-assembles the wire RR and unpacks it with miekg.
func ToRR(r Record) (dns.RR, error) {
	if len(r.RData) > 65535 {
		return nil, fmt.Errorf("rdata of %d octets", len(r.RData))
	}
	b := make([]byte, 0, len(r.Owner)+10+len(r.RData))
	b = append(b, r.Owner...)
	b = binary.BigEndian.AppendUint16(b, r.Type)
	b = binary.BigEndian.AppendUint16(b, r.Class)
	b = binary.BigEndian.AppendUint32(b, r.TTL)
	b = binary.BigEndian.AppendUint16(b, uint16(len(r.RData)))
	b = append(b, r.RData...)
	rr, _, err := dns.UnpackRR(b, 0)
	return rr, err
}
