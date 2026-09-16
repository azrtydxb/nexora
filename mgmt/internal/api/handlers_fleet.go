package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/control"
	"github.com/piwi3910/nexora/mgmt/internal/fleet"
	"github.com/piwi3910/nexora/mgmt/internal/stats"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// engineFromView maps a fleet engine view to the API schema.
func engineFromView(v fleet.EngineView) Engine {
	e := Engine{Id: v.ID, NodeName: v.NodeName, EngineVersion: v.EngineVersion, EnrolledAt: v.EnrolledAt, LastSeenAt: v.LastSeenAt,
		Connected: v.Connected, AppliedVersion: int64(v.AppliedVersion), RejectedReason: v.RejectedReason,
		PersistError: v.PersistError, VersionAhead: v.VersionAhead, Status: EngineStatus(v.Status),
		EngineGroupId: v.EngineGroupID, EngineGroupName: v.EngineGroupName, Labels: v.Labels, Revision: v.Revision,
		TargetVersion: int64(v.TargetVersion), CertificateSerial: v.CertificateSerial, RevokedAt: v.RevokedAt,
		CertRotateRequestedAt: v.CertRotateRequestedAt, CertificateNotAfter: v.CertificateNotAfter}
	if e.Labels == nil {
		e.Labels = map[string]string{}
	}
	if v.RejectedVersion != nil {
		r := int64(*v.RejectedVersion)
		e.RejectedVersion = &r
	}
	return e
}

func getEngine(ctx context.Context, q store.PolicyQuerier, id uuid.UUID) (Engine, error) {
	views, err := fleet.ListEngines(ctx, q, fleet.EngineFilter{EngineID: &id})
	if err != nil {
		return Engine{}, err
	}
	if len(views) == 0 {
		return Engine{}, store.ErrNotFound
	}
	return engineFromView(views[0]), nil
}

func (h *handlers) ListEngines(ctx context.Context, _ ListEnginesRequestObject) (ListEnginesResponseObject, error) {
	views, err := fleet.ListEngines(ctx, h.d.Store.Pool, fleet.EngineFilter{})
	if err != nil {
		return nil, err
	}
	out := make([]Engine, len(views))
	for i, v := range views {
		out[i] = engineFromView(v)
	}
	return ListEngines200JSONResponse(out), nil
}

func (h *handlers) GetEngine(ctx context.Context, req GetEngineRequestObject) (GetEngineResponseObject, error) {
	e, err := getEngine(ctx, h.d.Store.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	return GetEngine200JSONResponse(e), nil
}

// DeleteEngine revokes the engine (its streams end at once and its certificates stop being
// accepted) and marks it deleted.
func (h *handlers) DeleteEngine(ctx context.Context, req DeleteEngineRequestObject) (DeleteEngineResponseObject, error) {
	err := h.audited(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := getEngine(ctx, tx, req.Id)
		if err != nil {
			return auth.Change{}, err
		}
		if err := fleet.RevokeEngine(ctx, tx, req.Id); err != nil {
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

// joinTokenSelect reads join tokens with their engine group name and state.
const joinTokenSelect = `select t.id, t.name, t.created_by, t.created_at, t.expires_at, t.revoked_at, t.uses, t.engine_group_id, g.name,
	t.labels, t.max_uses,
	case when t.revoked_at is not null then 'revoked' when t.expires_at <= now() then 'expired'
	     when t.max_uses is not null and t.uses >= t.max_uses then 'exhausted' else 'active' end
	from join_tokens t join engine_groups g on g.id = t.engine_group_id`

func scanJoinToken(row pgx.Row) (JoinToken, error) {
	var j JoinToken
	var state string
	if err := row.Scan(&j.Id, &j.Name, &j.CreatedBy, &j.CreatedAt, &j.ExpiresAt, &j.RevokedAt, &j.Uses, &j.EngineGroupId,
		&j.EngineGroupName, &j.Labels, &j.MaxUses, &state); err != nil {
		return j, store.MapError(err)
	}
	j.State = JoinTokenState(state)
	return j, nil
}

func (h *handlers) ListJoinTokens(ctx context.Context, _ ListJoinTokensRequestObject) (ListJoinTokensResponseObject, error) {
	rows, err := h.d.Store.Pool.Query(ctx, joinTokenSelect+" order by t.created_at desc")
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
	b := req.Body
	name := strings.TrimSpace(b.Name)
	if len(name) < 1 || len(name) > 64 {
		return nil, invalid("name must be 1-64 characters")
	}
	if b.TtlSeconds < 60 || b.TtlSeconds > 31536000 {
		return nil, invalid("ttl_seconds must be between 60 and 31536000")
	}
	if b.MaxUses != nil && (*b.MaxUses < 1 || *b.MaxUses > 100000) {
		return nil, invalid("max_uses must be between 1 and 100000")
	}
	spec := control.JoinTokenSpec{Name: name, CreatedBy: PrincipalFrom(ctx).Actor().Name, TTL: time.Duration(b.TtlSeconds) * time.Second,
		EngineGroupID: store.DefaultEngineGroupID, MaxUses: b.MaxUses}
	if b.EngineGroupId != nil {
		spec.EngineGroupID = *b.EngineGroupId
	}
	if b.Labels != nil {
		if err := fleet.ValidateLabels(*b.Labels); err != nil {
			return nil, coded(http.StatusBadRequest, "invalid_labels", "%s", err.Error())
		}
		spec.Labels = *b.Labels
	}
	var created JoinTokenCreated
	err := h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		if err := requireEngineGroup(ctx, tx, &spec.EngineGroupID); err != nil {
			return auth.Change{}, err
		}
		jt, token, err := control.CreateJoinTokenFor(ctx, tx, h.d.CA, spec)
		if err != nil {
			return auth.Change{}, err
		}
		created.Token = token
		if created.JoinToken, err = scanJoinToken(tx.QueryRow(ctx, joinTokenSelect+" where t.id = $1", jt.ID)); err != nil {
			return auth.Change{}, err
		}
		return auth.Change{Action: "createJoinToken", TargetType: "join_token", TargetID: jt.ID, After: created.JoinToken}, nil
	})
	if err != nil {
		return nil, err
	}
	return CreateJoinToken201JSONResponse(created), nil
}

func (h *handlers) RevokeJoinToken(ctx context.Context, req RevokeJoinTokenRequestObject) (RevokeJoinTokenResponseObject, error) {
	err := h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := scanJoinToken(tx.QueryRow(ctx, joinTokenSelect+" where t.id = $1 for update of t", req.Id))
		if err != nil {
			return auth.Change{}, err
		}
		if _, err := tx.Exec(ctx, "update join_tokens set revoked_at = coalesce(revoked_at, now()) where id = $1", req.Id); err != nil {
			return auth.Change{}, err
		}
		after, err := scanJoinToken(tx.QueryRow(ctx, joinTokenSelect+" where t.id = $1", req.Id))
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
		Name              string  `json:"name"`
		RttMs             float32 `json:"rtt_ms"`
		TotalEngines      int     `json:"total_engines"`
		UnmeasuredEngines int     `json:"unmeasured_engines"`
		UpEngines         int     `json:"up_engines"`
	}, len(d.Upstreams))
	for i, u := range d.Upstreams {
		out.Upstreams[i].Name, out.Upstreams[i].RttMs = u.Name, float32(u.RTTMs)
		out.Upstreams[i].TotalEngines, out.Upstreams[i].UpEngines = u.TotalEngines, u.UpEngines
		out.Upstreams[i].UnmeasuredEngines = u.UnmeasuredEngines
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
