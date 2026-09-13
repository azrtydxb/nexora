package api

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/control"
	"github.com/piwi3910/nexora/mgmt/internal/stats"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// An engine counts as connected while the instance holding its stream heartbeats (every 5 s).
const engineSelect = `select e.id::text, e.node_name, e.engine_version, e.enrolled_at, e.last_seen_at,
	(e.connected_instance is not null and coalesce(i.heartbeat_at > now() - interval '15 seconds', false)),
	e.applied_version, e.rejected_version, e.rejected_reason, e.persist_error, e.version_ahead,
	(select coalesce(max(version), 0) from config_versions)
	from engines e left join instances i on i.id = e.connected_instance where e.deleted_at is null`

func scanEngine(row pgx.Row) (Engine, error) {
	var e Engine
	var id string
	var latest int64
	err := row.Scan(&id, &e.NodeName, &e.EngineVersion, &e.EnrolledAt, &e.LastSeenAt, &e.Connected, &e.AppliedVersion,
		&e.RejectedVersion, &e.RejectedReason, &e.PersistError, &e.VersionAhead, &latest)
	if err != nil {
		return e, store.MapError(err)
	}
	e.Id = uuid.MustParse(id)
	switch {
	case e.VersionAhead:
		e.Status = Ahead
	case !e.Connected:
		e.Status = Disconnected
	case e.RejectedVersion != nil && *e.RejectedVersion > e.AppliedVersion:
		e.Status = Rejected
	case e.AppliedVersion == latest:
		e.Status = Current
	default:
		e.Status = Behind
	}
	return e, nil
}

func (h *handlers) ListEngines(ctx context.Context, _ ListEnginesRequestObject) (ListEnginesResponseObject, error) {
	rows, err := h.d.Store.Pool.Query(ctx, engineSelect+" order by e.node_name, e.enrolled_at")
	if err != nil {
		return nil, store.MapError(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Engine, error) { return scanEngine(r) })
	if err != nil {
		return nil, store.MapError(err)
	}
	return ListEngines200JSONResponse(out), nil
}

func (h *handlers) GetEngine(ctx context.Context, req GetEngineRequestObject) (GetEngineResponseObject, error) {
	e, err := scanEngine(h.d.Store.Pool.QueryRow(ctx, engineSelect+" and e.id = $1", req.Id))
	if err != nil {
		return nil, err
	}
	return GetEngine200JSONResponse(e), nil
}

// DeleteEngine marks the engine deleted; its certificate stops being accepted on the next call.
func (h *handlers) DeleteEngine(ctx context.Context, req DeleteEngineRequestObject) (DeleteEngineResponseObject, error) {
	err := h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := scanEngine(tx.QueryRow(ctx, engineSelect+" and e.id = $1", req.Id))
		if err != nil {
			return auth.Change{}, err
		}
		tag, err := tx.Exec(ctx, "update engines set deleted_at = now() where id = $1 and deleted_at is null", req.Id)
		if err == nil && tag.RowsAffected() == 0 {
			err = store.ErrNotFound // deleted concurrently
		}
		return auth.Change{Action: "deleteEngine", TargetType: "engine", TargetID: req.Id.String(), Before: before}, err
	})
	if err != nil {
		return nil, err
	}
	return DeleteEngine204Response{}, nil
}

const joinTokenColumns = "id::text, name, created_by, created_at, expires_at, revoked_at, uses"

func scanJoinToken(row pgx.Row) (JoinToken, error) {
	var j JoinToken
	var id string
	if err := row.Scan(&id, &j.Name, &j.CreatedBy, &j.CreatedAt, &j.ExpiresAt, &j.RevokedAt, &j.Uses); err != nil {
		return j, store.MapError(err)
	}
	j.Id = uuid.MustParse(id)
	return j, nil
}

func (h *handlers) ListJoinTokens(ctx context.Context, _ ListJoinTokensRequestObject) (ListJoinTokensResponseObject, error) {
	rows, err := h.d.Store.Pool.Query(ctx, "select "+joinTokenColumns+" from join_tokens order by created_at desc")
	if err != nil {
		return nil, store.MapError(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (JoinToken, error) { return scanJoinToken(r) })
	if err != nil {
		return nil, store.MapError(err)
	}
	return ListJoinTokens200JSONResponse(out), nil
}

func (h *handlers) CreateJoinToken(ctx context.Context, req CreateJoinTokenRequestObject) (CreateJoinTokenResponseObject, error) {
	name := strings.TrimSpace(req.Body.Name)
	if len(name) < 1 || len(name) > 64 {
		return nil, invalid("name must be 1-64 characters")
	}
	if req.Body.TtlSeconds < 60 || req.Body.TtlSeconds > 31536000 {
		return nil, invalid("ttl_seconds must be between 60 and 31536000")
	}
	var created JoinTokenCreated
	err := h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		jt, token, err := control.CreateJoinToken(ctx, tx, h.d.CA, name, PrincipalFrom(ctx).Actor().Name,
			time.Duration(req.Body.TtlSeconds)*time.Second)
		if err != nil {
			return auth.Change{}, err
		}
		created = JoinTokenCreated{Token: token, JoinToken: JoinToken{Id: uuid.MustParse(jt.ID), Name: jt.Name,
			CreatedBy: jt.CreatedBy, CreatedAt: jt.CreatedAt, ExpiresAt: jt.ExpiresAt}}
		return auth.Change{Action: "createJoinToken", TargetType: "join_token", TargetID: jt.ID, After: created.JoinToken}, nil
	})
	if err != nil {
		return nil, err
	}
	return CreateJoinToken201JSONResponse(created), nil
}

func (h *handlers) RevokeJoinToken(ctx context.Context, req RevokeJoinTokenRequestObject) (RevokeJoinTokenResponseObject, error) {
	err := h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := scanJoinToken(tx.QueryRow(ctx, "select "+joinTokenColumns+" from join_tokens where id = $1 for update", req.Id))
		if err != nil {
			return auth.Change{}, err
		}
		after, err := scanJoinToken(tx.QueryRow(ctx, `update join_tokens set revoked_at = coalesce(revoked_at, now())
			where id = $1 returning `+joinTokenColumns, req.Id))
		return auth.Change{Action: "revokeJoinToken", TargetType: "join_token", TargetID: req.Id.String(), Before: before, After: after}, err
	})
	if err != nil {
		return nil, err
	}
	return RevokeJoinToken204Response{}, nil
}

func (h *handlers) ListConfigVersions(ctx context.Context, req ListConfigVersionsRequestObject) (ListConfigVersionsResponseObject, error) {
	limit, err := limitParam(req.Params.Limit, 50, 500)
	if err != nil {
		return nil, err
	}
	rows, err := h.d.Store.Pool.Query(ctx, "select version, created_at, created_by, summary from config_versions order by version desc limit $1", limit)
	if err != nil {
		return nil, store.MapError(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ConfigVersion, error) {
		var v ConfigVersion
		err := r.Scan(&v.Version, &v.CreatedAt, &v.CreatedBy, &v.Summary)
		return v, err
	})
	if err != nil {
		return nil, store.MapError(err)
	}
	return ListConfigVersions200JSONResponse(out), nil
}

func limitParam(p *int, def, max int) (int, error) {
	if p == nil {
		return def, nil
	}
	if *p < 1 || *p > max {
		return 0, invalid("limit must be between 1 and %d", max)
	}
	return *p, nil
}

func (h *handlers) GetDashboard(ctx context.Context, _ GetDashboardRequestObject) (GetDashboardResponseObject, error) {
	d, err := stats.Dashboard(ctx, h.d.Store)
	if err != nil {
		return nil, err
	}
	out := Dashboard{QueriesTotal: int64(d.QueriesTotal), BlockedTotal: int64(d.BlockedTotal), Qps: float32(d.QPS),
		CacheHitRatio: float32(d.CacheHitRatio), EnginesTotal: d.EnginesTotal, EnginesConnected: d.EnginesConnected}
	out.Upstreams = make([]struct {
		Name         string  `json:"name"`
		RttMs        float32 `json:"rtt_ms"`
		TotalEngines int     `json:"total_engines"`
		UpEngines    int     `json:"up_engines"`
	}, len(d.Upstreams))
	for i, u := range d.Upstreams {
		out.Upstreams[i].Name, out.Upstreams[i].RttMs = u.Name, float32(u.RTTMs)
		out.Upstreams[i].TotalEngines, out.Upstreams[i].UpEngines = u.TotalEngines, u.UpEngines
	}
	out.Series = make([]struct {
		At  time.Time `json:"at"`
		Qps float32   `json:"qps"`
	}, len(d.Series))
	for i, p := range d.Series {
		out.Series[i].At, out.Series[i].Qps = p.At, float32(p.QPS)
	}
	return GetDashboard200JSONResponse(out), nil
}
