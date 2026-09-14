package api

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/fleet"
	"github.com/piwi3910/nexora/mgmt/internal/rollout"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// requireEngineGroup refuses an unknown engine group with 422 engine_group_not_found and holds a
// key-share lock on a known one, so the group cannot be deleted before tx commits.
func requireEngineGroup(ctx context.Context, tx pgx.Tx, id *uuid.UUID) error {
	if id == nil {
		return nil
	}
	var one int
	err := tx.QueryRow(ctx, "select 1 from engine_groups where id = $1 for key share", *id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return coded(http.StatusUnprocessableEntity, "engine_group_not_found", "engine group %s does not exist", *id)
	}
	return err
}

// engineGroupInput is the body of createEngineGroup and updateEngineGroup without the revision.
type engineGroupInput struct {
	name                                                             string
	description, otlpEndpoint, upstreamMode, strategy                *string
	extraACL                                                         *[]string
	canaryCount, canaryPercent, ackTimeout, healthWindow, minQueries *int
	maxServfail                                                      *float32
	filterIndexMaxBytes                                              *int64
}

// applyEngineGroupInput overlays in on base (the table defaults on create, the stored group on
// update) and validates the result.
func applyEngineGroupInput(base fleet.EngineGroup, in engineGroupInput) (fleet.EngineGroup, error) {
	g := base
	g.Name = strings.TrimSpace(in.name)
	if !fleet.EngineGroupNameRE.MatchString(g.Name) {
		return g, invalid("name must match ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$")
	}
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	set(&g.Description, in.description)
	set(&g.OTLPEndpoint, in.otlpEndpoint)
	set(&g.UpstreamMode, in.upstreamMode)
	set(&g.RolloutStrategy, in.strategy)
	for _, f := range []struct {
		dst *int
		v   *int
	}{{&g.CanaryCount, in.canaryCount}, {&g.CanaryPercent, in.canaryPercent}, {&g.AckTimeoutSeconds, in.ackTimeout},
		{&g.HealthWindowSeconds, in.healthWindow}, {&g.MinHealthQueries, in.minQueries}} {
		if f.v != nil {
			*f.dst = *f.v
		}
	}
	if in.maxServfail != nil {
		g.MaxServfailRatio = float64(*in.maxServfail)
	}
	if in.filterIndexMaxBytes != nil {
		g.FilterIndexMaxBytes = *in.filterIndexMaxBytes
	}
	if in.extraACL != nil {
		if len(*in.extraACL) > 256 {
			return g, invalid("extra_acl_cidrs must list at most 256 prefixes")
		}
		g.ExtraACLCIDRs = make([]string, 0, len(*in.extraACL))
		for _, c := range *in.extraACL {
			p, err := netip.ParsePrefix(strings.TrimSpace(c))
			if err != nil {
				return g, invalid("extra_acl_cidrs: %q is not a CIDR", c)
			}
			g.ExtraACLCIDRs = append(g.ExtraACLCIDRs, p.Masked().String())
		}
	}
	switch {
	case len(g.Description) > 1024:
		return g, invalid("description must be at most 1024 characters")
	case len(g.OTLPEndpoint) > 512:
		return g, invalid("otlp_endpoint must be at most 512 characters")
	case g.UpstreamMode != "inherit" && g.UpstreamMode != "override":
		return g, invalid("upstream_mode must be inherit or override")
	case g.RolloutStrategy != string(rollout.AllAtOnce) && g.RolloutStrategy != string(rollout.CanaryStrategy):
		return g, invalid("rollout_strategy must be all_at_once or canary")
	case g.CanaryCount < 0 || g.CanaryPercent < 0 || g.CanaryPercent > 100:
		return g, coded(http.StatusBadRequest, "invalid_rollout_params", "canary_count must not be negative and canary_percent must be 0..100")
	case g.AckTimeoutSeconds < 5 || g.AckTimeoutSeconds > 3600:
		return g, coded(http.StatusBadRequest, "invalid_rollout_params", "ack_timeout_seconds must be 5..3600")
	case g.HealthWindowSeconds < 20 || g.HealthWindowSeconds > 3600:
		return g, coded(http.StatusBadRequest, "invalid_rollout_params", "health_window_seconds must be 20..3600")
	case g.MaxServfailRatio < 0 || g.MaxServfailRatio > 1:
		return g, coded(http.StatusBadRequest, "invalid_rollout_params", "max_servfail_ratio must be 0..1")
	case g.FilterIndexMaxBytes < 0 || (g.FilterIndexMaxBytes > 0 && g.FilterIndexMaxBytes < 16<<20):
		return g, invalid("filter_index_max_bytes must be 0 or at least 16777216")
	case g.MinHealthQueries < 0:
		return g, coded(http.StatusBadRequest, "invalid_rollout_params", "min_health_queries must not be negative")
	case g.RolloutStrategy == string(rollout.CanaryStrategy) && g.CanaryCount == 0 && g.CanaryPercent == 0:
		return g, coded(http.StatusBadRequest, "invalid_rollout_params", "canary strategy needs canary_count or canary_percent")
	}
	return g, nil
}

// newEngineGroupDefaults are the engine_groups column defaults.
var newEngineGroupDefaults = fleet.EngineGroup{UpstreamMode: "inherit", RolloutStrategy: string(rollout.AllAtOnce),
	AckTimeoutSeconds: 60, HealthWindowSeconds: 30, MaxServfailRatio: 0.05, MinHealthQueries: 100}

func engineGroupOut(g fleet.EngineGroup, active *Rollout) EngineGroup {
	out := EngineGroup{Id: g.ID, Name: g.Name, Description: g.Description, UpstreamMode: EngineGroupUpstreamMode(g.UpstreamMode),
		ExtraAclCidrs: append([]string{}, g.ExtraACLCIDRs...), OtlpEndpoint: g.OTLPEndpoint,
		RolloutStrategy: EngineGroupRolloutStrategy(g.RolloutStrategy), CanaryCount: g.CanaryCount, CanaryPercent: g.CanaryPercent,
		AckTimeoutSeconds: g.AckTimeoutSeconds, HealthWindowSeconds: g.HealthWindowSeconds, MaxServfailRatio: float32(g.MaxServfailRatio),
		MinHealthQueries: g.MinHealthQueries, FilterIndexMaxBytes: g.FilterIndexMaxBytes, RolloutsPaused: g.RolloutsPaused, EngineCount: g.EngineCount, Revision: g.Revision,
		CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt, ActiveRollout: active}
	if g.StableVersion != nil {
		v := int64(*g.StableVersion)
		out.StableVersion = &v
	}
	return out
}

func engineGroupNameError(err error, name string) error {
	if errors.Is(err, fleet.ErrEngineGroupNameTaken) {
		return coded(http.StatusConflict, "name_taken", "engine group %q already exists", name)
	}
	return err
}

func (h *handlers) ListEngineGroups(ctx context.Context, _ ListEngineGroupsRequestObject) (ListEngineGroupsResponseObject, error) {
	groups, err := fleet.ListEngineGroups(ctx, h.d.Store.Pool)
	if err != nil {
		return nil, err
	}
	active, err := activeRollouts(ctx, h.d.Store.Pool)
	if err != nil {
		return nil, err
	}
	out := make(ListEngineGroups200JSONResponse, 0, len(groups))
	for _, g := range groups {
		out = append(out, engineGroupOut(g, active[g.ID]))
	}
	return out, nil
}

func (h *handlers) getEngineGroup(ctx context.Context, q store.PolicyQuerier, id uuid.UUID) (EngineGroup, error) {
	g, err := fleet.GetEngineGroup(ctx, q, id)
	if err != nil {
		return EngineGroup{}, err
	}
	active, err := activeRollouts(ctx, q)
	if err != nil {
		return EngineGroup{}, err
	}
	return engineGroupOut(g, active[g.ID]), nil
}

func (h *handlers) GetEngineGroup(ctx context.Context, req GetEngineGroupRequestObject) (GetEngineGroupResponseObject, error) {
	g, err := h.getEngineGroup(ctx, h.d.Store.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	return GetEngineGroup200JSONResponse(g), nil
}

func (h *handlers) CreateEngineGroup(ctx context.Context, req CreateEngineGroupRequestObject) (CreateEngineGroupResponseObject, error) {
	b := req.Body
	g, err := applyEngineGroupInput(newEngineGroupDefaults, engineGroupInput{name: b.Name, description: b.Description,
		otlpEndpoint: b.OtlpEndpoint, upstreamMode: (*string)(b.UpstreamMode), strategy: (*string)(b.RolloutStrategy),
		extraACL: b.ExtraAclCidrs, canaryCount: b.CanaryCount, canaryPercent: b.CanaryPercent, ackTimeout: b.AckTimeoutSeconds,
		healthWindow: b.HealthWindowSeconds, minQueries: b.MinHealthQueries, maxServfail: b.MaxServfailRatio,
		filterIndexMaxBytes: b.FilterIndexMaxBytes})
	if err != nil {
		return nil, err
	}
	var created fleet.EngineGroup
	// Inside a publish: the new group gets its first snapshot and rollout with the version.
	err = h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		var err error
		created, err = fleet.CreateEngineGroup(ctx, tx, g)
		return auth.Change{Action: "createEngineGroup", TargetType: "engine_group", TargetID: created.ID.String(), After: engineGroupOut(created, nil)},
			engineGroupNameError(err, g.Name)
	})
	if err != nil {
		return nil, err
	}
	out, err := h.getEngineGroup(ctx, h.d.Store.Pool, created.ID)
	if err != nil {
		return nil, err
	}
	return CreateEngineGroup201JSONResponse(out), nil
}

func (h *handlers) UpdateEngineGroup(ctx context.Context, req UpdateEngineGroupRequestObject) (UpdateEngineGroupResponseObject, error) {
	b := req.Body
	in := engineGroupInput{name: b.Name, description: b.Description, otlpEndpoint: b.OtlpEndpoint,
		upstreamMode: (*string)(b.UpstreamMode), strategy: (*string)(b.RolloutStrategy), extraACL: b.ExtraAclCidrs,
		canaryCount: b.CanaryCount, canaryPercent: b.CanaryPercent, ackTimeout: b.AckTimeoutSeconds,
		healthWindow: b.HealthWindowSeconds, minQueries: b.MinHealthQueries, maxServfail: b.MaxServfailRatio,
		filterIndexMaxBytes: b.FilterIndexMaxBytes}
	err := h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := fleet.GetEngineGroup(ctx, tx, req.Id)
		if err != nil {
			return auth.Change{}, err
		}
		g, err := applyEngineGroupInput(before, in)
		if err != nil {
			return auth.Change{}, err
		}
		if req.Id == store.DefaultEngineGroupID && g.Name != before.Name {
			return auth.Change{}, coded(http.StatusConflict, "engine_group_protected", "the default engine group cannot be renamed")
		}
		after, err := fleet.UpdateEngineGroup(ctx, tx, g, b.Revision)
		return auth.Change{Action: "updateEngineGroup", TargetType: "engine_group", TargetID: req.Id.String(),
			Before: engineGroupOut(before, nil), After: engineGroupOut(after, nil)}, engineGroupNameError(err, g.Name)
	})
	if err != nil {
		return nil, err
	}
	out, err := h.getEngineGroup(ctx, h.d.Store.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	return UpdateEngineGroup200JSONResponse(out), nil
}

func (h *handlers) DeleteEngineGroup(ctx context.Context, req DeleteEngineGroupRequestObject) (DeleteEngineGroupResponseObject, error) {
	if req.Id == store.DefaultEngineGroupID {
		return nil, coded(http.StatusConflict, "engine_group_protected", "the default engine group cannot be deleted")
	}
	err := h.audited(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := fleet.GetEngineGroup(ctx, tx, req.Id)
		if err != nil {
			return auth.Change{}, err
		}
		err = fleet.DeleteEngineGroup(ctx, tx, req.Id, req.Params.Revision)
		if errors.Is(err, fleet.ErrEngineGroupNotEmpty) {
			err = coded(http.StatusConflict, "engine_group_not_empty", "%s", strings.TrimPrefix(err.Error(), fleet.ErrEngineGroupNotEmpty.Error()+": "))
		}
		return auth.Change{Action: "deleteEngineGroup", TargetType: "engine_group", TargetID: req.Id.String(), Before: engineGroupOut(before, nil)}, err
	})
	if err != nil {
		return nil, err
	}
	return DeleteEngineGroup204Response{}, nil
}

func (h *handlers) RollbackEngineGroup(ctx context.Context, req RollbackEngineGroupRequestObject) (RollbackEngineGroupResponseObject, error) {
	to := req.Body.ToVersion
	if to < 1 {
		return nil, invalid("to_version must be at least 1")
	}
	var out Rollout
	err := h.d.Store.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := fleet.GetEngineGroup(ctx, tx, req.Id); err != nil {
			return err
		}
		var exists bool
		var newest int64
		if err := tx.QueryRow(ctx, `select exists(select 1 from group_snapshots where engine_group_id = $1 and version = $2),
			coalesce((select max(version) from group_snapshots where engine_group_id = $1), 0)`, req.Id, to).Scan(&exists, &newest); err != nil {
			return err
		}
		if !exists {
			return coded(http.StatusNotFound, "version_not_found", "engine group has no snapshot of version %d", to)
		}
		if to >= newest {
			return coded(http.StatusBadRequest, "not_older", "version %d is not older than the engine group's newest version %d", to, newest)
		}
		_, id, err := snapshot.Republish(ctx, tx, PrincipalFrom(ctx).Actor(), req.Id, uint64(to), rollout.KindRollback)
		if err != nil {
			return err
		}
		out, err = getRollout(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return RollbackEngineGroup202JSONResponse(out), nil
}

func (h *handlers) ResumeEngineGroupRollouts(ctx context.Context, req ResumeEngineGroupRolloutsRequestObject) (ResumeEngineGroupRolloutsResponseObject, error) {
	err := h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		var paused bool
		err := tx.QueryRow(ctx, "select rollouts_paused from engine_groups where id = $1 for update", req.Id).Scan(&paused)
		if err != nil {
			return auth.Change{}, store.MapError(err)
		}
		if !paused {
			return auth.Change{}, coded(http.StatusConflict, "not_paused", "rollouts of this engine group are not paused")
		}
		_, err = tx.Exec(ctx, "update engine_groups set rollouts_paused = false, updated_at = now() where id = $1", req.Id)
		return auth.Change{Action: "resumeEngineGroupRollouts", TargetType: "engine_group", TargetID: req.Id.String()}, err
	})
	if err != nil {
		return nil, err
	}
	rollouts, err := listRollouts(ctx, h.d.Store.Pool, rolloutFilter{engineGroupID: &req.Id, limit: 1})
	if err != nil {
		return nil, err
	}
	if len(rollouts) == 0 {
		return nil, store.ErrNotFound
	}
	return ResumeEngineGroupRollouts202JSONResponse(rollouts[0]), nil
}
