package zone

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/dnssecconf"
	"github.com/piwi3910/nexora/mgmt/internal/nzf"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Service manages hosted zones and their records. Every mutation publishes a config version.
type Service struct {
	Store  *store.Store
	Build  snapshot.BuildConfig
	Signer Signer
	Now    func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// zoneColumns are selected FROM zoneFrom; DNSSECEnabled comes from zone_dnssec (Task 12).
const zoneColumns = `z.id, z.name, z.kind, z.revision, z.serial, z.default_ttl, z.soa_mname, z.soa_rname, z.soa_refresh,
	z.soa_retry, z.soa_expire, z.soa_minimum, z.soa_ttl, z.transfer_allow_cidrs, z.transfer_tsig_key_id, z.notify_targets,
	z.update_tsig_key_ids, z.update_allow_cidrs, z.allow_query_cidrs, z.primaries, z.current_seq, z.image_seq, z.loaded, z.expired, z.last_refresh_at, z.last_success_at,
	z.next_refresh_at, z.expires_at, z.last_error, z.last_trigger, COALESCE(d.enabled, false), z.created_at, z.updated_at, z.engine_group_id`

const zoneFrom = " FROM zones z LEFT JOIN zone_dnssec d ON d.zone_id = z.id"

func scanZone(row pgx.Row) (*Zone, error) {
	var z Zone
	var serial int64
	var ttl, refresh, retry, expire, minimum, soaTTL int32
	err := row.Scan(&z.ID, &z.Name, &z.Kind, &z.Revision, &serial, &ttl, &z.SOA.MName, &z.SOA.RName, &refresh, &retry,
		&expire, &minimum, &soaTTL, &z.TransferAllowCIDRs, &z.TransferTSIGKeyID, &z.Notify, &z.UpdateTSIGKeyIDs,
		&z.UpdateAllowCIDRs, &z.AllowQueryCIDRs, &z.Primaries, &z.CurrentSeq, &z.ImageSeq, &z.Loaded, &z.Expired, &z.LastRefreshAt, &z.LastSuccessAt, &z.NextRefreshAt,
		&z.ExpiresAt, &z.LastError, &z.LastTrigger, &z.DNSSECEnabled, &z.CreatedAt, &z.UpdatedAt, &z.EngineGroupID)
	if err != nil {
		return nil, store.MapError(err)
	}
	z.Serial, z.DefaultTTL = uint32(serial), uint32(ttl)
	z.SOA.Refresh, z.SOA.Retry, z.SOA.Expire, z.SOA.Minimum, z.SOA.TTL = uint32(refresh), uint32(retry), uint32(expire), uint32(minimum), uint32(soaTTL)
	return &z, nil
}

func loadZone(ctx context.Context, q querier, id uuid.UUID, forUpdate bool) (*Zone, error) {
	sql := "SELECT " + zoneColumns + zoneFrom + " WHERE z.id = $1"
	if forUpdate {
		sql += " FOR UPDATE OF z"
	}
	z, err := scanZone(q.QueryRow(ctx, sql, id))
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("zone %s: %w", id, store.ErrNotFound)
	}
	return z, err
}

// GetZone returns one zone.
func (s *Service) GetZone(ctx context.Context, id uuid.UUID) (*Zone, error) {
	return loadZone(ctx, s.Store.Pool, id, false)
}

// GetZoneByName returns the zone named name (absolute, any case).
func (s *Service) GetZoneByName(ctx context.Context, name string) (*Zone, error) {
	z, err := scanZone(s.Store.Pool.QueryRow(ctx, "SELECT "+zoneColumns+zoneFrom+" WHERE z.name = $1", dns.CanonicalName(name)))
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("zone %s: %w", name, store.ErrNotFound)
	}
	return z, err
}

// ListZones returns every zone ordered by name.
func (s *Service) ListZones(ctx context.Context) ([]Zone, error) {
	rows, err := s.Store.Pool.Query(ctx, "SELECT "+zoneColumns+zoneFrom+" ORDER BY z.name")
	if err != nil {
		return nil, store.MapError(err)
	}
	defer rows.Close()
	out := []Zone{}
	for rows.Next() {
		z, err := scanZone(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *z)
	}
	return out, store.MapError(rows.Err())
}

func validName(field, name string) (string, error) {
	if !dns.IsFqdn(name) {
		return "", invalid("invalid_name", fmt.Sprintf("%s %q must be an absolute domain name", field, name))
	}
	n, err := dnssecconf.ValidateDomain(name)
	if err != nil || n == "." {
		return "", invalid("invalid_name", fmt.Sprintf("%s %q is not a valid domain name", field, name))
	}
	return n, nil
}

func validEndpoints(field string, eps []Endpoint) error {
	for i, ep := range eps {
		if !dnssecconf.IsIPPort(ep.Address) {
			return invalid("invalid_endpoint", fmt.Sprintf("%s[%d]: %q is not ip:port", field, i, ep.Address))
		}
	}
	return nil
}

func validSOA(soa SOA) error {
	if _, err := validName("soa.mname", soa.MName); err != nil {
		return err
	}
	if _, err := validName("soa.rname", soa.RName); err != nil {
		return err
	}
	if soa.Refresh == 0 || soa.Retry == 0 || soa.Expire == 0 {
		return invalid("invalid_soa", "soa refresh, retry and expire must be positive")
	}
	for _, v := range []uint32{soa.Refresh, soa.Retry, soa.Expire, soa.Minimum, soa.TTL} {
		if v > maxTTL {
			return invalid("invalid_soa", fmt.Sprintf("soa timer %d exceeds %d", v, maxTTL))
		}
	}
	return nil
}

func parseCIDRs(field string, in []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(in))
	for _, c := range in {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			return nil, invalid("invalid_cidr", fmt.Sprintf("%s: %q is not a CIDR", field, c))
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// checkKeys verifies that every referenced TSIG key exists.
func checkKeys(ctx context.Context, tx pgx.Tx, ids []uuid.UUID) error {
	uniq := map[uuid.UUID]bool{}
	for _, id := range ids {
		uniq[id] = true
	}
	if len(uniq) == 0 {
		return nil
	}
	list := make([]uuid.UUID, 0, len(uniq))
	for id := range uniq {
		list = append(list, id)
	}
	// FOR SHARE: a concurrent tsigkey.Delete (FOR UPDATE, then its in-use check) waits for this
	// transaction and then sees the reference.
	rows, err := tx.Query(ctx, "SELECT id FROM tsig_keys WHERE id = ANY($1) FOR SHARE", list)
	if err != nil {
		return err
	}
	found, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	if len(found) != len(list) {
		return invalid("unknown_tsig_key", "a referenced TSIG key does not exist")
	}
	return nil
}

func endpointKeys(eps []Endpoint) []uuid.UUID {
	var ids []uuid.UUID
	for _, ep := range eps {
		if ep.TSIGKeyID != nil {
			ids = append(ids, *ep.TSIGKeyID)
		}
	}
	return ids
}

func withDefaults(soa SOA) SOA {
	if soa.Refresh == 0 {
		soa.Refresh = 10800
	}
	if soa.Retry == 0 {
		soa.Retry = 3600
	}
	if soa.Expire == 0 {
		soa.Expire = 1209600
	}
	if soa.Minimum == 0 {
		soa.Minimum = 3600
	}
	if soa.TTL == 0 {
		soa.TTL = 3600
	}
	return soa
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// CreateZone creates a zone. Primary zones get one apex NS record per nameserver and their first
// served version; secondary zones load on their first transfer. Zero SOA timers take the defaults.
func (s *Service) CreateZone(ctx context.Context, actor auth.Actor, in CreateZoneInput) (*Zone, error) {
	name, err := validName("name", in.Name)
	if err != nil {
		return nil, err
	}
	if in.Kind == "secondary" {
		// Placeholder SOA until the first transfer copies the primary's.
		in.SOA = SOA{MName: name, RName: name}
	}
	in.SOA = withDefaults(in.SOA)
	if err := validSOA(in.SOA); err != nil {
		return nil, err
	}
	if in.DefaultTTL > maxTTL {
		return nil, invalid("invalid_ttl", "default_ttl is too large")
	}
	var ns []dns.RR
	switch in.Kind {
	case "primary":
		if len(in.Nameservers) == 0 || len(in.Primaries) > 0 {
			return nil, invalid("invalid_zone", "primary zones need at least one nameserver and no primaries")
		}
		for _, n := range in.Nameservers {
			target, err := validName("nameservers", n)
			if err != nil {
				return nil, err
			}
			ns = append(ns, &dns.NS{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeNS, Class: dns.ClassINET, Ttl: in.DefaultTTL}, Ns: target})
		}
	case "secondary":
		if len(in.Primaries) == 0 || len(in.Nameservers) > 0 {
			return nil, invalid("invalid_zone", "secondary zones need at least one primary and no nameservers")
		}
	default:
		return nil, invalid("invalid_zone", fmt.Sprintf("kind %q must be primary or secondary", in.Kind))
	}
	if err := validEndpoints("primaries", in.Primaries); err != nil {
		return nil, err
	}
	if err := validEndpoints("notify", in.Notify); err != nil {
		return nil, err
	}
	cidrs, err := parseCIDRs("transfer allow_cidrs", in.Transfer.AllowCIDRs)
	if err != nil {
		return nil, err
	}
	updateCIDRs, err := parseCIDRs("update allow_cidrs", in.UpdateAllowCIDRs)
	if err != nil {
		return nil, err
	}
	queryCIDRs, err := parseCIDRs("allow_query_cidrs", in.AllowQueryCIDRs)
	if err != nil {
		return nil, err
	}
	keys := append(append(endpointKeys(in.Primaries), endpointKeys(in.Notify)...), in.UpdateTSIGKeyIDs...)
	if in.Transfer.TSIGKeyID != nil {
		keys = append(keys, *in.Transfer.TSIGKeyID)
	}
	var out *Zone
	_, err = snapshot.Mutate(ctx, s.Store, s.Build, actor, func(tx pgx.Tx) (auth.Change, error) {
		if err := checkKeys(ctx, tx, keys); err != nil {
			return auth.Change{}, err
		}
		if in.EngineGroupID != nil {
			var one int
			err := tx.QueryRow(ctx, "SELECT 1 FROM engine_groups WHERE id = $1 FOR KEY SHARE", *in.EngineGroupID).Scan(&one)
			if errors.Is(err, pgx.ErrNoRows) {
				return auth.Change{}, ErrUnknownEngineGroup
			}
			if err != nil {
				return auth.Change{}, err
			}
		}
		var id uuid.UUID
		err := tx.QueryRow(ctx, `INSERT INTO zones (name, kind, default_ttl, soa_mname, soa_rname, soa_refresh, soa_retry, soa_expire,
			soa_minimum, soa_ttl, transfer_allow_cidrs, transfer_tsig_key_id, notify_targets, update_tsig_key_ids, primaries, engine_group_id,
			update_allow_cidrs, allow_query_cidrs)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18) RETURNING id`,
			name, in.Kind, int64(in.DefaultTTL), dns.CanonicalName(in.SOA.MName), dns.CanonicalName(in.SOA.RName),
			int64(in.SOA.Refresh), int64(in.SOA.Retry), int64(in.SOA.Expire), int64(in.SOA.Minimum), int64(in.SOA.TTL),
			cidrs, in.Transfer.TSIGKeyID, nonNil(in.Notify), nonNil(in.UpdateTSIGKeyIDs), nonNil(in.Primaries), in.EngineGroupID,
			updateCIDRs, queryCIDRs).Scan(&id)
		if err != nil {
			return auth.Change{}, err
		}
		for _, rr := range ns {
			if _, err := insertRecord(ctx, tx, id, rr); err != nil {
				return auth.Change{}, err
			}
		}
		z, err := loadZone(ctx, tx, id, true)
		if err != nil {
			return auth.Change{}, err
		}
		if z.Kind == "primary" {
			if _, err := Rebuild(ctx, tx, s.Signer, z, RebuildOptions{Force: true}, s.now()); err != nil {
				return auth.Change{}, err
			}
		} else if err := RequestRefresh(ctx, tx, id, "create"); err != nil {
			return auth.Change{}, err
		}
		if out, err = loadZone(ctx, tx, id, false); err != nil {
			return auth.Change{}, err
		}
		return auth.Change{Action: "createZone", TargetType: "zone", TargetID: id.String(), After: out}, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateZone changes zone settings at in.Revision.
func (s *Service) UpdateZone(ctx context.Context, actor auth.Actor, id uuid.UUID, in UpdateZoneInput) (*Zone, error) {
	if in.SOA != nil {
		if err := validSOA(*in.SOA); err != nil {
			return nil, err
		}
	}
	if in.DefaultTTL != nil && *in.DefaultTTL > maxTTL {
		return nil, invalid("invalid_ttl", "default_ttl is too large")
	}
	var keys []uuid.UUID
	var cidrs, updateCIDRs, queryCIDRs []netip.Prefix
	if in.Primaries != nil {
		if err := validEndpoints("primaries", *in.Primaries); err != nil {
			return nil, err
		}
		keys = append(keys, endpointKeys(*in.Primaries)...)
	}
	if in.Notify != nil {
		if err := validEndpoints("notify", *in.Notify); err != nil {
			return nil, err
		}
		keys = append(keys, endpointKeys(*in.Notify)...)
	}
	if in.UpdateTSIGKeyIDs != nil {
		keys = append(keys, *in.UpdateTSIGKeyIDs...)
	}
	if in.Transfer != nil {
		var err error
		if cidrs, err = parseCIDRs("transfer allow_cidrs", in.Transfer.AllowCIDRs); err != nil {
			return nil, err
		}
		if in.Transfer.TSIGKeyID != nil {
			keys = append(keys, *in.Transfer.TSIGKeyID)
		}
	}
	if in.UpdateAllowCIDRs != nil {
		var err error
		if updateCIDRs, err = parseCIDRs("update allow_cidrs", *in.UpdateAllowCIDRs); err != nil {
			return nil, err
		}
	}
	if in.AllowQueryCIDRs != nil {
		var err error
		if queryCIDRs, err = parseCIDRs("allow_query_cidrs", *in.AllowQueryCIDRs); err != nil {
			return nil, err
		}
	}
	return s.Mutate(ctx, id, func(tx pgx.Tx, z *Zone) (string, any, any, RebuildOptions, error) {
		if z.Revision != in.Revision {
			return "", nil, nil, RebuildOptions{}, fmt.Errorf("zone revision %d is stale (current %d): %w", in.Revision, z.Revision, store.ErrConflict)
		}
		if err := checkKeys(ctx, tx, keys); err != nil {
			return "", nil, nil, RebuildOptions{}, err
		}
		if z.Kind == "secondary" && in.SOA != nil {
			return "", nil, nil, RebuildOptions{}, ErrReadOnly
		}
		if z.Kind == "primary" && in.Primaries != nil && len(*in.Primaries) > 0 {
			return "", nil, nil, RebuildOptions{}, invalid("invalid_zone", "primary zones take no primaries")
		}
		if z.Kind == "secondary" && in.Primaries != nil && len(*in.Primaries) == 0 {
			return "", nil, nil, RebuildOptions{}, invalid("invalid_zone", "secondary zones need at least one primary")
		}
		sets := []string{}
		args := []any{z.ID}
		set := func(col string, v any) {
			args = append(args, v)
			sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
		}
		if in.DefaultTTL != nil {
			set("default_ttl", int64(*in.DefaultTTL))
		}
		if in.SOA != nil {
			set("soa_mname", dns.CanonicalName(in.SOA.MName))
			set("soa_rname", dns.CanonicalName(in.SOA.RName))
			set("soa_refresh", int64(in.SOA.Refresh))
			set("soa_retry", int64(in.SOA.Retry))
			set("soa_expire", int64(in.SOA.Expire))
			set("soa_minimum", int64(in.SOA.Minimum))
			set("soa_ttl", int64(in.SOA.TTL))
		}
		if in.Primaries != nil {
			set("primaries", nonNil(*in.Primaries))
		}
		if in.Notify != nil {
			set("notify_targets", nonNil(*in.Notify))
		}
		if in.UpdateTSIGKeyIDs != nil {
			set("update_tsig_key_ids", nonNil(*in.UpdateTSIGKeyIDs))
		}
		if in.UpdateAllowCIDRs != nil {
			set("update_allow_cidrs", updateCIDRs)
		}
		if in.AllowQueryCIDRs != nil {
			set("allow_query_cidrs", queryCIDRs)
		}
		if in.Transfer != nil {
			set("transfer_allow_cidrs", cidrs)
			set("transfer_tsig_key_id", in.Transfer.TSIGKeyID)
		}
		if len(sets) > 0 {
			if _, err := tx.Exec(ctx, "UPDATE zones SET "+strings.Join(sets, ", ")+" WHERE id = $1", args...); err != nil {
				return "", nil, nil, RebuildOptions{}, err
			}
		}
		return "updateZone", z, nil, RebuildOptions{}, nil
	}, actor)
}

// RefreshChannel is the pg_notify channel that wakes the secondary-zone refresh scheduler; the
// payload is the zone id.
const RefreshChannel = "nexora_zone_refresh"

// Execer runs a statement (a pool or a transaction).
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// RequestRefresh makes secondary zone id due now with trigger ("notify", "manual" or "create") and
// wakes the scheduler. It returns ErrNotFound when id is not a secondary zone.
func RequestRefresh(ctx context.Context, q Execer, id uuid.UUID, trigger string) error {
	tag, err := q.Exec(ctx, `UPDATE zones SET next_refresh_at = now(), refresh_trigger = $2, refresh_requests = refresh_requests + 1
		WHERE id = $1 AND kind = 'secondary'`, id, trigger)
	if err != nil {
		return store.MapError(err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("secondary zone %s: %w", id, store.ErrNotFound)
	}
	_, err = q.Exec(ctx, "SELECT pg_notify($1, $2)", RefreshChannel, id.String())
	return store.MapError(err)
}

// RefreshNow schedules an immediate refresh of secondary zone id.
func (s *Service) RefreshNow(ctx context.Context, id uuid.UUID) error {
	z, err := s.GetZone(ctx, id)
	if err != nil {
		return err
	}
	if z.Kind != "secondary" {
		return invalid("zone_not_secondary", "only secondary zones are refreshed from a primary")
	}
	return RequestRefresh(ctx, s.Store.Pool, id, "manual")
}

// DeleteZone removes the zone at revision.
func (s *Service) DeleteZone(ctx context.Context, actor auth.Actor, id uuid.UUID, revision int64) error {
	_, err := snapshot.Mutate(ctx, s.Store, s.Build, actor, func(tx pgx.Tx) (auth.Change, error) {
		z, err := loadZone(ctx, tx, id, true)
		if err != nil {
			return auth.Change{}, err
		}
		if z.Revision != revision {
			return auth.Change{}, fmt.Errorf("zone revision %d is stale (current %d): %w", revision, z.Revision, store.ErrConflict)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM zones WHERE id = $1", id); err != nil {
			return auth.Change{}, err
		}
		return auth.Change{Action: "deleteZone", TargetType: "zone", TargetID: id.String(), Before: z}, nil
	})
	return err
}

// Mutate is the single transactional path for zone content changes: it locks the zone row, runs
// fn, bumps the zone revision, rebuilds the served zone and publishes a config version whose
// audit row carries fn's action, before and after (after defaults to the resulting zone).
func (s *Service) Mutate(ctx context.Context, zoneID uuid.UUID, fn func(tx pgx.Tx, z *Zone) (auditAction string, before, after any, opts RebuildOptions, err error), actor auth.Actor) (*Zone, error) {
	return s.MutateFenced(ctx, zoneID, nil, fn, actor)
}

// MutateFenced invokes fence in the mutation transaction before locking the zone.
// The fence must only use tx and is rerun on transaction retries.
func (s *Service) MutateFenced(ctx context.Context, zoneID uuid.UUID, fence func(pgx.Tx) error, fn func(tx pgx.Tx, z *Zone) (auditAction string, before, after any, opts RebuildOptions, err error), actor auth.Actor) (*Zone, error) {
	var out *Zone
	_, err := snapshot.Mutate(ctx, s.Store, s.Build, actor, func(tx pgx.Tx) (auth.Change, error) {
		if fence != nil {
			if err := fence(tx); err != nil {
				return auth.Change{}, err
			}
		}
		z, err := loadZone(ctx, tx, zoneID, true)
		if err != nil {
			return auth.Change{}, err
		}
		action, before, after, opts, err := fn(tx, z)
		if err != nil {
			return auth.Change{}, err
		}
		if _, err := tx.Exec(ctx, "UPDATE zones SET revision = revision + 1, updated_at = now() WHERE id = $1", zoneID); err != nil {
			return auth.Change{}, err
		}
		if z, err = loadZone(ctx, tx, zoneID, false); err != nil {
			return auth.Change{}, err
		}
		if z.Kind == "primary" || opts.Serial != nil {
			if _, err := Rebuild(ctx, tx, s.Signer, z, opts, s.now()); err != nil {
				return auth.Change{}, err
			}
		}
		if out, err = loadZone(ctx, tx, zoneID, false); err != nil {
			return auth.Change{}, err
		}
		if after == nil {
			after = out
		}
		return auth.Change{Action: action, TargetType: "zone", TargetID: zoneID.String(), Before: before, After: after}, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func insertRecord(ctx context.Context, tx pgx.Tx, zoneID uuid.UUID, rr dns.RR) (*Record, error) {
	wire, err := nzf.FromRR(rr)
	if err != nil {
		return nil, invalid("invalid_rdata", err.Error())
	}
	h := rr.Header()
	r := &Record{ZoneID: zoneID, Name: h.Name, Type: dns.TypeToString[h.Rrtype], TTL: h.Ttl, Data: RDataText(rr)}
	err = tx.QueryRow(ctx, `INSERT INTO zone_records (zone_id, owner, rtype, ttl, rdata, rdata_wire) VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING id, revision`, zoneID, h.Name, int32(h.Rrtype), int64(h.Ttl), r.Data, wire.RData).Scan(&r.ID, &r.Revision)
	if err != nil {
		return nil, store.MapError(err)
	}
	return r, nil
}

// LoadRecords returns the stored records of zoneID as RRs.
func LoadRecords(ctx context.Context, tx pgx.Tx, zoneID uuid.UUID) ([]dns.RR, error) {
	return loadRecordRRs(ctx, tx, zoneID)
}

// ApplyRecordChanges removes the stored records matching deleted (owner, type and RDATA; the TTL
// is ignored) and adds added, where a record already stored only takes the new TTL. Every RRset
// written takes the TTL of its last added record. SOA records are skipped.
func ApplyRecordChanges(ctx context.Context, tx pgx.Tx, zoneID uuid.UUID, deleted, added []dns.RR) error {
	for _, rr := range deleted {
		h := rr.Header()
		if h.Rrtype == dns.TypeSOA {
			continue
		}
		wire, err := nzf.FromRR(rr)
		if err != nil {
			return invalid("invalid_rdata", err.Error())
		}
		if _, err := tx.Exec(ctx, `DELETE FROM zone_records WHERE zone_id = $1 AND lower(owner) = lower($2) AND rtype = $3
			AND sha256(rdata_wire) = sha256($4::bytea)`, zoneID, h.Name, int32(h.Rrtype), wire.RData); err != nil {
			return err
		}
	}
	for _, rr := range added {
		h := rr.Header()
		if h.Rrtype == dns.TypeSOA {
			continue
		}
		wire, err := nzf.FromRR(rr)
		if err != nil {
			return invalid("invalid_rdata", err.Error())
		}
		if _, err := tx.Exec(ctx, `INSERT INTO zone_records (zone_id, owner, rtype, ttl, rdata, rdata_wire) VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (zone_id, lower(owner), rtype, sha256(rdata_wire)) DO UPDATE SET ttl = EXCLUDED.ttl,
			revision = zone_records.revision + 1, updated_at = now() WHERE zone_records.ttl <> EXCLUDED.ttl`,
			zoneID, h.Name, int32(h.Rrtype), int64(h.Ttl), RDataText(rr), wire.RData); err != nil {
			return store.MapError(err)
		}
		if err := setRRsetTTL(ctx, tx, zoneID, h.Name, h.Rrtype, h.Ttl); err != nil {
			return err
		}
	}
	return nil
}

// setRRsetTTL gives every record of the RRset the TTL just written.
func setRRsetTTL(ctx context.Context, tx pgx.Tx, zoneID uuid.UUID, owner string, rtype uint16, ttl uint32) error {
	_, err := tx.Exec(ctx, `UPDATE zone_records SET ttl = $1, revision = revision + 1, updated_at = now()
		WHERE zone_id = $2 AND lower(owner) = lower($3) AND rtype = $4 AND ttl <> $1`, int64(ttl), zoneID, owner, int32(rtype))
	return err
}

// loadRecordRRs returns the stored records of zoneID as RRs.
func loadRecordRRs(ctx context.Context, q querier, zoneID uuid.UUID) ([]dns.RR, error) {
	return queryRRs(ctx, q, "SELECT owner, rtype, ttl, rdata_wire FROM zone_records WHERE zone_id = $1", zoneID)
}

// queryRRs runs sql, which selects owner, rtype, ttl and rdata_wire of zone_records, and returns
// the rows as RRs.
func queryRRs(ctx context.Context, q querier, sql string, args ...any) ([]dns.RR, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dns.RR
	owner := make([]byte, 256)
	for rows.Next() {
		var name string
		var rtype int32
		var ttl int64
		var rdata []byte
		if err := rows.Scan(&name, &rtype, &ttl, &rdata); err != nil {
			return nil, err
		}
		n, err := dns.PackDomainName(name, owner, 0, nil, false)
		if err != nil {
			return nil, fmt.Errorf("record owner %q: %w", name, err)
		}
		rr, err := nzf.ToRR(nzf.Record{Owner: owner[:n], Type: uint16(rtype), Class: dns.ClassINET, TTL: uint32(ttl), RData: rdata})
		if err != nil {
			return nil, fmt.Errorf("record %s %d: %w", name, rtype, err)
		}
		out = append(out, rr)
	}
	return out, rows.Err()
}

// rrset names one RRset of a zone; owners compare case-insensitively.
type rrset struct {
	owner string
	rtype uint16
}

// loadRRsets returns the stored records of the RRsets sets of zoneID, each RRset once.
func loadRRsets(ctx context.Context, tx pgx.Tx, zoneID uuid.UUID, sets ...rrset) ([]dns.RR, error) {
	var out []dns.RR
	for i, set := range sets {
		if slices.ContainsFunc(sets[:i], func(o rrset) bool { return o.rtype == set.rtype && strings.EqualFold(o.owner, set.owner) }) {
			continue
		}
		rrs, err := queryRRs(ctx, tx, "SELECT owner, rtype, ttl, rdata_wire FROM zone_records WHERE zone_id = $1 AND lower(owner) = lower($2) AND rtype = $3",
			zoneID, set.owner, int32(set.rtype))
		if err != nil {
			return nil, err
		}
		out = append(out, rrs...)
	}
	return out, nil
}

// checkOwners applies CheckSet's rules to the edited owners of z, reading only the records at the
// owners and their ancestors up to the apex (which covers the apex NS count) and, for an owner
// holding a DNAME, whether any name lies below it.
func checkOwners(ctx context.Context, tx pgx.Tx, z *Zone, owners ...string) error {
	apex := strings.ToLower(z.Name)
	names := []string{apex}
	lower := make([]string, len(owners))
	for i, o := range owners {
		lower[i] = strings.ToLower(o)
		names = append(append(names, lower[i]), ancestors(apex, lower[i])...)
	}
	rows, err := tx.Query(ctx, "SELECT lower(owner), rtype FROM zone_records WHERE zone_id = $1 AND lower(owner) = ANY($2)", z.ID, names)
	if err != nil {
		return err
	}
	types := ownerTypes{}
	var owner string
	var rtype int32
	if _, err := pgx.ForEachRow(rows, []any{&owner, &rtype}, func() error {
		types.add(owner, uint16(rtype))
		return nil
	}); err != nil {
		return err
	}
	if err := checkApexNS(types[apex][dns.TypeNS]); err != nil {
		return err
	}
	for _, o := range lower {
		if types[o] == nil {
			continue // no records left at o
		}
		if err := checkOwner(apex, o, types); err != nil {
			return err
		}
		if types[o][dns.TypeDNAME] == 0 {
			continue
		}
		var below string
		err := tx.QueryRow(ctx, `SELECT lower(owner) FROM zone_records WHERE zone_id = $1 AND lower(owner) LIKE '%.' || $2 ESCAPE '\' LIMIT 1`,
			z.ID, likeEscaper.Replace(o)).Scan(&below)
		if err == nil {
			return invalid("dname_occludes", fmt.Sprintf("%s is below the DNAME at %s", below, o))
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	return nil
}

// likeEscaper escapes the LIKE wildcards and the escape character itself.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// rtypeOf returns the numeric type of a record read back by scanRecord.
func rtypeOf(r *Record) uint16 {
	if t, ok := dns.StringToType[r.Type]; ok {
		return t
	}
	var t uint16
	_, _ = fmt.Sscanf(r.Type, "TYPE%d", &t)
	return t
}

func writable(z *Zone) error {
	if z.Kind != "primary" {
		return ErrReadOnly
	}
	return nil
}

const recordColumns = "id, zone_id, owner, rtype, ttl, rdata, revision"

func scanRecord(row pgx.Row) (*Record, error) {
	var r Record
	var rtype int32
	var ttl int64
	if err := row.Scan(&r.ID, &r.ZoneID, &r.Name, &rtype, &ttl, &r.Data, &r.Revision); err != nil {
		return nil, store.MapError(err)
	}
	r.Type, r.TTL = dns.TypeToString[uint16(rtype)], uint32(ttl)
	if r.Type == "" {
		r.Type = fmt.Sprintf("TYPE%d", rtype)
	}
	return &r, nil
}

// lockRecord loads record recordID of zoneID for update and checks its revision.
func lockRecord(ctx context.Context, tx pgx.Tx, zoneID, recordID uuid.UUID, revision int64) (*Record, error) {
	r, err := scanRecord(tx.QueryRow(ctx, "SELECT "+recordColumns+" FROM zone_records WHERE id = $1 AND zone_id = $2 FOR UPDATE", recordID, zoneID))
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("record %s: %w", recordID, store.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	if r.Revision != revision {
		return nil, fmt.Errorf("record revision %d is stale (current %d): %w", revision, r.Revision, store.ErrConflict)
	}
	return r, nil
}

// CreateRecord adds one record to a primary zone.
func (s *Service) CreateRecord(ctx context.Context, actor auth.Actor, zoneID uuid.UUID, in RecordInput) (*Record, error) {
	var rec *Record
	_, err := s.Mutate(ctx, zoneID, func(tx pgx.Tx, z *Zone) (string, any, any, RebuildOptions, error) {
		if err := writable(z); err != nil {
			return "", nil, nil, RebuildOptions{}, err
		}
		rr, _, err := ParseRecord(z.Name, in)
		if err != nil {
			return "", nil, nil, RebuildOptions{}, err
		}
		h := rr.Header()
		set := rrset{h.Name, h.Rrtype}
		edit, err := recordEdit(ctx, tx, z, []string{h.Name}, []rrset{set}, func() error {
			if rec, err = insertRecord(ctx, tx, z.ID, rr); err != nil {
				return err
			}
			return setRRsetTTL(ctx, tx, z.ID, h.Name, h.Rrtype, h.Ttl)
		})
		return "createZoneRecord", nil, rec, RebuildOptions{Edit: edit}, err
	}, actor)
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// recordEdit runs change, a record edit of z, between reads of the RRsets sets, then checks the
// zone rules at owners. It returns the RRsets before and after the edit.
func recordEdit(ctx context.Context, tx pgx.Tx, z *Zone, owners []string, sets []rrset, change func() error) (*EditDelta, error) {
	before, err := loadRRsets(ctx, tx, z.ID, sets...)
	if err != nil {
		return nil, err
	}
	if err := change(); err != nil {
		return nil, err
	}
	if err := checkOwners(ctx, tx, z, owners...); err != nil {
		return nil, err
	}
	after, err := loadRRsets(ctx, tx, z.ID, sets...)
	if err != nil {
		return nil, err
	}
	return &EditDelta{Before: before, After: after}, nil
}

// UpdateRecord replaces record recordID at revision.
func (s *Service) UpdateRecord(ctx context.Context, actor auth.Actor, zoneID, recordID uuid.UUID, revision int64, in RecordInput) (*Record, error) {
	var rec *Record
	_, err := s.Mutate(ctx, zoneID, func(tx pgx.Tx, z *Zone) (string, any, any, RebuildOptions, error) {
		if err := writable(z); err != nil {
			return "", nil, nil, RebuildOptions{}, err
		}
		before, err := lockRecord(ctx, tx, zoneID, recordID, revision)
		if err != nil {
			return "", nil, nil, RebuildOptions{}, err
		}
		rr, data, err := ParseRecord(z.Name, in)
		if err != nil {
			return "", nil, nil, RebuildOptions{}, err
		}
		wire, err := nzf.FromRR(rr)
		if err != nil {
			return "", nil, nil, RebuildOptions{}, invalid("invalid_rdata", err.Error())
		}
		h := rr.Header()
		sets := []rrset{{before.Name, rtypeOf(before)}, {h.Name, h.Rrtype}}
		edit, err := recordEdit(ctx, tx, z, []string{before.Name, h.Name}, sets, func() error {
			rec, err = scanRecord(tx.QueryRow(ctx, `UPDATE zone_records SET owner = $3, rtype = $4, ttl = $5, rdata = $6, rdata_wire = $7,
				revision = revision + 1, updated_at = now() WHERE id = $1 AND zone_id = $2 RETURNING `+recordColumns,
				recordID, zoneID, h.Name, int32(h.Rrtype), int64(h.Ttl), data, wire.RData))
			if err != nil {
				return err
			}
			return setRRsetTTL(ctx, tx, z.ID, h.Name, h.Rrtype, h.Ttl)
		})
		return "updateZoneRecord", before, rec, RebuildOptions{Edit: edit}, err
	}, actor)
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// DeleteRecord removes record recordID at revision.
func (s *Service) DeleteRecord(ctx context.Context, actor auth.Actor, zoneID, recordID uuid.UUID, revision int64) error {
	_, err := s.Mutate(ctx, zoneID, func(tx pgx.Tx, z *Zone) (string, any, any, RebuildOptions, error) {
		if err := writable(z); err != nil {
			return "", nil, nil, RebuildOptions{}, err
		}
		before, err := lockRecord(ctx, tx, zoneID, recordID, revision)
		if err != nil {
			return "", nil, nil, RebuildOptions{}, err
		}
		edit, err := recordEdit(ctx, tx, z, []string{before.Name}, []rrset{{before.Name, rtypeOf(before)}}, func() error {
			_, err := tx.Exec(ctx, "DELETE FROM zone_records WHERE id = $1", recordID)
			return err
		})
		return "deleteZoneRecord", before, map[string]string{"deleted": recordID.String()}, RebuildOptions{Edit: edit}, err
	}, actor)
	return err
}

type recordCursor struct {
	Owner string    `json:"o"`
	Type  int32     `json:"t"`
	ID    uuid.UUID `json:"i"`
}

// ListRecords pages through the records of zoneID ordered by owner, type and id, optionally
// filtered by owner name and type. after is the cursor a previous page returned.
func (s *Service) ListRecords(ctx context.Context, zoneID uuid.UUID, name, rtype string, after string, limit int) ([]Record, string, error) {
	if _, err := s.GetZone(ctx, zoneID); err != nil {
		return nil, "", err
	}
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	where := []string{"zone_id = $1"}
	args := []any{zoneID}
	add := func(cond string, v ...any) {
		for i := range v {
			args = append(args, v[i])
			cond = strings.Replace(cond, "?", fmt.Sprintf("$%d", len(args)), 1)
		}
		where = append(where, cond)
	}
	if name != "" {
		add("lower(owner) = lower(?)", name)
	}
	if rtype != "" {
		t, ok := dns.StringToType[strings.ToUpper(rtype)]
		if !ok {
			return nil, "", invalid("unsupported_type", fmt.Sprintf("unknown record type %s", rtype))
		}
		add("rtype = ?", int32(t))
	}
	if after != "" {
		raw, err := base64.RawURLEncoding.DecodeString(after)
		var c recordCursor
		if err != nil || json.Unmarshal(raw, &c) != nil {
			return nil, "", invalid("invalid_cursor", "cursor is not valid")
		}
		add("(lower(owner), rtype, id) > (?, ?, ?)", c.Owner, c.Type, c.ID)
	}
	args = append(args, limit+1)
	rows, err := s.Store.Pool.Query(ctx, "SELECT "+recordColumns+" FROM zone_records WHERE "+strings.Join(where, " AND ")+
		fmt.Sprintf(" ORDER BY lower(owner), rtype, id LIMIT $%d", len(args)), args...)
	if err != nil {
		return nil, "", store.MapError(err)
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, "", store.MapError(err)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		raw, _ := json.Marshal(recordCursor{Owner: strings.ToLower(last.Name), Type: int32(dns.StringToType[last.Type]), ID: last.ID})
		next = base64.RawURLEncoding.EncodeToString(raw)
	}
	return out, next, nil
}
