package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/dnssecconf"
)

// ErrLastRootAnchor refuses deleting the only trust anchor of the root zone.
var ErrLastRootAnchor = errors.New("the last trust anchor for the root zone cannot be deleted")

// RootHint is one root server override.
type RootHint = dnssecconf.RootHint

// ResolutionSettings is the global resolution mode and recursion tuning.
type ResolutionSettings struct {
	Mode                                                  string
	QnameMinimisation, AggressiveNSEC                     bool
	MaxUpstreamQueries, MaxDelegationDepth, AuthorityPort int32
	RootHints                                             []RootHint
	Revision                                              int64
}

// ForwardZone sends queries at or below Domain to its own servers.
type ForwardZone struct {
	ID        uuid.UUID
	Domain    string
	Addresses []string
	Validate  bool
	Revision  int64
}

// DnssecSettings are the global validation switches.
type DnssecSettings struct {
	Validation, ValidateForwarded, RFC5011 bool
	Revision                               int64
}

// TrustAnchor is a DS record anchoring validation at Zone.
type TrustAnchor struct {
	ID               uuid.UUID
	Zone, DS, Source string
	CreatedAt        time.Time
}

// NegativeTrustAnchor disables validation at and below Domain until ExpiresAt.
type NegativeTrustAnchor struct {
	ID                        uuid.UUID
	Domain, Reason, CreatedBy string
	ExpiresAt, CreatedAt      time.Time
}

// RPZZone is one response policy zone, loaded from an uploaded file or by zone transfer.
type RPZZone struct {
	ID                                         uuid.UUID
	Name                                       string
	Position                                   int32
	SourceType                                 string // file | transfer
	BlobSHA256                                 *string
	BlobSize                                   int64
	FileRecords                                *int32
	PrimaryAddress, TSIGKeyName, TSIGAlgorithm *string
	TSIGSecretEnvelope                         []byte // NXE1; nil on update keeps the stored envelope
	MinRefreshSeconds                          int32
	PolicyOverride                             string
	RefreshNonce, Revision                     int64
}

// RPZEngineStatus is one engine's report for one RPZ zone.
type RPZEngineStatus struct {
	EngineID                       uuid.UUID
	EngineName                     string
	Serial, Records, Skipped, Hits int64
	LastSuccessAt                  *time.Time
	LastError                      string
	Stale                          bool
}

// EngineDnssecStatus is one engine's latest DnssecStats as protojson.
type EngineDnssecStatus struct {
	EngineID   uuid.UUID
	EngineName string
	Stats      []byte
	ReportedAt time.Time
}

// ResolutionRows is everything the snapshot builder needs for the M3 sections.
type ResolutionRows struct {
	Resolution   ResolutionSettings
	ForwardZones []ForwardZone
	Dnssec       DnssecSettings
	Anchors      []TrustAnchor
	NTAs         []NegativeTrustAnchor
	RPZ          []RPZZone
}

// GetResolutionSettings returns the singleton resolution settings.
func GetResolutionSettings(ctx context.Context, q PolicyQuerier) (ResolutionSettings, error) {
	var s ResolutionSettings
	err := q.QueryRow(ctx, `select mode, qname_minimisation, aggressive_nsec, max_upstream_queries, max_delegation_depth,
		authority_port, root_hints, revision from resolution_settings`).
		Scan(&s.Mode, &s.QnameMinimisation, &s.AggressiveNSEC, &s.MaxUpstreamQueries, &s.MaxDelegationDepth,
			&s.AuthorityPort, &s.RootHints, &s.Revision)
	if s.RootHints == nil {
		s.RootHints = []RootHint{}
	}
	return s, MapError(err)
}

// UpdateResolutionSettings stores s using s.Revision as the expected revision.
func UpdateResolutionSettings(ctx context.Context, tx pgx.Tx, s ResolutionSettings) (ResolutionSettings, error) {
	if s.RootHints == nil {
		s.RootHints = []RootHint{}
	}
	var rev int64
	err := tx.QueryRow(ctx, `update resolution_settings set mode = $1, qname_minimisation = $2, aggressive_nsec = $3,
		max_upstream_queries = $4, max_delegation_depth = $5, authority_port = $6, root_hints = $7,
		revision = revision + 1, updated_at = now() where revision = $8 returning revision`,
		s.Mode, s.QnameMinimisation, s.AggressiveNSEC, s.MaxUpstreamQueries, s.MaxDelegationDepth, s.AuthorityPort,
		s.RootHints, s.Revision).Scan(&rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return ResolutionSettings{}, fmt.Errorf("%w: resolution settings revision %d is stale; reload and retry", ErrConflict, s.Revision)
	}
	if err != nil {
		return ResolutionSettings{}, err
	}
	return GetResolutionSettings(ctx, tx)
}

const selectForwardZones = "select id, domain, addresses, validate, revision from forward_zones"

func scanForwardZone(row pgx.Row) (ForwardZone, error) {
	var z ForwardZone
	err := row.Scan(&z.ID, &z.Domain, &z.Addresses, &z.Validate, &z.Revision)
	return z, err
}

// ListForwardZones returns every forward zone ordered by domain.
func ListForwardZones(ctx context.Context, q PolicyQuerier) ([]ForwardZone, error) {
	return collect(ctx, q, selectForwardZones+" order by domain", scanForwardZone)
}

// GetForwardZone returns one forward zone or ErrNotFound.
func GetForwardZone(ctx context.Context, q PolicyQuerier, id uuid.UUID) (ForwardZone, error) {
	z, err := scanForwardZone(q.QueryRow(ctx, selectForwardZones+" where id = $1", id))
	return z, MapError(err)
}

// CreateForwardZone inserts z (a duplicate domain is ErrConflict).
func CreateForwardZone(ctx context.Context, tx pgx.Tx, z ForwardZone) (ForwardZone, error) {
	created, err := scanForwardZone(tx.QueryRow(ctx, `insert into forward_zones(domain, addresses, validate) values ($1, $2, $3)
		returning id, domain, addresses, validate, revision`, z.Domain, z.Addresses, z.Validate))
	return created, MapError(err)
}

// UpdateForwardZone replaces z using z.Revision as the expected revision.
func UpdateForwardZone(ctx context.Context, tx pgx.Tx, z ForwardZone) (ForwardZone, error) {
	updated, err := scanForwardZone(tx.QueryRow(ctx, `update forward_zones set domain = $2, addresses = $3, validate = $4,
		revision = revision + 1, updated_at = now() where id = $1 and revision = $5
		returning id, domain, addresses, validate, revision`, z.ID, z.Domain, z.Addresses, z.Validate, z.Revision))
	if errors.Is(err, pgx.ErrNoRows) {
		return ForwardZone{}, missingOrStale(ctx, tx, "forward_zones", z.ID)
	}
	return updated, MapError(err)
}

// DeleteForwardZone deletes the zone at the expected revision.
func DeleteForwardZone(ctx context.Context, tx pgx.Tx, id uuid.UUID, revision int64) error {
	return deleteAtRevision(ctx, tx, "forward_zones", id, revision)
}

// GetDnssecSettings returns the singleton DNSSEC settings.
func GetDnssecSettings(ctx context.Context, q PolicyQuerier) (DnssecSettings, error) {
	var s DnssecSettings
	err := q.QueryRow(ctx, "select validation, validate_forwarded, rfc5011, revision from dnssec_settings").
		Scan(&s.Validation, &s.ValidateForwarded, &s.RFC5011, &s.Revision)
	return s, MapError(err)
}

// UpdateDnssecSettings stores s using s.Revision as the expected revision.
func UpdateDnssecSettings(ctx context.Context, tx pgx.Tx, s DnssecSettings) (DnssecSettings, error) {
	err := tx.QueryRow(ctx, `update dnssec_settings set validation = $1, validate_forwarded = $2, rfc5011 = $3,
		revision = revision + 1, updated_at = now() where revision = $4 returning revision`,
		s.Validation, s.ValidateForwarded, s.RFC5011, s.Revision).Scan(&s.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return DnssecSettings{}, fmt.Errorf("%w: dnssec settings revision %d is stale; reload and retry", ErrConflict, s.Revision)
	}
	return s, err
}

const selectTrustAnchors = "select id, zone, ds, source, created_at from trust_anchors"

func scanTrustAnchor(row pgx.Row) (TrustAnchor, error) {
	var a TrustAnchor
	err := row.Scan(&a.ID, &a.Zone, &a.DS, &a.Source, &a.CreatedAt)
	return a, err
}

// ListTrustAnchors returns every trust anchor ordered by zone and DS.
func ListTrustAnchors(ctx context.Context, q PolicyQuerier) ([]TrustAnchor, error) {
	return collect(ctx, q, selectTrustAnchors+" order by zone, ds", scanTrustAnchor)
}

// CreateTrustAnchor inserts an operator anchor (a duplicate is ErrConflict).
func CreateTrustAnchor(ctx context.Context, tx pgx.Tx, zone, ds string) (TrustAnchor, error) {
	a, err := scanTrustAnchor(tx.QueryRow(ctx, `insert into trust_anchors(zone, ds, source) values ($1, $2, 'operator')
		returning id, zone, ds, source, created_at`, zone, ds))
	return a, MapError(err)
}

// DeleteTrustAnchor deletes an anchor and returns it; the last root anchor is ErrLastRootAnchor.
func DeleteTrustAnchor(ctx context.Context, tx pgx.Tx, id uuid.UUID) (TrustAnchor, error) {
	a, err := scanTrustAnchor(tx.QueryRow(ctx, selectTrustAnchors+" where id = $1 for update", id))
	if err != nil {
		return TrustAnchor{}, MapError(err)
	}
	if a.Zone == "." {
		var roots int
		if err := tx.QueryRow(ctx, "select count(*) from (select 1 from trust_anchors where zone = '.' for update) r").Scan(&roots); err != nil {
			return TrustAnchor{}, err
		}
		if roots <= 1 {
			return TrustAnchor{}, ErrLastRootAnchor
		}
	}
	_, err = tx.Exec(ctx, "delete from trust_anchors where id = $1", id)
	return a, err
}

const selectNTAs = "select id, domain, reason, created_by, expires_at, created_at from negative_trust_anchors"

func scanNTA(row pgx.Row) (NegativeTrustAnchor, error) {
	var n NegativeTrustAnchor
	err := row.Scan(&n.ID, &n.Domain, &n.Reason, &n.CreatedBy, &n.ExpiresAt, &n.CreatedAt)
	return n, err
}

// ListNegativeTrustAnchors returns every NTA ordered by domain.
func ListNegativeTrustAnchors(ctx context.Context, q PolicyQuerier) ([]NegativeTrustAnchor, error) {
	return collect(ctx, q, selectNTAs+" order by domain", scanNTA)
}

// CreateNegativeTrustAnchor inserts n (a duplicate domain is ErrConflict).
func CreateNegativeTrustAnchor(ctx context.Context, tx pgx.Tx, n NegativeTrustAnchor) (NegativeTrustAnchor, error) {
	created, err := scanNTA(tx.QueryRow(ctx, `insert into negative_trust_anchors(domain, reason, expires_at, created_by)
		values ($1, $2, $3, $4) returning id, domain, reason, created_by, expires_at, created_at`,
		n.Domain, n.Reason, n.ExpiresAt, n.CreatedBy))
	return created, MapError(err)
}

// DeleteNegativeTrustAnchor deletes an NTA and returns it.
func DeleteNegativeTrustAnchor(ctx context.Context, tx pgx.Tx, id uuid.UUID) (NegativeTrustAnchor, error) {
	n, err := scanNTA(tx.QueryRow(ctx, `delete from negative_trust_anchors where id = $1
		returning id, domain, reason, created_by, expires_at, created_at`, id))
	return n, MapError(err)
}

// DeleteExpiredNegativeTrustAnchors deletes every NTA expired at now.
func DeleteExpiredNegativeTrustAnchors(ctx context.Context, tx pgx.Tx, now time.Time) (int64, error) {
	tag, err := tx.Exec(ctx, "delete from negative_trust_anchors where expires_at <= $1", now)
	return tag.RowsAffected(), err
}

const selectRPZZones = `select z.id, z.name, z.position, z.source_type, z.blob_sha256, coalesce(b.size, 0), z.file_records,
	z.primary_address, z.tsig_key_name, z.tsig_algorithm, z.tsig_secret_envelope, z.min_refresh_seconds,
	z.policy_override, z.refresh_nonce, z.revision
	from rpz_zones z left join blobs b on b.sha256 = z.blob_sha256`

func scanRPZZone(row pgx.Row) (RPZZone, error) {
	var z RPZZone
	err := row.Scan(&z.ID, &z.Name, &z.Position, &z.SourceType, &z.BlobSHA256, &z.BlobSize, &z.FileRecords,
		&z.PrimaryAddress, &z.TSIGKeyName, &z.TSIGAlgorithm, &z.TSIGSecretEnvelope, &z.MinRefreshSeconds,
		&z.PolicyOverride, &z.RefreshNonce, &z.Revision)
	return z, err
}

// ListRPZZones returns every RPZ zone in policy order.
func ListRPZZones(ctx context.Context, q PolicyQuerier) ([]RPZZone, error) {
	return collect(ctx, q, selectRPZZones+" order by z.position", scanRPZZone)
}

// GetRPZZone returns one RPZ zone or ErrNotFound.
func GetRPZZone(ctx context.Context, q PolicyQuerier, id uuid.UUID) (RPZZone, error) {
	z, err := scanRPZZone(q.QueryRow(ctx, selectRPZZones+" where z.id = $1", id))
	return z, MapError(err)
}

// CreateRPZZone inserts z last in policy order, using z.ID when set.
func CreateRPZZone(ctx context.Context, tx pgx.Tx, z RPZZone) (RPZZone, error) {
	if z.ID == uuid.Nil {
		z.ID = uuid.New()
	}
	_, err := tx.Exec(ctx, `insert into rpz_zones(id, name, position, source_type, primary_address, tsig_key_name,
		tsig_algorithm, tsig_secret_envelope, min_refresh_seconds, policy_override)
		values ($1, $2, (select coalesce(max(position), 0) + 1 from rpz_zones), $3, $4, $5, $6, $7, $8, $9)`,
		z.ID, z.Name, z.SourceType, z.PrimaryAddress, z.TSIGKeyName, z.TSIGAlgorithm, z.TSIGSecretEnvelope,
		z.MinRefreshSeconds, z.PolicyOverride)
	if err != nil {
		return RPZZone{}, MapError(err)
	}
	return GetRPZZone(ctx, tx, z.ID)
}

// UpdateRPZZone replaces the editable fields of z using z.Revision as the expected revision. The
// source type never changes; a nil envelope keeps the stored one unless the algorithm is cleared.
func UpdateRPZZone(ctx context.Context, tx pgx.Tx, z RPZZone) (RPZZone, error) {
	tag, err := tx.Exec(ctx, `update rpz_zones set primary_address = $2, tsig_key_name = $3, tsig_algorithm = $4,
		tsig_secret_envelope = case when $4::text is null then null else coalesce($5, tsig_secret_envelope) end,
		min_refresh_seconds = $6, policy_override = $7, revision = revision + 1, updated_at = now()
		where id = $1 and revision = $8`,
		z.ID, z.PrimaryAddress, z.TSIGKeyName, z.TSIGAlgorithm, z.TSIGSecretEnvelope, z.MinRefreshSeconds, z.PolicyOverride, z.Revision)
	if err != nil {
		return RPZZone{}, MapError(err)
	}
	if tag.RowsAffected() == 0 {
		return RPZZone{}, missingOrStale(ctx, tx, "rpz_zones", z.ID)
	}
	return GetRPZZone(ctx, tx, z.ID)
}

// DeleteRPZZone deletes the zone at the expected revision.
func DeleteRPZZone(ctx context.Context, tx pgx.Tx, id uuid.UUID, revision int64) error {
	return deleteAtRevision(ctx, tx, "rpz_zones", id, revision)
}

// SetRPZZoneFile points a file zone at an uploaded blob at the expected revision.
func SetRPZZoneFile(ctx context.Context, tx pgx.Tx, id uuid.UUID, revision int64, sha256 string, records int32) (RPZZone, error) {
	tag, err := tx.Exec(ctx, `update rpz_zones set blob_sha256 = $2, file_records = $3, revision = revision + 1,
		updated_at = now() where id = $1 and revision = $4`, id, sha256, records, revision)
	if err != nil {
		return RPZZone{}, MapError(err)
	}
	if tag.RowsAffected() == 0 {
		return RPZZone{}, missingOrStale(ctx, tx, "rpz_zones", id)
	}
	return GetRPZZone(ctx, tx, id)
}

// BumpRPZRefreshNonce asks engines to refresh the zone now. The revision is unchanged: a refresh
// is not an edit.
func BumpRPZRefreshNonce(ctx context.Context, tx pgx.Tx, id uuid.UUID) (RPZZone, error) {
	tag, err := tx.Exec(ctx, "update rpz_zones set refresh_nonce = refresh_nonce + 1, updated_at = now() where id = $1", id)
	if err != nil {
		return RPZZone{}, MapError(err)
	}
	if tag.RowsAffected() == 0 {
		return RPZZone{}, ErrNotFound
	}
	return GetRPZZone(ctx, tx, id)
}

// ReorderRPZZones sets policy order to ids, which must list every zone exactly once.
func ReorderRPZZones(ctx context.Context, tx pgx.Tx, ids []uuid.UUID) error {
	rows, err := tx.Query(ctx, "select id from rpz_zones order by id for update")
	if err != nil {
		return MapError(err)
	}
	current, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return MapError(err)
	}
	want := slices.Clone(ids)
	slices.SortFunc(want, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	slices.SortFunc(current, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	if !slices.Equal(want, current) {
		return fmt.Errorf("%w: ids must list every RPZ zone exactly once", ErrConflict)
	}
	// Negate first so the unique position index never sees two zones at one position.
	if _, err := tx.Exec(ctx, "update rpz_zones set position = -position"); err != nil {
		return err
	}
	for i, id := range ids {
		if _, err := tx.Exec(ctx, "update rpz_zones set position = $2 where id = $1", id, i+1); err != nil {
			return err
		}
	}
	return nil
}

// ListRPZEngineStatus returns the status reports of live engines keyed by zone id.
func ListRPZEngineStatus(ctx context.Context, q PolicyQuerier) (map[uuid.UUID][]RPZEngineStatus, error) {
	rows, err := q.Query(ctx, `select s.rpz_zone_id, s.engine_id, e.node_name, s.serial, s.records, s.skipped, s.hits,
		s.last_success_at, s.last_error, s.stale from engine_rpz_status s join engines e on e.id = s.engine_id
		where e.deleted_at is null order by e.node_name, s.engine_id`)
	if err != nil {
		return nil, MapError(err)
	}
	defer rows.Close()
	out := map[uuid.UUID][]RPZEngineStatus{}
	for rows.Next() {
		var zone uuid.UUID
		var s RPZEngineStatus
		if err := rows.Scan(&zone, &s.EngineID, &s.EngineName, &s.Serial, &s.Records, &s.Skipped, &s.Hits,
			&s.LastSuccessAt, &s.LastError, &s.Stale); err != nil {
			return nil, MapError(err)
		}
		out[zone] = append(out[zone], s)
	}
	return out, MapError(rows.Err())
}

// ListEngineDnssecStatus returns the latest DNSSEC report of every live engine.
func ListEngineDnssecStatus(ctx context.Context, q PolicyQuerier) ([]EngineDnssecStatus, error) {
	return collect(ctx, q, `select s.engine_id, e.node_name, s.stats, s.reported_at from engine_dnssec_status s
		join engines e on e.id = s.engine_id where e.deleted_at is null order by e.node_name, s.engine_id`,
		func(row pgx.Row) (EngineDnssecStatus, error) {
			var s EngineDnssecStatus
			err := row.Scan(&s.EngineID, &s.EngineName, &s.Stats, &s.ReportedAt)
			return s, err
		})
}

// LoadResolution reads every M3 configuration table.
func LoadResolution(ctx context.Context, q PolicyQuerier) (ResolutionRows, error) {
	var r ResolutionRows
	var err error
	if r.Resolution, err = GetResolutionSettings(ctx, q); err != nil {
		return r, err
	}
	if r.ForwardZones, err = ListForwardZones(ctx, q); err != nil {
		return r, err
	}
	if r.Dnssec, err = GetDnssecSettings(ctx, q); err != nil {
		return r, err
	}
	if r.Anchors, err = ListTrustAnchors(ctx, q); err != nil {
		return r, err
	}
	if r.NTAs, err = ListNegativeTrustAnchors(ctx, q); err != nil {
		return r, err
	}
	r.RPZ, err = ListRPZZones(ctx, q)
	return r, err
}

func collect[T any](ctx context.Context, q PolicyQuerier, sql string, scan func(pgx.Row) (T, error)) ([]T, error) {
	rows, err := q.Query(ctx, sql)
	if err != nil {
		return nil, MapError(err)
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, MapError(err)
		}
		out = append(out, v)
	}
	return out, MapError(rows.Err())
}

func deleteAtRevision(ctx context.Context, tx pgx.Tx, table string, id uuid.UUID, revision int64) error {
	tag, err := tx.Exec(ctx, "delete from "+table+" where id = $1 and revision = $2", id, revision)
	if err != nil {
		return MapError(err)
	}
	if tag.RowsAffected() == 0 {
		return missingOrStale(ctx, tx, table, id)
	}
	return nil
}
