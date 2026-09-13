package api

import (
	"context"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/fleet"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// rolloutSelect reads rollouts with their engine group name and progress: total counts the group's
// non-revoked, non-deleted engines (only the canaries while canary/verifying), applied those at or
// above the version, rejected those that rejected it.
const rolloutSelect = `select r.id, r.engine_group_id, g.name, r.version, r.from_version, r.kind, r.strategy, r.state,
	r.canary_engine_ids, r.phase_started_at, r.halt_reason, r.created_by, r.created_at, r.finished_at,
	p.total, p.applied, p.rejected
	from rollouts r join engine_groups g on g.id = r.engine_group_id
	cross join lateral (select count(*) as total,
		count(*) filter (where e.applied_version >= r.version) as applied,
		count(*) filter (where e.rejected_version = r.version) as rejected
		from engines e where e.engine_group_id = r.engine_group_id and e.revoked_at is null and e.deleted_at is null
		and (r.state not in ('canary', 'verifying') or e.id = any(r.canary_engine_ids))) p`

func scanRollout(row pgx.Row) (Rollout, error) {
	var r Rollout
	var kind, strategy, state string
	err := row.Scan(&r.Id, &r.EngineGroupId, &r.EngineGroupName, &r.Version, &r.FromVersion, &kind, &strategy, &state,
		&r.CanaryEngineIds, &r.PhaseStartedAt, &r.HaltReason, &r.CreatedBy, &r.CreatedAt, &r.FinishedAt,
		&r.Progress.Total, &r.Progress.Applied, &r.Progress.Rejected)
	r.Kind, r.Strategy, r.State = RolloutKind(kind), RolloutStrategy(strategy), RolloutState(state)
	if r.CanaryEngineIds == nil {
		r.CanaryEngineIds = []uuid.UUID{}
	}
	return r, store.MapError(err)
}

type rolloutFilter struct {
	engineGroupID *uuid.UUID
	state         *string
	limit         int
}

// listRollouts returns rollouts newest version first.
func listRollouts(ctx context.Context, q store.PolicyQuerier, f rolloutFilter) ([]Rollout, error) {
	rows, err := q.Query(ctx, rolloutSelect+` where ($1::uuid is null or r.engine_group_id = $1) and ($2::text is null or r.state = $2)
		order by r.version desc, r.created_at desc limit $3`, f.engineGroupID, f.state, f.limit)
	if err != nil {
		return nil, store.MapError(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Rollout, error) { return scanRollout(r) })
	if err != nil {
		return nil, err
	}
	return out, nil
}

func getRollout(ctx context.Context, q store.PolicyQuerier, id uuid.UUID) (Rollout, error) {
	return scanRollout(q.QueryRow(ctx, rolloutSelect+" where r.id = $1", id))
}

// activeRollouts returns each engine group's newest rollout that is pending, canary, verifying,
// rolling or halted.
func activeRollouts(ctx context.Context, q store.PolicyQuerier) (map[uuid.UUID]*Rollout, error) {
	rows, err := q.Query(ctx, rolloutSelect+` where r.state in ('pending', 'canary', 'verifying', 'rolling', 'halted')
		order by r.engine_group_id, r.version desc`)
	if err != nil {
		return nil, store.MapError(err)
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Rollout, error) { return scanRollout(r) })
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID]*Rollout{}
	for i := range list {
		if _, seen := out[list[i].EngineGroupId]; !seen {
			out[list[i].EngineGroupId] = &list[i]
		}
	}
	return out, nil
}

func (h *handlers) ListRollouts(ctx context.Context, req ListRolloutsRequestObject) (ListRolloutsResponseObject, error) {
	limit, err := limitParam(req.Params.Limit, 50, 200)
	if err != nil {
		return nil, err
	}
	f := rolloutFilter{engineGroupID: req.Params.EngineGroupId, limit: limit}
	if req.Params.State != nil {
		if !req.Params.State.Valid() {
			return nil, invalid("state %q is not a rollout state", *req.Params.State)
		}
		s := string(*req.Params.State)
		f.state = &s
	}
	out, err := listRollouts(ctx, h.d.Store.Pool, f)
	if err != nil {
		return nil, err
	}
	return ListRollouts200JSONResponse(out), nil
}

func (h *handlers) GetRollout(ctx context.Context, req GetRolloutRequestObject) (GetRolloutResponseObject, error) {
	r, err := getRollout(ctx, h.d.Store.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	views, err := fleet.ListEngines(ctx, h.d.Store.Pool, fleet.EngineFilter{EngineGroupID: &r.EngineGroupId})
	if err != nil {
		return nil, err
	}
	out := RolloutDetail{Id: r.Id, EngineGroupId: r.EngineGroupId, EngineGroupName: r.EngineGroupName, Version: r.Version,
		FromVersion: r.FromVersion, Kind: RolloutDetailKind(r.Kind), Strategy: RolloutDetailStrategy(r.Strategy), State: r.State,
		CanaryEngineIds: r.CanaryEngineIds, PhaseStartedAt: r.PhaseStartedAt, HaltReason: r.HaltReason, CreatedBy: r.CreatedBy,
		CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt, Progress: r.Progress}
	out.Engines = makeOf(out.Engines, 0)
	for _, v := range views {
		if v.RevokedAt != nil {
			continue
		}
		progress := RolloutDetailEnginesProgressWaiting
		switch {
		case v.RejectedVersion != nil && int64(*v.RejectedVersion) == r.Version:
			progress = RolloutDetailEnginesProgressRejected
		case int64(v.AppliedVersion) >= r.Version:
			progress = RolloutDetailEnginesProgressApplied
		case !v.Connected:
			progress = RolloutDetailEnginesProgressDisconnected
		}
		out.Engines = append(out.Engines, struct {
			AppliedVersion int64                        `json:"applied_version"`
			Canary         bool                         `json:"canary"`
			Connected      bool                         `json:"connected"`
			EngineId       uuid.UUID                    `json:"engine_id"`
			NodeName       string                       `json:"node_name"`
			Progress       RolloutDetailEnginesProgress `json:"progress"`
			RejectedReason string                       `json:"rejected_reason"`
		}{AppliedVersion: int64(v.AppliedVersion), Canary: slices.Contains(r.CanaryEngineIds, v.ID), Connected: v.Connected,
			EngineId: v.ID, NodeName: v.NodeName, Progress: progress, RejectedReason: v.RejectedReason})
	}
	return GetRollout200JSONResponse(out), nil
}

func (h *handlers) GetFleetSummary(ctx context.Context, _ GetFleetSummaryRequestObject) (GetFleetSummaryResponseObject, error) {
	views, err := fleet.ListEngines(ctx, h.d.Store.Pool, fleet.EngineFilter{})
	if err != nil {
		return nil, err
	}
	groups, err := fleet.ListEngineGroups(ctx, h.d.Store.Pool)
	if err != nil {
		return nil, err
	}
	active, err := activeRollouts(ctx, h.d.Store.Pool)
	if err != nil {
		return nil, err
	}
	out := FleetSummary{EnginesTotal: len(views), EnginesByStatus: map[string]int{}}
	if err := h.d.Store.Pool.QueryRow(ctx, "select count(*) from rollouts where state = 'halted'").Scan(&out.HaltedRollouts); err != nil {
		return nil, store.MapError(err)
	}
	engines, connected := map[uuid.UUID]int{}, map[uuid.UUID]int{}
	for _, v := range views {
		out.EnginesByStatus[v.Status]++
		engines[v.EngineGroupID]++
		if v.Connected {
			connected[v.EngineGroupID]++
		}
	}
	out.EngineGroups = makeOf(out.EngineGroups, len(groups))
	for i, g := range groups {
		o := &out.EngineGroups[i]
		o.Id, o.Name, o.Engines, o.Connected = g.ID, g.Name, engines[g.ID], connected[g.ID]
		o.RolloutStrategy, o.RolloutsPaused, o.ActiveRollout = FleetSummaryEngineGroupsRolloutStrategy(g.RolloutStrategy), g.RolloutsPaused, active[g.ID]
		if g.StableVersion != nil {
			v := int64(*g.StableVersion)
			o.StableVersion = &v
		}
	}
	return GetFleetSummary200JSONResponse(out), nil
}
