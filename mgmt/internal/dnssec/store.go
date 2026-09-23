package dnssec

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

// Store is the zone.Signer of DNSSEC-enabled zones: keys and settings come from Postgres, private
// keys from Box (unsealed only for the signing call, or used inside the PKCS#11 token), and
// signatures are cached in zone_signatures so unchanged RRsets keep theirs.
type Store struct{ Box *secrets.Box }

var _ zone.Signer = (*Store)(nil)

// cacheChunk bounds the rows of one zone_signatures write.
const cacheChunk = 5000

// Sign signs rrs, the unsigned served set of z.
func (s *Store) Sign(ctx context.Context, tx pgx.Tx, z *zone.Zone, rrs []dns.RR, now time.Time) ([]dns.RR, error) {
	var nsecMode string
	err := tx.QueryRow(ctx, "SELECT nsec_mode FROM zone_dnssec WHERE zone_id = $1 AND enabled", z.ID).Scan(&nsecMode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("zone %s: DNSSEC is not enabled", z.Name)
	}
	if err != nil {
		return nil, err
	}
	keys, release, err := s.loadKeys(ctx, tx, z)
	if err != nil {
		return nil, err
	}
	defer release()
	cache, err := loadCache(ctx, tx, z.ID, nil)
	if err != nil {
		return nil, err
	}
	out, err := Sign(Input{Origin: z.Name, Records: rrs, Keys: keys, NSEC3: nsecMode == "nsec3", Cache: cache, Now: now})
	if err != nil {
		return nil, err
	}
	if err := saveCache(ctx, tx, z.ID, cache, out.Cache); err != nil {
		return nil, err
	}
	next, err := s.rolloverDue(ctx, tx, z, rrs, now)
	if err != nil {
		return nil, err
	}
	if next.IsZero() || out.NextRefresh.Before(next) {
		next = out.NextRefresh
	}
	if _, err := tx.Exec(ctx, "UPDATE zone_dnssec SET next_maintenance_at = $2 WHERE zone_id = $1", z.ID, next); err != nil {
		return nil, err
	}
	return out.Served, nil
}

// rolloverDue returns when the next key transition of z is due: now when one already is (the
// maintainer applies it on its next run), zero when none is pending.
func (s *Store) rolloverDue(ctx context.Context, tx pgx.Tx, z *zone.Zone, rrs []dns.RR, now time.Time) (time.Time, error) {
	st, _, err := loadSettings(ctx, tx, s.Box, z.ID)
	if err != nil {
		return time.Time{}, err
	}
	keys, err := loadKeyStates(ctx, tx, z.ID)
	if err != nil {
		return time.Time{}, err
	}
	var maxTTL uint32
	for _, rr := range rrs {
		maxTTL = max(maxTTL, rr.Header().Ttl)
	}
	out, act, next := Advance(keys, policyOf(st, z.SOA.TTL, maxTTL), now)
	if act.CreateZSK || !slices.EqualFunc(out, keys, func(a, b KeyState) bool { return a.State == b.State }) {
		return now, nil
	}
	return next, nil
}

// SignZONEMD replaces the RRSIGs covering the apex ZONEMD RRset in served after Rebuild set its
// digest, signing it with the active ZSKs. The zone_signatures cache is bypassed for this RRset:
// the digest changes with every version, so a cached signature would never be reused.
func (s *Store) SignZONEMD(ctx context.Context, tx pgx.Tx, z *zone.Zone, served []dns.RR, now time.Time) ([]dns.RR, error) {
	apex := dns.CanonicalName(z.Name)
	var set []dns.RR
	out := make([]dns.RR, 0, len(served))
	for _, rr := range served {
		atApex := dns.CanonicalName(rr.Header().Name) == apex
		if sig, ok := rr.(*dns.RRSIG); ok && atApex && sig.TypeCovered == dns.TypeZONEMD {
			continue
		}
		if atApex && rr.Header().Rrtype == dns.TypeZONEMD {
			set = append(set, rr)
		}
		out = append(out, rr)
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("zone %s: no apex ZONEMD in the served set", z.Name)
	}
	keys, release, err := s.loadKeys(ctx, tx, z)
	if err != nil {
		return nil, err
	}
	defer release()
	signed := &Output{Cache: map[SigKey]CachedSig{}}
	sigs, err := signSet(apex, setKey{owner: apex, rtype: dns.TypeZONEMD}, set, keys, map[SigKey]CachedSig{}, now, signed)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE zone_dnssec SET next_maintenance_at = LEAST(next_maintenance_at, $2) WHERE zone_id = $1`, z.ID, signed.NextRefresh); err != nil {
		return nil, err
	}
	return append(out, sigs...), nil
}

// ResignSOA replaces the SOA signatures in served after Rebuild set the new serial.
func (s *Store) ResignSOA(ctx context.Context, tx pgx.Tx, z *zone.Zone, served []dns.RR, now time.Time) ([]dns.RR, error) {
	var soa []dns.RR
	out := make([]dns.RR, 0, len(served))
	for _, rr := range served {
		if sig, ok := rr.(*dns.RRSIG); ok && sig.TypeCovered == dns.TypeSOA {
			continue
		}
		if rr.Header().Rrtype == dns.TypeSOA {
			soa = append(soa, rr)
		}
		out = append(out, rr)
	}
	if len(soa) != 1 {
		return nil, fmt.Errorf("zone %s: %d SOA records in the served set", z.Name, len(soa))
	}
	keys, release, err := s.loadKeys(ctx, tx, z)
	if err != nil {
		return nil, err
	}
	defer release()
	k := setKey{owner: strings.ToLower(soa[0].Header().Name), rtype: dns.TypeSOA}
	cache, err := loadCache(ctx, tx, z.ID, &k)
	if err != nil {
		return nil, err
	}
	signed := &Output{Cache: map[SigKey]CachedSig{}}
	sigs, err := signSet(dns.CanonicalName(z.Name), k, soa, keys, cache, now, signed)
	if err != nil {
		return nil, err
	}
	if err := saveCache(ctx, tx, z.ID, cache, signed.Cache); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE zone_dnssec SET next_maintenance_at = LEAST(next_maintenance_at, $2) WHERE zone_id = $1`, z.ID, signed.NextRefresh); err != nil {
		return nil, err
	}
	return append(out, sigs...), nil
}

// loadKeys returns the published, active and retired keys of z. Only active keys get a signer;
// release zeroes their unsealed private keys and must be called once signing is done.
func (s *Store) loadKeys(ctx context.Context, tx pgx.Tx, z *zone.Zone) ([]Key, func(), error) {
	rows, err := tx.Query(ctx, `SELECT id, role, algorithm, public_key, backend, key_ref, private_envelope, state, ds_state, activated_at
		FROM dnssec_keys WHERE zone_id = $1 AND state IN ('published', 'active', 'retired') ORDER BY role, published_at, id`, z.ID)
	if err != nil {
		return nil, nil, err
	}
	type row struct {
		id                   uuid.UUID
		role, state, dsState string
		algorithm            int16
		stored               secrets.StoredKey
		activatedAt          *time.Time
	}
	var list []row
	// Only the newest active KSK is advertised in CDS/CDNSKEY while its DS is pending: during a
	// double-signature rollover the parent's DS set should move to it alone.
	var cdsKey uuid.UUID
	var cdsAt time.Time
	for rows.Next() {
		var r row
		var backend string
		if err := rows.Scan(&r.id, &r.role, &r.algorithm, &r.stored.PublicKey, &backend, &r.stored.KeyRef, &r.stored.Envelope, &r.state, &r.dsState, &r.activatedAt); err != nil {
			rows.Close()
			return nil, nil, err
		}
		r.stored.Backend, r.stored.Algorithm = secrets.Backend(backend), uint8(r.algorithm)
		if r.role == "ksk" && r.state == "active" && r.activatedAt != nil && !r.activatedAt.Before(cdsAt) {
			cdsKey, cdsAt = r.id, *r.activatedAt
		}
		list = append(list, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var releases []func()
	release := func() {
		for _, f := range releases {
			f()
		}
	}
	keys := make([]Key, 0, len(list))
	for _, r := range list {
		flags := uint16(256)
		if r.role == "ksk" {
			flags = 257
		}
		k := Key{
			ID: r.id.String(), Role: r.role, Algorithm: r.stored.Algorithm,
			DNSKEY: &dns.DNSKEY{Hdr: hdr(z.Name, dns.TypeDNSKEY, z.SOA.TTL), Flags: flags, Protocol: 3, Algorithm: r.stored.Algorithm, PublicKey: r.stored.PublicKey},
			Signs:  r.state == "active",
			InCDS:  r.id == cdsKey && r.dsState == "pending",
		}
		if k.Signs {
			signer, rel, err := s.Box.Signer(r.stored)
			if err != nil {
				release()
				return nil, nil, fmt.Errorf("zone %s %s key %s: %w", z.Name, r.role, r.id, err)
			}
			k.Signer = signer
			releases = append(releases, rel)
		}
		keys = append(keys, k)
	}
	return keys, release, nil
}

// loadCache reads the cached signatures of zoneID, or only those of one RRset when only is set. Rows that
// no longer decode are left out, so their RRsets are signed again and the rows replaced.
func loadCache(ctx context.Context, tx pgx.Tx, zoneID uuid.UUID, only *setKey) (map[SigKey]CachedSig, error) {
	sql := "SELECT owner, type_covered, key_tag, rrset_digest, rrsig FROM zone_signatures WHERE zone_id = $1"
	args := []any{zoneID}
	if only != nil {
		sql += " AND owner = $2 AND type_covered = $3"
		args = append(args, only.owner, int32(only.rtype))
	}
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cache := map[SigKey]CachedSig{}
	for rows.Next() {
		var owner string
		var typ, tag int32
		var digest, wire []byte
		if err := rows.Scan(&owner, &typ, &tag, &digest, &wire); err != nil {
			return nil, err
		}
		rr, _, err := dns.UnpackRR(wire, 0)
		sig, ok := rr.(*dns.RRSIG)
		if err != nil || !ok || len(digest) != 32 {
			continue
		}
		c := CachedSig{RRSIG: sig}
		copy(c.Digest[:], digest)
		cache[SigKey{Owner: owner, Type: uint16(typ), KeyTag: uint16(tag)}] = c
	}
	return cache, rows.Err()
}

// saveCache writes the signatures of next that differ from old and deletes the rows of old that
// next no longer holds.
func saveCache(ctx context.Context, tx pgx.Tx, zoneID uuid.UUID, old, next map[SigKey]CachedSig) error {
	var owners []string
	var types, tags []int32
	var digests, sigs [][]byte
	var exps []time.Time
	flush := func() error {
		if len(owners) == 0 {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO zone_signatures (zone_id, owner, type_covered, key_tag, rrset_digest, expiration, rrsig)
			SELECT $1, u.owner, u.t, u.tag, u.digest, u.exp, u.sig
			FROM unnest($2::text[], $3::int[], $4::int[], $5::bytea[], $6::timestamptz[], $7::bytea[]) AS u(owner, t, tag, digest, exp, sig)
			ON CONFLICT (zone_id, owner, type_covered, key_tag) DO UPDATE
			SET rrset_digest = EXCLUDED.rrset_digest, expiration = EXCLUDED.expiration, rrsig = EXCLUDED.rrsig`,
			zoneID, owners, types, tags, digests, exps, sigs)
		owners, types, tags, digests, exps, sigs = owners[:0], types[:0], tags[:0], digests[:0], exps[:0], sigs[:0]
		return err
	}
	buf := make([]byte, dns.MaxMsgSize+256)
	for k, c := range next {
		if o, ok := old[k]; ok && o.RRSIG == c.RRSIG && o.Digest == c.Digest {
			continue // reused as loaded
		}
		n, err := dns.PackRR(c.RRSIG, buf, 0, nil, false)
		if err != nil {
			return fmt.Errorf("pack RRSIG %s/%s: %w", k.Owner, dns.TypeToString[k.Type], err)
		}
		owners, types, tags = append(owners, k.Owner), append(types, int32(k.Type)), append(tags, int32(k.KeyTag))
		digests, sigs = append(digests, append([]byte(nil), c.Digest[:]...)), append(sigs, append([]byte(nil), buf[:n]...))
		exps = append(exps, time.Unix(int64(c.RRSIG.Expiration), 0))
		if len(owners) == cacheChunk {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	for k := range old {
		if _, ok := next[k]; ok {
			continue
		}
		owners, types, tags = append(owners, k.Owner), append(types, int32(k.Type)), append(tags, int32(k.KeyTag))
	}
	if len(owners) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `DELETE FROM zone_signatures s USING unnest($2::text[], $3::int[], $4::int[]) AS d(owner, t, tag)
		WHERE s.zone_id = $1 AND s.owner = d.owner AND s.type_covered = d.t AND s.key_tag = d.tag`, zoneID, owners, types, tags)
	return err
}
