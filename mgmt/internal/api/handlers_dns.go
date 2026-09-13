package api

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

var domainRE = regexp.MustCompile(`^[a-z0-9_-]+(\.[a-z0-9_-]+)*$`)

// mutate publishes a config change made by the calling principal.
func (h *handlers) mutate(ctx context.Context, fn func(tx pgx.Tx) (auth.Change, error)) error {
	_, err := snapshot.Mutate(ctx, h.d.Store, h.d.Build, PrincipalFrom(ctx).Actor(), fn)
	return err
}

// checkRevision compares the stored revision with the one the client edited.
func checkRevision(stored, sent int64) error {
	if stored != sent {
		return fmt.Errorf("%w: revision %d is stale (current %d); reload and retry", store.ErrConflict, sent, stored)
	}
	return nil
}

func requireRevision(r *int64) (int64, error) {
	if r == nil {
		return 0, invalid("revision is required on update")
	}
	return *r, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

// ---- upstreams ----

const upstreamColumns = "id::text, name, protocol, address, tls_server_name, doh_url, timeout_ms, ca_certificate_pem, position, enabled, revision"

func scanUpstream(row pgx.Row) (Upstream, error) {
	var u Upstream
	var id, protocol string
	err := row.Scan(&id, &u.Name, &protocol, &u.Address, &u.TlsServerName, &u.DohUrl, &u.TimeoutMs, &u.CaCertificatePem, &u.Position, &u.Enabled, &u.Revision)
	if err != nil {
		return u, store.MapError(err)
	}
	u.Id, u.Protocol = uuid.MustParse(id), UpstreamProtocol(protocol)
	return u, nil
}

type upstreamFields struct {
	name, protocol, address, tlsName, dohURL, caPEM string
	timeout, position                               int
	enabled                                         bool
}

func validateUpstream(in UpstreamInput) (upstreamFields, error) {
	f := upstreamFields{name: strings.TrimSpace(in.Name), protocol: string(in.Protocol), address: deref(in.Address),
		tlsName: deref(in.TlsServerName), dohURL: deref(in.DohUrl), caPEM: deref(in.CaCertificatePem),
		timeout: in.TimeoutMs, position: in.Position, enabled: in.Enabled}
	if len(f.name) < 1 || len(f.name) > 64 {
		return f, invalid("name must be 1-64 characters")
	}
	if !in.Protocol.Valid() {
		return f, invalid("protocol must be udp, tcp, dot or doh")
	}
	if f.timeout < 50 || f.timeout > 5000 {
		return f, invalid("timeout_ms must be between 50 and 5000")
	}
	if f.position < 0 {
		return f, invalid("position must not be negative")
	}
	switch in.Protocol {
	case UpstreamInputProtocolUdp, UpstreamInputProtocolTcp, UpstreamInputProtocolDot:
		if _, err := netip.ParseAddrPort(f.address); err != nil {
			return f, invalid("address must be an IP:port, got %q", f.address)
		}
		if in.Protocol == UpstreamInputProtocolDot && f.tlsName == "" {
			return f, invalid("tls_server_name is required for DoT")
		}
	case UpstreamInputProtocolDoh:
		u, err := url.Parse(f.dohURL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return f, invalid("doh_url must be an https:// URL")
		}
	}
	if f.caPEM != "" {
		if err := validateCertificates(f.caPEM); err != nil {
			return f, err
		}
	}
	return f, nil
}

func validateCertificates(pemText string) error {
	rest := []byte(pemText)
	n := 0
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return invalid("ca_certificate_pem contains a %s block; only certificates are allowed", block.Type)
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return invalid("ca_certificate_pem: %v", err)
		}
		n++
	}
	if n == 0 || strings.TrimSpace(string(rest)) != "" {
		return invalid("ca_certificate_pem must contain only PEM certificates")
	}
	return nil
}

func (h *handlers) ListUpstreams(ctx context.Context, _ ListUpstreamsRequestObject) (ListUpstreamsResponseObject, error) {
	rows, err := h.d.Store.Pool.Query(ctx, "select "+upstreamColumns+" from upstreams order by position, name")
	if err != nil {
		return nil, store.MapError(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Upstream, error) { return scanUpstream(r) })
	if err != nil {
		return nil, store.MapError(err)
	}
	return ListUpstreams200JSONResponse(out), nil
}

func (h *handlers) CreateUpstream(ctx context.Context, req CreateUpstreamRequestObject) (CreateUpstreamResponseObject, error) {
	f, err := validateUpstream(*req.Body)
	if err != nil {
		return nil, err
	}
	var after Upstream
	err = h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		var err error
		after, err = scanUpstream(tx.QueryRow(ctx, `insert into upstreams(name, protocol, address, tls_server_name, doh_url,
			timeout_ms, ca_certificate_pem, position, enabled) values ($1, $2, $3, $4, $5, $6, $7, $8, $9) returning `+upstreamColumns,
			f.name, f.protocol, f.address, f.tlsName, f.dohURL, f.timeout, f.caPEM, f.position, f.enabled))
		return auth.Change{Action: "createUpstream", TargetType: "upstream", TargetID: after.Id.String(), After: after}, err
	})
	if err != nil {
		return nil, err
	}
	return CreateUpstream201JSONResponse(after), nil
}

func (h *handlers) UpdateUpstream(ctx context.Context, req UpdateUpstreamRequestObject) (UpdateUpstreamResponseObject, error) {
	f, err := validateUpstream(*req.Body)
	if err != nil {
		return nil, err
	}
	rev, err := requireRevision(req.Body.Revision)
	if err != nil {
		return nil, err
	}
	var after Upstream
	err = h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := scanUpstream(tx.QueryRow(ctx, "select "+upstreamColumns+" from upstreams where id = $1 for update", req.Id))
		if err != nil {
			return auth.Change{}, err
		}
		if err := checkRevision(before.Revision, rev); err != nil {
			return auth.Change{}, err
		}
		after, err = scanUpstream(tx.QueryRow(ctx, `update upstreams set name = $2, protocol = $3, address = $4, tls_server_name = $5,
			doh_url = $6, timeout_ms = $7, ca_certificate_pem = $8, position = $9, enabled = $10,
			revision = revision + 1, updated_at = now() where id = $1 returning `+upstreamColumns,
			req.Id, f.name, f.protocol, f.address, f.tlsName, f.dohURL, f.timeout, f.caPEM, f.position, f.enabled))
		return auth.Change{Action: "updateUpstream", TargetType: "upstream", TargetID: req.Id.String(), Before: before, After: after}, err
	})
	if err != nil {
		return nil, err
	}
	return UpdateUpstream200JSONResponse(after), nil
}

func (h *handlers) DeleteUpstream(ctx context.Context, req DeleteUpstreamRequestObject) (DeleteUpstreamResponseObject, error) {
	err := h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := scanUpstream(tx.QueryRow(ctx, "select "+upstreamColumns+" from upstreams where id = $1 for update", req.Id))
		if err != nil {
			return auth.Change{}, err
		}
		if err := checkRevision(before.Revision, req.Params.Revision); err != nil {
			return auth.Change{}, err
		}
		_, err = tx.Exec(ctx, "delete from upstreams where id = $1", req.Id)
		return auth.Change{Action: "deleteUpstream", TargetType: "upstream", TargetID: req.Id.String(), Before: before}, err
	})
	if err != nil {
		return nil, err
	}
	return DeleteUpstream204Response{}, nil
}

// ---- resolver settings ----

const resolverColumns = `strategy, cache_max_bytes, cache_min_ttl, cache_max_ttl, cache_negative_max_ttl, cache_stale_window,
	block_mode, block_ttl, otlp_endpoint, trace_sample_one_in, trace_slow_threshold_us, revision`

func scanResolver(row pgx.Row) (ResolverSettings, error) {
	var s ResolverSettings
	var strategy, blockMode string
	err := row.Scan(&strategy, &s.CacheMaxBytes, &s.CacheMinTtl, &s.CacheMaxTtl, &s.CacheNegativeMaxTtl, &s.CacheStaleWindow,
		&blockMode, &s.BlockTtl, &s.OtlpEndpoint, &s.TraceSampleOneIn, &s.TraceSlowThresholdUs, &s.Revision)
	s.Strategy, s.BlockMode = ResolverSettingsStrategy(strategy), ResolverSettingsBlockMode(blockMode)
	return s, store.MapError(err)
}

func validateResolver(s ResolverSettings) error {
	switch {
	case !s.Strategy.Valid():
		return invalid("strategy must be ordered or fastest")
	case !s.BlockMode.Valid():
		return invalid("block_mode must be null_ip, nxdomain or refused")
	case s.CacheMaxBytes < 1<<20:
		return invalid("cache_max_bytes must be at least 1048576")
	case s.CacheMinTtl < 0 || s.CacheMaxTtl < 0 || s.CacheNegativeMaxTtl < 0 || s.CacheStaleWindow < 0 ||
		s.BlockTtl < 0 || s.TraceSampleOneIn < 0 || s.TraceSlowThresholdUs < 0:
		return invalid("TTLs, windows and trace settings must not be negative")
	case s.CacheMaxTtl < s.CacheMinTtl:
		return invalid("cache_max_ttl must be at least cache_min_ttl")
	}
	return nil
}

func (h *handlers) GetResolverSettings(ctx context.Context, _ GetResolverSettingsRequestObject) (GetResolverSettingsResponseObject, error) {
	s, err := scanResolver(h.d.Store.Pool.QueryRow(ctx, "select "+resolverColumns+" from resolver_settings"))
	if err != nil {
		return nil, err
	}
	return GetResolverSettings200JSONResponse(s), nil
}

func (h *handlers) UpdateResolverSettings(ctx context.Context, req UpdateResolverSettingsRequestObject) (UpdateResolverSettingsResponseObject, error) {
	in := *req.Body
	if err := validateResolver(in); err != nil {
		return nil, err
	}
	var after ResolverSettings
	err := h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := scanResolver(tx.QueryRow(ctx, "select "+resolverColumns+" from resolver_settings for update"))
		if err != nil {
			return auth.Change{}, err
		}
		if err := checkRevision(before.Revision, in.Revision); err != nil {
			return auth.Change{}, err
		}
		after, err = scanResolver(tx.QueryRow(ctx, `update resolver_settings set strategy = $1, cache_max_bytes = $2,
			cache_min_ttl = $3, cache_max_ttl = $4, cache_negative_max_ttl = $5, cache_stale_window = $6, block_mode = $7,
			block_ttl = $8, otlp_endpoint = $9, trace_sample_one_in = $10, trace_slow_threshold_us = $11,
			revision = revision + 1, updated_at = now() returning `+resolverColumns,
			string(in.Strategy), in.CacheMaxBytes, in.CacheMinTtl, in.CacheMaxTtl, in.CacheNegativeMaxTtl, in.CacheStaleWindow,
			string(in.BlockMode), in.BlockTtl, strings.TrimSpace(in.OtlpEndpoint), in.TraceSampleOneIn, in.TraceSlowThresholdUs))
		return auth.Change{Action: "updateResolverSettings", TargetType: "resolver_settings", TargetID: "singleton", Before: before, After: after}, err
	})
	if err != nil {
		return nil, err
	}
	return UpdateResolverSettings200JSONResponse(after), nil
}

// ---- access control ----

const accessControlSelect = `select array(select host(c) || '/' || masklen(c)
	from unnest(allow_cidrs) with ordinality as u(c, n) order by n), revision from access_control`

func scanAccessControl(row pgx.Row) (AccessControl, error) {
	var a AccessControl
	err := row.Scan(&a.AllowCidrs, &a.Revision)
	return a, store.MapError(err)
}

func (h *handlers) GetAccessControl(ctx context.Context, _ GetAccessControlRequestObject) (GetAccessControlResponseObject, error) {
	a, err := scanAccessControl(h.d.Store.Pool.QueryRow(ctx, accessControlSelect))
	if err != nil {
		return nil, err
	}
	return GetAccessControl200JSONResponse(a), nil
}

func (h *handlers) UpdateAccessControl(ctx context.Context, req UpdateAccessControlRequestObject) (UpdateAccessControlResponseObject, error) {
	cidrs := make([]string, 0, len(req.Body.AllowCidrs))
	for _, c := range req.Body.AllowCidrs {
		p, err := netip.ParsePrefix(strings.TrimSpace(c))
		if err != nil {
			return nil, invalid("allow_cidrs: %q is not a CIDR", c)
		}
		cidrs = append(cidrs, p.Masked().String())
	}
	var after AccessControl
	err := h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := scanAccessControl(tx.QueryRow(ctx, accessControlSelect+" for update"))
		if err != nil {
			return auth.Change{}, err
		}
		if err := checkRevision(before.Revision, req.Body.Revision); err != nil {
			return auth.Change{}, err
		}
		if _, err := tx.Exec(ctx, "update access_control set allow_cidrs = $1::cidr[], revision = revision + 1, updated_at = now()", cidrs); err != nil {
			return auth.Change{}, err
		}
		after, err = scanAccessControl(tx.QueryRow(ctx, accessControlSelect))
		return auth.Change{Action: "updateAccessControl", TargetType: "access_control", TargetID: "singleton", Before: before, After: after}, err
	})
	if err != nil {
		return nil, err
	}
	return UpdateAccessControl200JSONResponse(after), nil
}

// ---- allowlist ----

func scanAllowlist(row pgx.Row) (Allowlist, error) {
	var a Allowlist
	err := row.Scan(&a.Domains, &a.Revision)
	return a, store.MapError(err)
}

func (h *handlers) GetAllowlist(ctx context.Context, _ GetAllowlistRequestObject) (GetAllowlistResponseObject, error) {
	a, err := scanAllowlist(h.d.Store.Pool.QueryRow(ctx, "select domains, revision from allowlist"))
	if err != nil {
		return nil, err
	}
	return GetAllowlist200JSONResponse(a), nil
}

func (h *handlers) UpdateAllowlist(ctx context.Context, req UpdateAllowlistRequestObject) (UpdateAllowlistResponseObject, error) {
	seen := map[string]bool{}
	domains := make([]string, 0, len(req.Body.Domains))
	for _, d := range req.Body.Domains {
		d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
		if len(d) > 253 || !domainRE.MatchString(d) {
			return nil, invalid("domains: %q is not a domain name", d)
		}
		if !seen[d] {
			seen[d] = true
			domains = append(domains, d)
		}
	}
	var after Allowlist
	err := h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := scanAllowlist(tx.QueryRow(ctx, "select domains, revision from allowlist for update"))
		if err != nil {
			return auth.Change{}, err
		}
		if err := checkRevision(before.Revision, req.Body.Revision); err != nil {
			return auth.Change{}, err
		}
		after, err = scanAllowlist(tx.QueryRow(ctx, `update allowlist set domains = $1, revision = revision + 1, updated_at = now()
			returning domains, revision`, domains))
		return auth.Change{Action: "updateAllowlist", TargetType: "allowlist", TargetID: "singleton", Before: before, After: after}, err
	})
	if err != nil {
		return nil, err
	}
	return UpdateAllowlist200JSONResponse(after), nil
}

// ---- filter lists ----

const filterListColumns = `id::text, name, kind, url, refresh_interval_seconds, enabled, current_blob_sha256, entry_count,
	invalid_line_count, last_success_at, last_attempt_at, last_error,
	(last_error <> '' or (last_success_at is not null and
		last_success_at < now() - 2 * refresh_interval_seconds * interval '1 second')) as stale, revision`

func scanFilterList(row pgx.Row) (FilterList, error) {
	var f FilterList
	var id, kind string
	err := row.Scan(&id, &f.Name, &kind, &f.Url, &f.RefreshIntervalSeconds, &f.Enabled, &f.CurrentBlobSha256, &f.EntryCount,
		&f.InvalidLineCount, &f.LastSuccessAt, &f.LastAttemptAt, &f.LastError, &f.Stale, &f.Revision)
	if err != nil {
		return f, store.MapError(err)
	}
	f.Id, f.Kind = uuid.MustParse(id), FilterListKind(kind)
	return f, nil
}

func validateFilterList(in FilterListInput) (string, string, error) {
	name, rawURL := strings.TrimSpace(in.Name), strings.TrimSpace(in.Url)
	if len(name) < 1 || len(name) > 64 {
		return "", "", invalid("name must be 1-64 characters")
	}
	if !in.Kind.Valid() {
		return "", "", invalid("kind must be block or allow")
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "", invalid("url must be an http:// or https:// URL")
	}
	if in.RefreshIntervalSeconds < 300 {
		return "", "", invalid("refresh_interval_seconds must be at least 300")
	}
	return name, rawURL, nil
}

func (h *handlers) getFilterList(ctx context.Context, id uuid.UUID) (FilterList, error) {
	return scanFilterList(h.d.Store.Pool.QueryRow(ctx, "select "+filterListColumns+" from filter_lists where id = $1", id))
}

func (h *handlers) ListFilterLists(ctx context.Context, _ ListFilterListsRequestObject) (ListFilterListsResponseObject, error) {
	rows, err := h.d.Store.Pool.Query(ctx, "select "+filterListColumns+" from filter_lists order by name")
	if err != nil {
		return nil, store.MapError(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (FilterList, error) { return scanFilterList(r) })
	if err != nil {
		return nil, store.MapError(err)
	}
	return ListFilterLists200JSONResponse(out), nil
}

func (h *handlers) GetFilterList(ctx context.Context, req GetFilterListRequestObject) (GetFilterListResponseObject, error) {
	f, err := h.getFilterList(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return GetFilterList200JSONResponse(f), nil
}

func (h *handlers) CreateFilterList(ctx context.Context, req CreateFilterListRequestObject) (CreateFilterListResponseObject, error) {
	name, rawURL, err := validateFilterList(*req.Body)
	if err != nil {
		return nil, err
	}
	var after FilterList
	err = h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		var err error
		after, err = scanFilterList(tx.QueryRow(ctx, `insert into filter_lists(name, kind, url, refresh_interval_seconds, enabled)
			values ($1, $2, $3, $4, $5) returning `+filterListColumns, name, string(req.Body.Kind), rawURL, req.Body.RefreshIntervalSeconds, req.Body.Enabled))
		return auth.Change{Action: "createFilterList", TargetType: "filter_list", TargetID: after.Id.String(), After: after}, err
	})
	if err != nil {
		return nil, err
	}
	return CreateFilterList201JSONResponse(after), nil
}

func (h *handlers) UpdateFilterList(ctx context.Context, req UpdateFilterListRequestObject) (UpdateFilterListResponseObject, error) {
	name, rawURL, err := validateFilterList(*req.Body)
	if err != nil {
		return nil, err
	}
	rev, err := requireRevision(req.Body.Revision)
	if err != nil {
		return nil, err
	}
	var after FilterList
	err = h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := scanFilterList(tx.QueryRow(ctx, "select "+filterListColumns+" from filter_lists where id = $1 for update", req.Id))
		if err != nil {
			return auth.Change{}, err
		}
		if err := checkRevision(before.Revision, rev); err != nil {
			return auth.Change{}, err
		}
		after, err = scanFilterList(tx.QueryRow(ctx, `update filter_lists set name = $2, kind = $3, url = $4,
			refresh_interval_seconds = $5, enabled = $6, revision = revision + 1, updated_at = now()
			where id = $1 returning `+filterListColumns, req.Id, name, string(req.Body.Kind), rawURL, req.Body.RefreshIntervalSeconds, req.Body.Enabled))
		return auth.Change{Action: "updateFilterList", TargetType: "filter_list", TargetID: req.Id.String(), Before: before, After: after}, err
	})
	if err != nil {
		return nil, err
	}
	return UpdateFilterList200JSONResponse(after), nil
}

func (h *handlers) DeleteFilterList(ctx context.Context, req DeleteFilterListRequestObject) (DeleteFilterListResponseObject, error) {
	err := h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := scanFilterList(tx.QueryRow(ctx, "select "+filterListColumns+" from filter_lists where id = $1 for update", req.Id))
		if err != nil {
			return auth.Change{}, err
		}
		if err := checkRevision(before.Revision, req.Params.Revision); err != nil {
			return auth.Change{}, err
		}
		_, err = tx.Exec(ctx, "delete from filter_lists where id = $1", req.Id)
		return auth.Change{Action: "deleteFilterList", TargetType: "filter_list", TargetID: req.Id.String(), Before: before}, err
	})
	if err != nil {
		return nil, err
	}
	return DeleteFilterList204Response{}, nil
}

func (h *handlers) RefreshFilterList(ctx context.Context, req RefreshFilterListRequestObject) (RefreshFilterListResponseObject, error) {
	if h.d.RefreshFilterList == nil {
		return nil, invalid("filter list fetching is not enabled on this instance")
	}
	if _, err := h.getFilterList(ctx, req.Id); err != nil {
		return nil, err
	}
	if err := h.d.RefreshFilterList(ctx, PrincipalFrom(ctx), req.Id.String()); err != nil {
		return nil, err
	}
	f, err := h.getFilterList(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return RefreshFilterList200JSONResponse(f), nil
}
