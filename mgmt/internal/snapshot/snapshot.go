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
// change rows -> audit row -> build snapshot -> insert config_versions -> pg_notify.
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

// PublishRaw stores snap unvalidated as the next version and notifies. Tests use it to push
// snapshots the builder would never produce; no production code path calls it.
func PublishRaw(ctx context.Context, st *store.Store, snap *controlv1.ConfigSnapshot, createdBy string) (uint64, error) {
	var version uint64
	err := st.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		if version, err = nextVersion(ctx, tx); err != nil {
			return err
		}
		snap.Version = version
		raw, err := proto.Marshal(snap)
		if err != nil {
			return err
		}
		return insertVersion(ctx, tx, version, createdBy, "raw snapshot", raw)
	})
	return version, err
}

// Latest returns the newest config version and its snapshot (store.ErrNotFound when none exists).
func Latest(ctx context.Context, q Querier) (uint64, *controlv1.ConfigSnapshot, error) {
	var version int64
	var raw []byte
	if err := q.QueryRow(ctx, "select version, snapshot from config_versions order by version desc limit 1").Scan(&version, &raw); err != nil {
		return 0, nil, store.MapError(err)
	}
	snap := &controlv1.ConfigSnapshot{}
	if err := proto.Unmarshal(raw, snap); err != nil {
		return 0, nil, fmt.Errorf("config version %d: %w", version, err)
	}
	return uint64(version), snap, nil
}

func publish(ctx context.Context, tx pgx.Tx, cfg BuildConfig, a auth.Actor, change auth.Change) (uint64, error) {
	version, err := nextVersion(ctx, tx)
	if err != nil {
		return 0, err
	}
	if err := auth.WriteAudit(ctx, tx, a, change, &version); err != nil {
		return 0, err
	}
	snap, err := Build(ctx, tx, version, cfg)
	if err != nil {
		return 0, err
	}
	raw, err := proto.Marshal(snap)
	if err != nil {
		return 0, err
	}
	summary := strings.TrimSpace(change.Action + " " + change.TargetType + " " + change.TargetID)
	return version, insertVersion(ctx, tx, version, a.Name, summary, raw)
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

func insertVersion(ctx context.Context, tx pgx.Tx, version uint64, createdBy, summary string, raw []byte) error {
	if _, err := tx.Exec(ctx, "insert into config_versions(version, created_by, summary, snapshot) values ($1, $2, $3, $4)",
		version, createdBy, summary, raw); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "select pg_notify($1, $2)", NotifyChannel, strconv.FormatUint(version, 10))
	return err
}

// Build reads the whole configuration inside tx and returns it as snapshot version `version`.
// A non-empty allowlist is stored as a blob as a side effect.
func Build(ctx context.Context, tx pgx.Tx, version uint64, cfg BuildConfig) (*controlv1.ConfigSnapshot, error) {
	snap := &controlv1.ConfigSnapshot{
		Version:       version,
		CreatedUnixMs: time.Now().UnixMilli(),
		Resolver:      &controlv1.ResolverConfig{},
		Cache:         &controlv1.CacheConfig{},
		Filter:        &controlv1.FilterConfig{},
		Telemetry:     &controlv1.TelemetryConfig{QuerylogToManagement: cfg.QueryLogToManagement},
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
	snap.Telemetry.OtlpEndpoint = otlp
	if otlp == "" {
		snap.Telemetry.OtlpEndpoint = cfg.DefaultOTLPEndpoint
	}
	snap.Telemetry.TraceSampleOneIn, snap.Telemetry.TraceSlowThresholdUs = uint32(sample), uint32(slow)

	if err := buildUpstreams(ctx, tx, snap); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `select array(select host(c) || '/' || masklen(c)
		from access_control, unnest(allow_cidrs) with ordinality as u(c, n) order by n)`).Scan(&snap.AclAllowCidrs); err != nil {
		return nil, fmt.Errorf("access control: %w", err)
	}
	if err := buildFilterLists(ctx, tx, snap); err != nil {
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
	}
	if err := buildPolicy(ctx, tx, snap); err != nil {
		return nil, err
	}
	rows, err := store.LoadResolution(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("resolution: %w", err)
	}
	ApplyResolution(snap, rows, time.Now())
	return snap, nil
}

// buildPolicy loads policy groups, rewrites, global safe search and the current blob of every
// fetched blocklist (enabled or not, since groups may select disabled lists).
func buildPolicy(ctx context.Context, tx pgx.Tx, snap *controlv1.ConfigSnapshot) error {
	groups, err := store.ListPolicyGroups(ctx, tx)
	if err != nil {
		return fmt.Errorf("policy groups: %w", err)
	}
	rewrites, err := store.ListRewrites(ctx, tx, nil, true)
	if err != nil {
		return fmt.Errorf("rewrites: %w", err)
	}
	global, err := store.GetGlobalSafeSearch(ctx, tx)
	if err != nil {
		return fmt.Errorf("global safe search: %w", err)
	}
	rows, err := tx.Query(ctx, `select f.id, f.name, b.sha256, b.size from filter_lists f
		join blobs b on b.sha256 = f.current_blob_sha256 where f.kind = 'block'`)
	if err != nil {
		return fmt.Errorf("policy blocklists: %w", err)
	}
	defer rows.Close()
	listBlobs := map[uuid.UUID]*controlv1.BlobRef{}
	for rows.Next() {
		var id uuid.UUID
		var size int64
		ref := &controlv1.BlobRef{}
		if err := rows.Scan(&id, &ref.Name, &ref.Sha256, &size); err != nil {
			return fmt.Errorf("policy blocklists: %w", err)
		}
		ref.Size = uint64(size)
		listBlobs[id] = ref
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("policy blocklists: %w", err)
	}
	sec := BuildPolicySection(groups, listBlobs, rewrites, global.SafeSearch)
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

func buildUpstreams(ctx context.Context, tx pgx.Tx, snap *controlv1.ConfigSnapshot) error {
	rows, err := tx.Query(ctx, `select id::text, name, protocol, address, tls_server_name, doh_url, timeout_ms, ca_certificate_pem
		from upstreams where enabled order by position, name`)
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

func buildFilterLists(ctx context.Context, tx pgx.Tx, snap *controlv1.ConfigSnapshot) error {
	rows, err := tx.Query(ctx, `select f.name, f.kind, f.current_blob_sha256, b.size
		from filter_lists f join blobs b on b.sha256 = f.current_blob_sha256
		where f.enabled and f.current_blob_sha256 is not null order by f.name`)
	if err != nil {
		return fmt.Errorf("filter lists: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		ref := &controlv1.BlobRef{}
		var kind string
		var size int64
		if err := rows.Scan(&ref.Name, &kind, &ref.Sha256, &size); err != nil {
			return fmt.Errorf("filter lists: %w", err)
		}
		ref.Size = uint64(size)
		switch kind {
		case "block":
			snap.Filter.Blocklists = append(snap.Filter.Blocklists, ref)
		case "allow":
			snap.Filter.Allowlists = append(snap.Filter.Allowlists, ref)
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
