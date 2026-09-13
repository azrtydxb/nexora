// Package nzf encodes and decodes NZF1, the binary zone image and delta format the management
// plane publishes to engines as zstd blobs (layout in .procoder/plans/nexora-v1-m4.md, Task 1).
package nzf

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// Record is one resource record with uncompressed wire owner and RDATA.
type Record struct {
	Owner []byte
	Type  uint16
	Class uint16
	TTL   uint32
	RData []byte
}

// Image is a complete zone at one serial; it contains the apex SOA.
type Image struct {
	Origin  []byte
	Serial  uint32
	Records []Record
}

// Delta turns the zone at FromSerial into the zone at ToSerial. Deleted[0] and Added[0] are the
// apex SOA records at FromSerial and ToSerial.
type Delta struct {
	Origin               []byte
	FromSerial, ToSerial uint32
	Deleted, Added       []Record
}

const (
	KindFull  byte = 1
	KindDelta byte = 2
)

const (
	typeSOA    = 6
	classIN    = 1
	headerTail = 16 // serial, from_serial, count_a, count_b
	minRecord  = 11 // owner_len + 1-octet owner + type + class + ttl + rdlen
)

var magic = []byte("NZF1")

// ErrMalformed wraps every decoding failure.
var ErrMalformed = errors.New("nzf: malformed")

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMalformed, fmt.Sprintf(format, args...))
}

// nameEnd validates the uncompressed wire name starting at b[0] and returns its length.
func nameEnd(b []byte) (int, error) {
	i := 0
	for {
		if i >= len(b) {
			return 0, malformed("name runs past its buffer")
		}
		l := int(b[i])
		if l == 0 {
			if i+1 > 255 {
				return 0, malformed("name longer than 255 octets")
			}
			return i + 1, nil
		}
		if l >= 0x40 {
			return 0, malformed("label length octet 0x%02x", l)
		}
		i += l + 1
		if i > 255 {
			return 0, malformed("name longer than 255 octets")
		}
	}
}

func validName(b []byte) error {
	n, err := nameEnd(b)
	if err != nil {
		return err
	}
	if n != len(b) {
		return malformed("name length %d, declared %d", n, len(b))
	}
	return nil
}

// within reports whether the valid wire name owner equals origin or lies below it at a label
// boundary (case-insensitive).
func within(owner, origin []byte) bool {
	if len(origin) > len(owner) {
		return false
	}
	start := len(owner) - len(origin)
	if !bytes.EqualFold(owner[start:], origin) {
		return false
	}
	for i := 0; i < len(owner); i += int(owner[i]) + 1 {
		if i == start {
			return true
		}
		if owner[i] == 0 {
			break
		}
	}
	return false
}

// soaSerial returns the serial of SOA RDATA (two uncompressed names then 20 octets).
func soaSerial(rdata []byte) (uint32, error) {
	m, err := nameEnd(rdata)
	if err != nil {
		return 0, err
	}
	r, err := nameEnd(rdata[m:])
	if err != nil {
		return 0, err
	}
	if len(rdata) != m+r+20 {
		return 0, malformed("SOA rdata length %d", len(rdata))
	}
	return binary.BigEndian.Uint32(rdata[m+r:]), nil
}

func checkSOA(r Record, origin []byte, serial uint32) error {
	if r.Type != typeSOA || !bytes.EqualFold(r.Owner, origin) {
		return malformed("expected the apex SOA")
	}
	s, err := soaSerial(r.RData)
	if err != nil {
		return err
	}
	if s != serial {
		return malformed("SOA serial %d, header serial %d", s, serial)
	}
	return nil
}

func checkRecord(r Record, origin []byte) error {
	if err := validName(r.Owner); err != nil {
		return err
	}
	if !within(r.Owner, origin) {
		return malformed("owner outside the zone origin")
	}
	if r.Class != classIN {
		return malformed("class %d", r.Class)
	}
	if len(r.RData) > 65535 {
		return malformed("rdata of %d octets", len(r.RData))
	}
	return nil
}

func checkOrigin(origin []byte) error {
	if err := validName(origin); err != nil {
		return err
	}
	if len(origin) < 2 {
		return malformed("root origin")
	}
	return nil
}

// checkFullSOA requires exactly one SOA, at the origin, carrying the image serial.
func checkFullSOA(rs []Record, origin []byte, serial uint32) error {
	found := false
	for _, r := range rs {
		if r.Type != typeSOA {
			continue
		}
		if found {
			return malformed("more than one SOA")
		}
		if err := checkSOA(r, origin, serial); err != nil {
			return err
		}
		found = true
	}
	if !found {
		return malformed("no SOA")
	}
	return nil
}

func lower(name []byte) []byte {
	out := append([]byte(nil), name...)
	for i := 0; i < len(out); i += int(out[i]) + 1 {
		for j := i + 1; j <= i+int(out[i]); j++ {
			if out[j] >= 'A' && out[j] <= 'Z' {
				out[j] += 'a' - 'A'
			}
		}
	}
	return out
}

func appendHeader(b []byte, kind byte, origin []byte, serial, from uint32, a, c int) []byte {
	b = append(b, magic...)
	b = append(b, kind, 0, byte(len(origin)))
	b = append(b, lower(origin)...)
	b = binary.BigEndian.AppendUint32(b, serial)
	b = binary.BigEndian.AppendUint32(b, from)
	b = binary.BigEndian.AppendUint32(b, uint32(a))
	return binary.BigEndian.AppendUint32(b, uint32(c))
}

func appendRecords(b []byte, rs []Record) []byte {
	for _, r := range rs {
		b = append(b, byte(len(r.Owner)))
		b = append(b, r.Owner...)
		b = binary.BigEndian.AppendUint16(b, r.Type)
		b = binary.BigEndian.AppendUint16(b, r.Class)
		b = binary.BigEndian.AppendUint32(b, r.TTL)
		b = binary.BigEndian.AppendUint16(b, uint16(len(r.RData)))
		b = append(b, r.RData...)
	}
	return b
}

// EncodeFull serialises a full image with its records in canonical order.
func EncodeFull(img Image) ([]byte, error) {
	if err := checkOrigin(img.Origin); err != nil {
		return nil, err
	}
	for _, r := range img.Records {
		if err := checkRecord(r, img.Origin); err != nil {
			return nil, err
		}
	}
	if err := checkFullSOA(img.Records, img.Origin, img.Serial); err != nil {
		return nil, err
	}
	rs := append([]Record(nil), img.Records...)
	SortRecords(rs)
	b := appendHeader(nil, KindFull, img.Origin, img.Serial, 0, len(rs), 0)
	return appendRecords(b, rs), nil
}

// EncodeDelta serialises a delta: each list keeps its SOA first and sorts the rest.
func EncodeDelta(d Delta) ([]byte, error) {
	if err := checkOrigin(d.Origin); err != nil {
		return nil, err
	}
	if len(d.Deleted) == 0 || len(d.Added) == 0 {
		return nil, malformed("delta without SOA records")
	}
	if err := checkSOA(d.Deleted[0], d.Origin, d.FromSerial); err != nil {
		return nil, err
	}
	if err := checkSOA(d.Added[0], d.Origin, d.ToSerial); err != nil {
		return nil, err
	}
	for _, list := range [][]Record{d.Deleted, d.Added} {
		for _, r := range list {
			if err := checkRecord(r, d.Origin); err != nil {
				return nil, err
			}
		}
	}
	del := append([]Record(nil), d.Deleted...)
	add := append([]Record(nil), d.Added...)
	SortRecords(del[1:])
	SortRecords(add[1:])
	b := appendHeader(nil, KindDelta, d.Origin, d.ToSerial, d.FromSerial, len(del), len(add))
	b = appendRecords(b, del)
	return appendRecords(b, add), nil
}

type reader struct {
	b   []byte
	off int
}

func (r *reader) take(n int) ([]byte, error) {
	if n < 0 || len(r.b)-r.off < n {
		return nil, malformed("truncated")
	}
	s := r.b[r.off : r.off+n : r.off+n]
	r.off += n
	return s, nil
}

func (r *reader) u8() (byte, error) {
	s, err := r.take(1)
	if err != nil {
		return 0, err
	}
	return s[0], nil
}

func (r *reader) u16() (uint16, error) {
	s, err := r.take(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(s), nil
}

func (r *reader) u32() (uint32, error) {
	s, err := r.take(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(s), nil
}

func (r *reader) record(origin []byte) (Record, error) {
	ol, err := r.u8()
	if err != nil {
		return Record{}, err
	}
	var rec Record
	if rec.Owner, err = r.take(int(ol)); err != nil {
		return Record{}, err
	}
	if rec.Type, err = r.u16(); err != nil {
		return Record{}, err
	}
	if rec.Class, err = r.u16(); err != nil {
		return Record{}, err
	}
	if rec.TTL, err = r.u32(); err != nil {
		return Record{}, err
	}
	rdlen, err := r.u16()
	if err != nil {
		return Record{}, err
	}
	if rec.RData, err = r.take(int(rdlen)); err != nil {
		return Record{}, err
	}
	return rec, checkRecord(rec, origin)
}

// Decode parses NZF1 bytes. Returned slices alias raw. Every length is bounds-checked; trailing
// bytes, compressed or out-of-zone names and inconsistent SOA records are rejected.
func Decode(raw []byte) (kind byte, img *Image, d *Delta, err error) {
	r := &reader{b: raw}
	m, err := r.take(4)
	if err != nil {
		return 0, nil, nil, err
	}
	if !bytes.Equal(m, magic) {
		return 0, nil, nil, malformed("bad magic")
	}
	if kind, err = r.u8(); err != nil {
		return 0, nil, nil, err
	}
	if kind != KindFull && kind != KindDelta {
		return 0, nil, nil, malformed("kind %d", kind)
	}
	reserved, err := r.u8()
	if err != nil {
		return 0, nil, nil, err
	}
	if reserved != 0 {
		return 0, nil, nil, malformed("reserved octet %d", reserved)
	}
	ol, err := r.u8()
	if err != nil {
		return 0, nil, nil, err
	}
	origin, err := r.take(int(ol))
	if err != nil {
		return 0, nil, nil, err
	}
	if err := checkOrigin(origin); err != nil {
		return 0, nil, nil, err
	}
	hdr, err := r.take(headerTail)
	if err != nil {
		return 0, nil, nil, err
	}
	serial := binary.BigEndian.Uint32(hdr[0:])
	from := binary.BigEndian.Uint32(hdr[4:])
	countA := binary.BigEndian.Uint32(hdr[8:])
	countB := binary.BigEndian.Uint32(hdr[12:])
	if uint64(countA)+uint64(countB) > uint64((len(raw)-r.off)/minRecord) {
		return 0, nil, nil, malformed("record counts %d+%d exceed the remaining %d octets", countA, countB, len(raw)-r.off)
	}
	if kind == KindFull && (from != 0 || countB != 0) {
		return 0, nil, nil, malformed("full image with delta header fields")
	}
	a := make([]Record, 0, countA)
	for i := uint32(0); i < countA; i++ {
		rec, err := r.record(origin)
		if err != nil {
			return 0, nil, nil, err
		}
		a = append(a, rec)
	}
	b := make([]Record, 0, countB)
	for i := uint32(0); i < countB; i++ {
		rec, err := r.record(origin)
		if err != nil {
			return 0, nil, nil, err
		}
		b = append(b, rec)
	}
	if r.off != len(raw) {
		return 0, nil, nil, malformed("%d trailing octets", len(raw)-r.off)
	}
	if kind == KindFull {
		if err := checkFullSOA(a, origin, serial); err != nil {
			return 0, nil, nil, err
		}
		return kind, &Image{Origin: origin, Serial: serial, Records: a}, nil, nil
	}
	if len(a) == 0 || len(b) == 0 {
		return 0, nil, nil, malformed("delta without SOA records")
	}
	if err := checkSOA(a[0], origin, from); err != nil {
		return 0, nil, nil, err
	}
	if err := checkSOA(b[0], origin, serial); err != nil {
		return 0, nil, nil, err
	}
	return kind, nil, &Delta{Origin: origin, FromSerial: from, ToSerial: serial, Deleted: a, Added: b}, nil
}
