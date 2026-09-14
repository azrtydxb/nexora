// Package snapshot builds ConfigSnapshots from the database and publishes new config versions.
package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/rollout"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// NotifyChannel is the pg_notify channel carrying each new config version.
const NotifyChannel = "nexora_config"

// BuildConfig carries the instance-level inputs of a snapshot.
type BuildConfig struct {
	QueryLogToManagement bool
	DefaultOTLPEndpoint  string
}

// Querier is satisfied by *pgxpool.Pool, *pgx.Conn and pgx.Tx.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Mutate runs fn and publishes the resulting configuration in one transaction:
// change rows -> audit row -> config_versions -> per engine group: snapshot and rollout -> pg_notify.
func Mutate(ctx context.Context, st *store.Store, cfg BuildConfig, a auth.Actor, fn func(tx pgx.Tx) (auth.Change, error)) (uint64, error) {
	var version uint64
	err := st.InTx(ctx, func(tx pgx.Tx) error {
		change, err := fn(tx)
		if err != nil {
			return err
		}
		version, err = publish(ctx, tx, cfg, a, change)
		return err
	})
	return version, err
}

// EnsureInitial publishes version 1 when no config version exists yet and returns the latest version.
func EnsureInitial(ctx context.Context, st *store.Store, cfg BuildConfig) (uint64, error) {
	var version uint64
	err := st.InTx(ctx, func(tx pgx.Tx) error {
		if err := lockVersions(ctx, tx); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "select coalesce(max(version), 0) from config_versions").Scan(&version); err != nil {
			return err
		}
		if version > 0 {
			return nil
		}
		var err error
		version, err = publish(ctx, tx, cfg, auth.Actor{Type: "system", ID: "bootstrap", Name: "system"},
			auth.Change{Action: "initialSnapshot", TargetType: "config", TargetID: "1"})
		return err
	})
	return version, err
}

// ErrUnknownVersion is returned by Republish when the engine group has no snapshot of the version.
var ErrUnknownVersion = errors.New("version not found for this engine group")

// PublishRaw stores snap unvalidated as the next version of every engine group, rolled out at
// once, and notifies. Tests use it to push snapshots the builder would never produce; no
// production code path calls it.
func PublishRaw(ctx context.Context, st *store.Store, snap *controlv1.ConfigSnapshot, createdBy string) (uint64, error) {
	var version uint64
	err := st.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		if version, err = nextVersion(ctx, tx); err != nil {
			return err
		}
		snap.Version = version
		if err := insertVersion(ctx, tx, version, createdBy, "raw snapshot"); err != nil {
			return err
		}
		groups, err := engineGroupIDs(ctx, tx)
		if err != nil {
			return err
		}
		for _, g := range groups {
			if err := insertGroupSnapshot(ctx, tx, version, g, snap); err != nil {
				return err
			}
			if _, _, err := rollout.Create(ctx, tx, rollout.CreateParams{EngineGroupID: g, Version: version, Kind: rollout.KindChange,
				Immediate: true, Actor: createdBy}); err != nil {
				return err
			}
		}
		return nil
	})
	return version, err
}

// Latest returns the default engine group's newest snapshot (store.ErrNotFound when none exists).
func Latest(ctx context.Context, q Querier) (uint64, *controlv1.ConfigSnapshot, error) {
	var version int64
	var raw []byte
	if err := q.QueryRow(ctx, `select version, snapshot from group_snapshots where engine_group_id = $1
		order by version desc limit 1`, store.DefaultEngineGroupID).Scan(&version, &raw); err != nil {
		return 0, nil, store.MapError(err)
	}
	snap := &controlv1.ConfigSnapshot{}
	if err := proto.Unmarshal(raw, snap); err != nil {
		return 0, nil, fmt.Errorf("config version %d: %w", version, err)
	}
	return uint64(version), snap, nil
}

// ContentDigest is the lowercase hex SHA-256 of snap's deterministic encoding with version and
// creation time zeroed: equal digests mean an engine would serve the same configuration.
func ContentDigest(snap *controlv1.ConfigSnapshot) (string, error) {
	c := proto.Clone(snap).(*controlv1.ConfigSnapshot)
	c.Version, c.CreatedUnixMs = 0, 0
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Republish copies engine group groupID's snapshot of fromVersion into a new version (re-encoded
// with the new number) and rolls it out to that group at once. Other groups get no snapshot of the
// new version, so their targets do not change.
func Republish(ctx context.Context, tx pgx.Tx, a auth.Actor, groupID uuid.UUID, fromVersion uint64, kind rollout.Kind) (uint64, uuid.UUID, error) {
	version, err := nextVersion(ctx, tx)
	if err != nil {
		return 0, uuid.Nil, err
	}
	var raw []byte
	if err := tx.QueryRow(ctx, "select snapshot from group_snapshots where version = $1 and engine_group_id = $2",
		int64(fromVersion), groupID).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, uuid.Nil, ErrUnknownVersion
		}
		return 0, uuid.Nil, err
	}
	snap := &controlv1.ConfigSnapshot{}
	if err := proto.Unmarshal(raw, snap); err != nil {
		return 0, uuid.Nil, fmt.Errorf("engine group %s version %d: %w", groupID, fromVersion, err)
	}
	change := auth.Change{Action: string(kind) + "EngineGroup", TargetType: "engine_group", TargetID: groupID.String(),
		After: map[string]uint64{"from_version": fromVersion}}
	if err := auth.WriteAudit(ctx, tx, a, change, &version); err != nil {
		return 0, uuid.Nil, err
	}
	if err := insertVersion(ctx, tx, version, a.Name, fmt.Sprintf("%s engine group %s to %d", kind, groupID, fromVersion)); err != nil {
		return 0, uuid.Nil, err
	}
	snap.Version, snap.CreatedUnixMs = version, time.Now().UnixMilli()
	if err := insertGroupSnapshot(ctx, tx, version, groupID, snap); err != nil {
		return 0, uuid.Nil, err
	}
	id, _, err := rollout.Create(ctx, tx, rollout.CreateParams{EngineGroupID: groupID, Version: version, FromVersion: &fromVersion,
		Kind: kind, Immediate: true, Actor: a.Name})
	return version, id, err
}

// publish writes the next version: the audit row, the config_versions row, and for every engine
// group its snapshot and rollout. A group whose content equals its stable version's rolls out at
// once; any other change follows the group's strategy.
func publish(ctx context.Context, tx pgx.Tx, cfg BuildConfig, a auth.Actor, change auth.Change) (uint64, error) {
	version, err := nextVersion(ctx, tx)
	if err != nil {
		return 0, err
	}
	if err := auth.WriteAudit(ctx, tx, a, change, &version); err != nil {
		return 0, err
	}
	summary := strings.TrimSpace(change.Action + " " + change.TargetType + " " + change.TargetID)
	if err := insertVersion(ctx, tx, version, a.Name, summary); err != nil {
		return 0, err
	}
	groups, err := engineGroupIDs(ctx, tx)
	if err != nil {
		return 0, err
	}
	for _, g := range groups {
		snap, err := BuildForGroup(ctx, tx, version, cfg, g)
		if err != nil {
			return 0, err
		}
		if err := insertGroupSnapshot(ctx, tx, version, g, snap); err != nil {
			return 0, err
		}
		var immediate bool
		if err := tx.QueryRow(ctx, `select coalesce((select gs.content_sha256 from engine_groups g
			join group_snapshots gs on gs.engine_group_id = g.id and gs.version = g.stable_version where g.id = $1)
			= (select content_sha256 from group_snapshots where engine_group_id = $1 and version = $2), false)`,
			g, int64(version)).Scan(&immediate); err != nil {
			return 0, err
		}
		if _, _, err := rollout.Create(ctx, tx, rollout.CreateParams{EngineGroupID: g, Version: version, Kind: rollout.KindChange,
			Immediate: immediate, Actor: a.Name}); err != nil {
			return 0, err
		}
	}
	return version, nil
}

// engineGroupIDs lists every engine group, the default group first.
func engineGroupIDs(ctx context.Context, tx pgx.Tx) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, "select id from engine_groups order by (id <> $1), name", store.DefaultEngineGroupID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

// insertGroupSnapshot stores snap (whose Version must be version) as the group's snapshot.
func insertGroupSnapshot(ctx context.Context, tx pgx.Tx, version uint64, groupID uuid.UUID, snap *controlv1.ConfigSnapshot) error {
	digest, err := ContentDigest(snap)
	if err != nil {
		return err
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(snap)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "insert into group_snapshots (version, engine_group_id, snapshot, content_sha256) values ($1, $2, $3, $4)",
		int64(version), groupID, raw, digest)
	return err
}

func lockVersions(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtext('nexora:config_version'))")
	return err
}

func nextVersion(ctx context.Context, tx pgx.Tx) (uint64, error) {
	if err := lockVersions(ctx, tx); err != nil {
		return 0, err
	}
	var version uint64
	err := tx.QueryRow(ctx, "select coalesce(max(version), 0) + 1 from config_versions").Scan(&version)
	return version, err
}

// insertVersion inserts the config_versions row (snapshots live in group_snapshots) and notifies.
func insertVersion(ctx context.Context, tx pgx.Tx, version uint64, createdBy, summary string) error {
	if _, err := tx.Exec(ctx, "insert into config_versions(version, created_by, summary) values ($1, $2, $3)",
		version, createdBy, summary); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "select pg_notify($1, $2)", NotifyChannel, strconv.FormatUint(version, 10))
	return err
}

// Build is BuildForGroup for the default engine group.
func Build(ctx context.Context, tx pgx.Tx, version uint64, cfg BuildConfig) (*controlv1.ConfigSnapshot, error) {
	return BuildForGroup(ctx, tx, version, cfg, store.DefaultEngineGroupID)
}

// BuildForGroup reads the configuration of engine group groupID (the global rows plus the group's)
// inside tx and returns it as snapshot version `version`. A non-empty allowlist is stored as a
// blob as a side effect.
func BuildForGroup(ctx context.Context, tx pgx.Tx, version uint64, cfg BuildConfig, groupID uuid.UUID) (*controlv1.ConfigSnapshot, error) {
	var upstreamMode, groupOTLP string
	var extraACL []string
	var indexMaxBytes int64
	if err := tx.QueryRow(ctx, `select upstream_mode,
		array(select host(c) || '/' || masklen(c) from unnest(extra_acl_cidrs) with ordinality as u(c, n) order by n), otlp_endpoint,
		filter_index_max_bytes
		from engine_groups where id = $1`, groupID).Scan(&upstreamMode, &extraACL, &groupOTLP, &indexMaxBytes); err != nil {
		return nil, fmt.Errorf("engine group %s: %w", groupID, err)
	}
	snap := &controlv1.ConfigSnapshot{
		Version:       version,
		CreatedUnixMs: time.Now().UnixMilli(),
		Resolver:      &controlv1.ResolverConfig{},
		Cache:         &controlv1.CacheConfig{},
		Filter:        &controlv1.FilterConfig{},
		Telemetry:     &controlv1.TelemetryConfig{QuerylogToManagement: cfg.QueryLogToManagement},

		FilterIndexMaxBytes: uint64(indexMaxBytes),
	}
	var strategy, blockMode, otlp string
	var maxBytes int64
	var minTTL, maxTTL, negTTL, stale, blockTTL, sample, slow int32
	if err := tx.QueryRow(ctx, `select strategy, cache_max_bytes, cache_min_ttl, cache_max_ttl, cache_negative_max_ttl,
		cache_stale_window, block_mode, block_ttl, otlp_endpoint, trace_sample_one_in, trace_slow_threshold_us
		from resolver_settings`).Scan(&strategy, &maxBytes, &minTTL, &maxTTL, &negTTL, &stale, &blockMode, &blockTTL, &otlp, &sample, &slow); err != nil {
		return nil, fmt.Errorf("resolver settings: %w", err)
	}
	var ok bool
	if snap.Resolver.Strategy, ok = strategies[strategy]; !ok {
		return nil, fmt.Errorf("unknown strategy %q", strategy)
	}
	if snap.Filter.BlockMode, ok = blockModes[blockMode]; !ok {
		return nil, fmt.Errorf("unknown block mode %q", blockMode)
	}
	snap.Cache.MaxBytes, snap.Cache.MinTtl, snap.Cache.MaxTtl = uint64(maxBytes), uint32(minTTL), uint32(maxTTL)
	snap.Cache.NegativeMaxTtl, snap.Cache.StaleWindow = uint32(negTTL), uint32(stale)
	snap.Filter.BlockTtl = uint32(blockTTL)
	if groupOTLP != "" {
		otlp = groupOTLP
	}
	snap.Telemetry.OtlpEndpoint = otlp
	if otlp == "" {
		snap.Telemetry.OtlpEndpoint = cfg.DefaultOTLPEndpoint
	}
	snap.Telemetry.TraceSampleOneIn, snap.Telemetry.TraceSlowThresholdUs = uint32(sample), uint32(slow)

	if err := buildUpstreams(ctx, tx, snap, groupID, upstreamMode); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `select array(select host(c) || '/' || masklen(c)
		from access_control, unnest(allow_cidrs) with ordinality as u(c, n) order by n)`).Scan(&snap.AclAllowCidrs); err != nil {
		return nil, fmt.Errorf("access control: %w", err)
	}
	snap.AclAllowCidrs = append(snap.AclAllowCidrs, extraACL...)
	if err := tx.QueryRow(ctx, `select array(select host(c) || '/' || masklen(c)
		from access_control, unnest(authoritative_allow_cidrs) with ordinality as u(c, n) order by n)`).Scan(&snap.AuthoritativeAllowCidrs); err != nil {
		return nil, fmt.Errorf("authoritative access control: %w", err)
	}
	snap.AuthoritativeAclSet = true
	if err := buildFilterLists(ctx, tx, snap, groupID); err != nil {
		return nil, err
	}
	var domains []string
	if err := tx.QueryRow(ctx, "select domains from allowlist").Scan(&domains); err != nil {
		return nil, fmt.Errorf("allowlist: %w", err)
	}
	if len(domains) > 0 {
		ref, err := storeAllowlist(ctx, tx, domains)
		if err != nil {
			return nil, err
		}
		snap.Filter.Allowlists = append(snap.Filter.Allowlists, ref)
		snap.Filter.AllowlistRefs = append(snap.Filter.AllowlistRefs, &controlv1.FilterListRef{ListId: "allowlist", Position: CustomListPosition + 999_999, Blob: ref})
	}
	if err := buildPolicy(ctx, tx, snap, groupID); err != nil {
		return nil, err
	}
	rows, err := store.LoadResolution(ctx, tx, groupID)
	if err != nil {
		return nil, fmt.Errorf("resolution: %w", err)
	}
	ApplyResolution(snap, rows, time.Now())
	if err := AddAuthZones(ctx, tx, snap, groupID); err != nil {
		return nil, err
	}
	return snap, nil
}

// buildPolicy loads the policy groups of engine group groupID (global or its own), their rewrites
// and the global rewrites of the group, global safe search and the current blob of every fetched
// blocklist (enabled or not, since groups may select disabled lists) in attribution order.
func buildPolicy(ctx context.Context, tx pgx.Tx, snap *controlv1.ConfigSnapshot, groupID uuid.UUID) error {
	all, err := store.ListPolicyGroups(ctx, tx)
	if err != nil {
		return fmt.Errorf("policy groups: %w", err)
	}
	inGroup := func(id *uuid.UUID) bool { return id == nil || *id == groupID }
	groups := all[:0:0]
	kept := map[uuid.UUID]bool{}
	for _, g := range all {
		if inGroup(g.EngineGroupID) {
			groups = append(groups, g)
			kept[g.ID] = true
		}
	}
	allRewrites, err := store.ListRewrites(ctx, tx, nil, true)
	if err != nil {
		return fmt.Errorf("rewrites: %w", err)
	}
	var rewrites []store.Rewrite
	for _, r := range allRewrites {
		if r.GroupID != nil && kept[*r.GroupID] || r.GroupID == nil && inGroup(r.EngineGroupID) {
			rewrites = append(rewrites, r)
		}
	}
	global, err := store.GetGlobalSafeSearch(ctx, tx)
	if err != nil {
		return fmt.Errorf("global safe search: %w", err)
	}
	rows, err := tx.Query(ctx, `select f.id, f.name, b.sha256, b.size, coalesce(f.category_key, ''),
			coalesce(f.catalog_position, 0), f.enabled, f.managed_by_catalog
		from filter_lists f join blobs b on b.sha256 = f.current_blob_sha256 where f.kind = 'block'
		order by f.managed_by_catalog desc, f.catalog_position, f.name`)
	if err != nil {
		return fmt.Errorf("policy blocklists: %w", err)
	}
	defer rows.Close()
	var lists []PolicyList
	custom := uint32(0)
	for rows.Next() {
		var size int64
		var pos int32
		l := PolicyList{Ref: &controlv1.BlobRef{}}
		if err := rows.Scan(&l.ID, &l.Ref.Name, &l.Ref.Sha256, &size, &l.CategoryKey, &pos, &l.Enabled, &l.Managed); err != nil {
			return fmt.Errorf("policy blocklists: %w", err)
		}
		l.Ref.Size, l.Position = uint64(size), uint32(pos)
		if !l.Managed {
			l.Position = CustomListPosition + custom
			custom++
		}
		lists = append(lists, l)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("policy blocklists: %w", err)
	}
	sec := BuildPolicySection(groups, lists, rewrites, global.SafeSearch)
	snap.PolicyGroups, snap.RewriteSets, snap.GlobalRewriteSetIds = sec.Groups, sec.RewriteSets, sec.GlobalRewriteSetIDs
	return nil
}

var (
	strategies = map[string]controlv1.UpstreamStrategy{
		"ordered": controlv1.UpstreamStrategy_UPSTREAM_STRATEGY_ORDERED,
		"fastest": controlv1.UpstreamStrategy_UPSTREAM_STRATEGY_FASTEST,
	}
	protocols = map[string]controlv1.UpstreamProtocol{
		"udp": controlv1.UpstreamProtocol_UPSTREAM_PROTOCOL_UDP,
		"tcp": controlv1.UpstreamProtocol_UPSTREAM_PROTOCOL_TCP,
		"dot": controlv1.UpstreamProtocol_UPSTREAM_PROTOCOL_DOT,
		"doh": controlv1.UpstreamProtocol_UPSTREAM_PROTOCOL_DOH,
	}
	blockModes = map[string]controlv1.BlockMode{
		"null_ip":  controlv1.BlockMode_BLOCK_MODE_NULL_IP,
		"nxdomain": controlv1.BlockMode_BLOCK_MODE_NXDOMAIN,
		"refused":  controlv1.BlockMode_BLOCK_MODE_REFUSED,
	}
)

// buildUpstreams lists the group's upstreams first, then (upstream mode inherit) the global ones.
func buildUpstreams(ctx context.Context, tx pgx.Tx, snap *controlv1.ConfigSnapshot, groupID uuid.UUID, mode string) error {
	rows, err := tx.Query(ctx, `select id::text, name, protocol, address, tls_server_name, doh_url, timeout_ms, ca_certificate_pem
		from upstreams where enabled and (engine_group_id = $1 or ($2 = 'inherit' and engine_group_id is null))
		order by (engine_group_id is null), position, name`, groupID, mode)
	if err != nil {
		return fmt.Errorf("upstreams: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		u := &controlv1.Upstream{}
		var protocol string
		var timeout int32
		if err := rows.Scan(&u.Id, &u.Name, &protocol, &u.Address, &u.TlsServerName, &u.DohUrl, &timeout, &u.CaCertificatePem); err != nil {
			return fmt.Errorf("upstreams: %w", err)
		}
		p, ok := protocols[protocol]
		if !ok {
			return fmt.Errorf("upstream %s: unknown protocol %q", u.Name, protocol)
		}
		u.Protocol, u.TimeoutMs = p, uint32(timeout)
		snap.Upstreams = append(snap.Upstreams, u)
	}
	return rows.Err()
}

// buildFilterLists adds the enabled lists of the global filter with their identity: catalog lists
// only while their category is enabled (in catalog order), then custom lists by name. Each blob
// goes to the M1 BlobRef fields too, so engines without FilterListRef keep filtering.
func buildFilterLists(ctx context.Context, tx pgx.Tx, snap *controlv1.ConfigSnapshot, groupID uuid.UUID) error {
	rows, err := tx.Query(ctx, `select f.id::text, f.name, f.kind, f.current_blob_sha256, b.size, coalesce(f.category_key, ''),
			coalesce(f.catalog_position, 0), f.managed_by_catalog
		from filter_lists f
		join blobs b on b.sha256 = f.current_blob_sha256
		left join filter_categories c on c.key = f.category_key
		where f.enabled and f.current_blob_sha256 is not null
			and (f.engine_group_id is null or f.engine_group_id = $1)
			and (not f.managed_by_catalog or c.enabled)
		order by f.managed_by_catalog desc, f.catalog_position, f.name`, groupID)
	if err != nil {
		return fmt.Errorf("filter lists: %w", err)
	}
	defer rows.Close()
	custom := uint32(0)
	for rows.Next() {
		ref := &controlv1.BlobRef{}
		lr := &controlv1.FilterListRef{Blob: ref}
		var kind string
		var size int64
		var pos int32
		var managed bool
		if err := rows.Scan(&lr.ListId, &ref.Name, &kind, &ref.Sha256, &size, &lr.Category, &pos, &managed); err != nil {
			return fmt.Errorf("filter lists: %w", err)
		}
		ref.Size, lr.Position = uint64(size), uint32(pos)
		if !managed {
			lr.Position = CustomListPosition + custom
			custom++
		}
		switch kind {
		case "block":
			snap.Filter.Blocklists = append(snap.Filter.Blocklists, ref)
			snap.Filter.BlocklistRefs = append(snap.Filter.BlocklistRefs, lr)
		case "allow":
			snap.Filter.Allowlists = append(snap.Filter.Allowlists, ref)
			snap.Filter.AllowlistRefs = append(snap.Filter.AllowlistRefs, lr)
		default:
			return errors.New("filter list " + ref.Name + ": unknown kind " + kind)
		}
	}
	return rows.Err()
}

// storeAllowlist normalises the domains (lowercase, no trailing dot, sorted, unique, one per
// line), zstd-compresses them deterministically and upserts the blob.
func storeAllowlist(ctx context.Context, tx pgx.Tx, domains []string) (*controlv1.BlobRef, error) {
	set := make(map[string]struct{}, len(domains))
	for _, d := range domains {
		if d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), "."); d != "" {
			set[d] = struct{}{}
		}
	}
	sorted := make([]string, 0, len(set))
	for d := range set {
		sorted = append(sorted, d)
	}
	sort.Strings(sorted)
	var text strings.Builder
	for _, d := range sorted {
		text.WriteString(d)
		text.WriteByte('\n')
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	data := enc.EncodeAll([]byte(text.String()), nil)
	_ = enc.Close()
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	if _, err := tx.Exec(ctx, "insert into blobs(sha256, size, data) values ($1, $2, $3) on conflict do nothing", sha, len(data), data); err != nil {
		return nil, fmt.Errorf("allowlist blob: %w", err)
	}
	return &controlv1.BlobRef{Sha256: sha, Size: uint64(len(data)), Name: "allowlist"}, nil
}
