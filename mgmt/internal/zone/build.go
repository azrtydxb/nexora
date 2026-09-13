package zone

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/mgmt/internal/nzf"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

const (
	journalListed   = 32  // deltas advertised in the snapshot beyond the image (snapshot.AddAuthZones)
	journalKept     = 100 // rows kept in zone_journal
	imageEveryDelta = 64
	// imageMinDeltaBytes keeps small zones from writing an image on every change: the "deltas
	// total a quarter of the image" rule applies once the deltas since the image reach this size.
	imageMinDeltaBytes = 64 << 10
	// maxServedSize bounds a decompressed zone blob read back from the database.
	maxServedSize = 2 << 30
)

// Rebuild computes the served RR set of z (SOA from the zone row plus its records, signed when
// DNSSEC is on), diffs it against the current served version and, when anything changed (or
// opts force it), writes the next serial as a journal delta and, when due, a full image.
func Rebuild(ctx context.Context, tx pgx.Tx, signer Signer, z *Zone, opts RebuildOptions, now time.Time) (bool, error) {
	desired, err := desiredRRs(ctx, tx, z)
	if err != nil {
		return false, err
	}
	if z.DNSSECEnabled && signer != nil {
		if desired, err = signer.Sign(ctx, tx, z, desired, now); err != nil {
			return false, err
		}
	}
	var previous []nzf.Record
	if z.CurrentSeq > 0 {
		if previous, err = loadServed(ctx, tx, z); err != nil {
			return false, err
		}
	}
	desiredRecs, err := toRecords(desired)
	if err != nil {
		return false, err
	}
	deleted, added := diffIgnoringSOA(previous, desiredRecs)
	soaChanged := z.CurrentSeq == 0 || !soaFieldsEqual(previous, desiredRecs)
	if len(deleted) == 0 && len(added) == 0 && !soaChanged && !opts.Force && opts.Serial == nil {
		return false, nil
	}
	oldSerial := z.Serial
	newSerial := SerialNext(oldSerial)
	if z.Kind == "secondary" {
		if opts.Serial == nil {
			return false, fmt.Errorf("zone %s: a secondary rebuild needs the transferred serial", z.Name)
		}
		newSerial = *opts.Serial
	} else if opts.Serial != nil && SerialLess(oldSerial, *opts.Serial) {
		newSerial = *opts.Serial
	}
	if z.CurrentSeq == 0 && opts.Serial == nil {
		newSerial = oldSerial // the first version keeps the initial serial
	}
	desired = setSOASerial(desired, newSerial)
	if z.DNSSECEnabled && signer != nil {
		if desired, err = signer.ResignSOA(ctx, tx, z, desired, now); err != nil {
			return false, err
		}
	}
	if desiredRecs, err = toRecords(desired); err != nil {
		return false, err
	}
	origin, err := wireName(z.Name)
	if err != nil {
		return false, err
	}
	seq := z.CurrentSeq + 1
	forceImage := false
	if z.CurrentSeq > 0 {
		d := nzf.Delta{Origin: origin, FromSerial: oldSerial, ToSerial: newSerial}
		d.Deleted = append(soaWithSigs(previous), deleted...)
		if servedSerial(d.Deleted) != oldSerial {
			// The zone row's serial was changed outside Rebuild: the delta states the row's serial
			// and a new image lets engines that cannot apply it reload.
			d.Deleted[0] = withSerial(d.Deleted[0], oldSerial)
			forceImage = true
		}
		d.Added = append(soaWithSigs(desiredRecs), added...)
		raw, err := nzf.EncodeDelta(d)
		if err != nil {
			return false, fmt.Errorf("zone %s delta: %w", z.Name, err)
		}
		sha, err := putBlob(ctx, tx, raw)
		if err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO zone_journal (zone_id, seq, from_serial, to_serial, blob_sha256, raw_size) VALUES ($1,$2,$3,$4,$5,$6)`,
			z.ID, seq, int64(oldSerial), int64(newSerial), sha, len(raw)); err != nil {
			return false, err
		}
	}
	due, err := needImage(ctx, tx, z, seq)
	if err != nil {
		return false, err
	}
	if due || forceImage {
		raw, err := nzf.EncodeFull(nzf.Image{Origin: origin, Serial: newSerial, Records: desiredRecs})
		if err != nil {
			return false, fmt.Errorf("zone %s image: %w", z.Name, err)
		}
		sha, err := putBlob(ctx, tx, raw)
		if err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO zone_images (zone_id, seq, serial, blob_sha256, raw_size) VALUES ($1,$2,$3,$4,$5)`,
			z.ID, seq, int64(newSerial), sha, len(raw)); err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM zone_images WHERE zone_id = $1 AND seq < $2`, z.ID, seq); err != nil {
			return false, err
		}
		z.ImageSeq = seq
	}
	if _, err := tx.Exec(ctx, `DELETE FROM zone_journal WHERE zone_id = $1 AND seq <= $2 AND seq <= $3`, z.ID, seq-journalKept, z.ImageSeq); err != nil {
		return false, err
	}
	z.Serial, z.CurrentSeq = newSerial, seq
	_, err = tx.Exec(ctx, `UPDATE zones SET serial = $2, current_seq = $3, image_seq = $4, updated_at = now() WHERE id = $1`,
		z.ID, int64(newSerial), seq, z.ImageSeq)
	return true, err
}

// LoadServed returns the current served RR set of z: the image at image_seq with the later
// journal deltas applied.
func LoadServed(ctx context.Context, tx pgx.Tx, z *Zone) ([]dns.RR, error) {
	recs, err := loadServed(ctx, tx, z)
	if err != nil {
		return nil, err
	}
	out := make([]dns.RR, 0, len(recs))
	for _, r := range recs {
		rr, err := nzf.ToRR(r)
		if err != nil {
			return nil, err
		}
		out = append(out, rr)
	}
	return out, nil
}

// SetRecords replaces every stored record of zoneID with rrs (SOA records are skipped: SOA
// fields live on the zone row; repeated records are stored once).
func SetRecords(ctx context.Context, tx pgx.Tx, zoneID uuid.UUID, rrs []dns.RR) error {
	if _, err := tx.Exec(ctx, "DELETE FROM zone_records WHERE zone_id = $1", zoneID); err != nil {
		return err
	}
	seen := make(map[string]bool, len(rrs))
	for _, rr := range rrs {
		if rr.Header().Rrtype == dns.TypeSOA {
			continue
		}
		wire, err := nzf.FromRR(rr)
		if err != nil {
			return invalid("invalid_rdata", err.Error())
		}
		key := strings.ToLower(rr.Header().Name) + "\x00" + string(rune(wire.Type)) + string(wire.RData)
		if seen[key] {
			continue // a repeated record is one record, as in BIND
		}
		seen[key] = true
		if _, err := insertRecord(ctx, tx, zoneID, rr); err != nil {
			return err
		}
	}
	return nil
}

func wireName(name string) ([]byte, error) {
	b := make([]byte, 256)
	n, err := dns.PackDomainName(dns.Fqdn(name), b, 0, nil, false)
	if err != nil {
		return nil, fmt.Errorf("name %q: %w", name, err)
	}
	return b[:n], nil
}

func soaRR(z *Zone) *dns.SOA {
	return &dns.SOA{
		Hdr: dns.RR_Header{Name: z.Name, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: z.SOA.TTL},
		Ns:  z.SOA.MName, Mbox: z.SOA.RName, Serial: z.Serial,
		Refresh: z.SOA.Refresh, Retry: z.SOA.Retry, Expire: z.SOA.Expire, Minttl: z.SOA.Minimum,
	}
}

func desiredRRs(ctx context.Context, tx pgx.Tx, z *Zone) ([]dns.RR, error) {
	recs, err := loadRecordRRs(ctx, tx, z.ID)
	if err != nil {
		return nil, err
	}
	return append([]dns.RR{soaRR(z)}, recs...), nil
}

func setSOASerial(rrs []dns.RR, serial uint32) []dns.RR {
	for _, rr := range rrs {
		if soa, ok := rr.(*dns.SOA); ok {
			soa.Serial = serial
		}
	}
	return rrs
}

func toRecords(rrs []dns.RR) ([]nzf.Record, error) {
	out := make([]nzf.Record, 0, len(rrs))
	for _, rr := range rrs {
		r, err := nzf.FromRR(rr)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// isSOAData reports whether r is the SOA or an RRSIG covering it: these carry the serial and
// travel first in every delta instead of through the diff.
func isSOAData(r nzf.Record) bool {
	return r.Type == dns.TypeSOA || (r.Type == dns.TypeRRSIG && len(r.RData) >= 2 && binary.BigEndian.Uint16(r.RData) == dns.TypeSOA)
}

// soaWithSigs returns the SOA record of rs followed by the RRSIGs covering it.
func soaWithSigs(rs []nzf.Record) []nzf.Record {
	var soa, sigs []nzf.Record
	for _, r := range rs {
		switch {
		case r.Type == dns.TypeSOA:
			soa = append(soa, r)
		case isSOAData(r):
			sigs = append(sigs, r)
		}
	}
	return append(soa, sigs...)
}

func recordKey(r nzf.Record) string {
	var b bytes.Buffer
	b.Write(r.Owner)
	var h [8]byte
	binary.BigEndian.PutUint16(h[0:], r.Type)
	binary.BigEndian.PutUint16(h[2:], r.Class)
	binary.BigEndian.PutUint32(h[4:], r.TTL)
	b.Write(h[:])
	b.Write(r.RData)
	return b.String()
}

// diffIgnoringSOA is the set difference of two served sets on exact wire bytes (owner case and
// TTL included), leaving out SOA data.
func diffIgnoringSOA(previous, desired []nzf.Record) (deleted, added []nzf.Record) {
	prev := make(map[string]struct{}, len(previous))
	for _, r := range previous {
		if !isSOAData(r) {
			prev[recordKey(r)] = struct{}{}
		}
	}
	want := make(map[string]struct{}, len(desired))
	for _, r := range desired {
		if isSOAData(r) {
			continue
		}
		k := recordKey(r)
		if _, dup := want[k]; dup {
			continue
		}
		want[k] = struct{}{}
		if _, ok := prev[k]; !ok {
			added = append(added, r)
		}
	}
	for _, r := range previous {
		if isSOAData(r) {
			continue
		}
		if _, ok := want[recordKey(r)]; !ok {
			deleted = append(deleted, r)
		}
	}
	return deleted, added
}

// soaFieldsEqual compares the SOA of both sets ignoring the serial.
func soaFieldsEqual(previous, desired []nzf.Record) bool {
	a, b := soaWithSigs(previous), soaWithSigs(desired)
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	ra, rb := a[0].RData, b[0].RData
	if a[0].TTL != b[0].TTL || !bytes.Equal(a[0].Owner, b[0].Owner) || len(ra) != len(rb) || len(ra) < 20 {
		return false
	}
	s := len(ra) - 20 // the serial is the first of the five trailing 32-bit fields
	return bytes.Equal(ra[:s], rb[:s]) && bytes.Equal(ra[s+4:], rb[s+4:])
}

func servedSerial(soaFirst []nzf.Record) uint32 {
	rd := soaFirst[0].RData
	return binary.BigEndian.Uint32(rd[len(rd)-20:])
}

func withSerial(r nzf.Record, serial uint32) nzf.Record {
	rd := append([]byte(nil), r.RData...)
	binary.BigEndian.PutUint32(rd[len(rd)-20:], serial)
	r.RData = rd
	return r
}

func putBlob(ctx context.Context, tx pgx.Tx, raw []byte) (string, error) {
	data, _, err := nzf.Compress(raw)
	if err != nil {
		return "", err
	}
	sha, _, err := store.PutBlob(ctx, tx, data)
	return sha, err
}

func readBlob(ctx context.Context, tx pgx.Tx, sha string) ([]byte, error) {
	var data []byte
	if err := tx.QueryRow(ctx, "SELECT data FROM blobs WHERE sha256 = $1", sha).Scan(&data); err != nil {
		return nil, fmt.Errorf("zone blob %s: %w", sha, store.MapError(err))
	}
	return nzf.Decompress(data, maxServedSize)
}

func loadServed(ctx context.Context, tx pgx.Tx, z *Zone) ([]nzf.Record, error) {
	var imageSHA string
	if err := tx.QueryRow(ctx, "SELECT blob_sha256 FROM zone_images WHERE zone_id = $1 AND seq = $2", z.ID, z.ImageSeq).Scan(&imageSHA); err != nil {
		return nil, fmt.Errorf("zone %s image at seq %d: %w", z.Name, z.ImageSeq, store.MapError(err))
	}
	raw, err := readBlob(ctx, tx, imageSHA)
	if err != nil {
		return nil, err
	}
	_, img, _, err := nzf.Decode(raw)
	if err != nil || img == nil {
		return nil, fmt.Errorf("zone %s image: %v", z.Name, err)
	}
	rows, err := tx.Query(ctx, "SELECT blob_sha256 FROM zone_journal WHERE zone_id = $1 AND seq > $2 AND seq <= $3 ORDER BY seq", z.ID, z.ImageSeq, z.CurrentSeq)
	if err != nil {
		return nil, err
	}
	shas, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	set := make(map[string]nzf.Record, len(img.Records))
	for _, r := range img.Records {
		set[recordKey(r)] = r
	}
	for _, sha := range shas {
		raw, err := readBlob(ctx, tx, sha)
		if err != nil {
			return nil, err
		}
		_, _, d, err := nzf.Decode(raw)
		if err != nil || d == nil {
			return nil, fmt.Errorf("zone %s delta %s: %v", z.Name, sha, err)
		}
		for _, r := range d.Deleted {
			delete(set, recordKey(r))
		}
		for _, r := range d.Added {
			set[recordKey(r)] = r
		}
	}
	out := make([]nzf.Record, 0, len(set))
	for _, r := range set {
		out = append(out, r)
	}
	nzf.SortRecords(out)
	return out, nil
}

// needImage reports whether version seq gets a full image: none yet, imageEveryDelta deltas since
// the image, or deltas since the image totalling a quarter of its size (at least imageMinDeltaBytes).
func needImage(ctx context.Context, tx pgx.Tx, z *Zone, seq int64) (bool, error) {
	if z.ImageSeq == 0 || seq-z.ImageSeq >= imageEveryDelta {
		return true, nil
	}
	var imageSize, deltaBytes int64
	err := tx.QueryRow(ctx, `SELECT (SELECT raw_size FROM zone_images WHERE zone_id = $1 AND seq = $2),
		(SELECT coalesce(sum(raw_size), 0) FROM zone_journal WHERE zone_id = $1 AND seq > $2)::bigint`, z.ID, z.ImageSeq).Scan(&imageSize, &deltaBytes)
	if err != nil {
		return false, err
	}
	return deltaBytes >= imageMinDeltaBytes && deltaBytes*4 >= imageSize, nil
}
