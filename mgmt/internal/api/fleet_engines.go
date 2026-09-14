package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/control"
	"github.com/piwi3910/nexora/mgmt/internal/fleet"
	"github.com/piwi3910/nexora/mgmt/internal/rollout"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

func (h *handlers) UpdateEngine(ctx context.Context, req UpdateEngineRequestObject) (UpdateEngineResponseObject, error) {
	b := req.Body
	if b.Labels != nil {
		if err := fleet.ValidateLabels(*b.Labels); err != nil {
			return nil, coded(http.StatusBadRequest, "invalid_labels", "%s", err.Error())
		}
	}
	actor := PrincipalFrom(ctx).Actor()
	err := h.d.Store.InTx(ctx, func(tx pgx.Tx) error {
		var revision int64
		var revoked bool
		var group uuid.UUID
		err := tx.QueryRow(ctx, `select revision, revoked_at is not null, engine_group_id from engines
			where id = $1 and deleted_at is null for update`, req.Id).Scan(&revision, &revoked, &group)
		if err != nil {
			return store.MapError(err)
		}
		if err := checkRevision(revision, b.Revision); err != nil {
			return err
		}
		if revoked {
			return coded(http.StatusConflict, "engine_revoked", "engine %s is revoked", req.Id)
		}
		if err := requireEngineGroup(ctx, tx, b.EngineGroupId); err != nil {
			return err
		}
		before, err := getEngine(ctx, tx, req.Id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update engines set engine_group_id = coalesce($2, engine_group_id), labels = coalesce($3, labels),
			revision = revision + 1 where id = $1`, req.Id, b.EngineGroupId, b.Labels); err != nil {
			return err
		}
		if b.EngineGroupId != nil && *b.EngineGroupId != group {
			// The engine applies only versions above the one it runs, so its new group's snapshot
			// is copied into a new version rolled out at once.
			var from int64
			if err := tx.QueryRow(ctx, `select coalesce(g.stable_version, (select max(version) from group_snapshots where engine_group_id = g.id), 0)
				from engine_groups g where g.id = $1`, *b.EngineGroupId).Scan(&from); err != nil {
				return err
			}
			if from > 0 {
				if _, _, err := snapshot.Republish(ctx, tx, actor, *b.EngineGroupId, uint64(from), rollout.KindRepublish); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, "select pg_notify($1, $2::text)", control.ChannelEngineUpdated, req.Id); err != nil {
				return err
			}
		}
		after, err := getEngine(ctx, tx, req.Id)
		if err != nil {
			return err
		}
		return auth.WriteAudit(ctx, tx, actor, auth.Change{Action: "updateEngine", TargetType: "engine", TargetID: req.Id.String(),
			Before: before, After: after}, nil)
	})
	if err != nil {
		return nil, err
	}
	e, err := getEngine(ctx, h.d.Store.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	return UpdateEngine200JSONResponse(e), nil
}

var statsWindows = map[GetEngineStatsParamsWindow]time.Duration{"5m": 5 * time.Minute, "1h": time.Hour, "24h": 24 * time.Hour}

func (h *handlers) GetEngineStats(ctx context.Context, req GetEngineStatsRequestObject) (GetEngineStatsResponseObject, error) {
	window := GetEngineStatsParamsWindow("5m")
	if req.Params.Window != nil {
		window = *req.Params.Window
	}
	d, ok := statsWindows[window]
	if !ok {
		return nil, invalid("window must be 5m, 1h or 24h")
	}
	if _, err := getEngine(ctx, h.d.Store.Pool, req.Id); err != nil {
		return nil, err
	}
	points, err := fleet.Series(ctx, h.d.Store.Pool, req.Id, d)
	if err != nil {
		return nil, err
	}
	out := EngineStats{Window: EngineStatsWindow(window)}
	out.Samples = makeOf(out.Samples, len(points))
	for i, p := range points {
		s := &out.Samples[i]
		s.At, s.Qps, s.CacheHitRatio, s.ServfailRatio, s.P99Ms = p.At, float32(p.QPS), float32(p.CacheHitRatio), float32(p.ServfailRatio), float32(p.P99Ms)
	}
	fi, at, err := fleet.LatestFilterIndex(ctx, h.d.Store.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	if fi != nil {
		f := newOf(out.FilterIndex)
		f.At, f.Entries, f.Bytes, f.MaxBytes, f.Cpu = at, int64(fi.Entries), int64(fi.Bytes), int64(fi.MaxBytes), fi.Cpu
		f.BuildSeconds, f.DecisionNsBlocked, f.DecisionNsClean = float32(fi.BuildSeconds), float32(fi.DecisionNsBlocked), float32(fi.DecisionNsClean)
		out.FilterIndex = f
	}
	return GetEngineStats200JSONResponse(out), nil
}

// revokedConflict maps fleet.ErrEngineRevoked to 409 engine_revoked.
func revokedConflict(err error, id uuid.UUID) error {
	if errors.Is(err, fleet.ErrEngineRevoked) {
		return coded(http.StatusConflict, "engine_revoked", "engine %s is revoked", id)
	}
	return err
}

// RevokeEngine revokes the engine and all its certificates; every instance ends its streams.
func (h *handlers) RevokeEngine(ctx context.Context, req RevokeEngineRequestObject) (RevokeEngineResponseObject, error) {
	err := h.audited(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := getEngine(ctx, tx, req.Id)
		if err != nil {
			return auth.Change{}, err
		}
		if before.RevokedAt != nil {
			return auth.Change{}, coded(http.StatusConflict, "engine_revoked", "engine %s is already revoked", req.Id)
		}
		if err := fleet.RevokeEngine(ctx, tx, req.Id); err != nil {
			return auth.Change{}, err
		}
		after, err := getEngine(ctx, tx, req.Id)
		return auth.Change{Action: "revokeEngine", TargetType: "engine", TargetID: req.Id.String(), Before: before, After: after}, err
	})
	if err != nil {
		return nil, err
	}
	e, err := getEngine(ctx, h.d.Store.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	return RevokeEngine200JSONResponse(e), nil
}

// RotateEngineCertificate asks the engine for a new certificate over its control stream.
func (h *handlers) RotateEngineCertificate(ctx context.Context, req RotateEngineCertificateRequestObject) (RotateEngineCertificateResponseObject, error) {
	err := h.audited(ctx, func(tx pgx.Tx) (auth.Change, error) {
		if err := fleet.RequestRotation(ctx, tx, req.Id); err != nil {
			return auth.Change{}, revokedConflict(err, req.Id)
		}
		return auth.Change{Action: "rotateEngineCertificate", TargetType: "engine", TargetID: req.Id.String()}, nil
	})
	if err != nil {
		return nil, err
	}
	e, err := getEngine(ctx, h.d.Store.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	return RotateEngineCertificate202JSONResponse(e), nil
}
