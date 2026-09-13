package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

func policyGroupOut(g store.PolicyGroup) PolicyGroup {
	out := PolicyGroup{
		Id: g.ID, EngineGroupId: g.EngineGroupID, Name: g.Name, Description: g.Description, Revision: g.Revision, CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt,
		Cidrs: make([]string, 0, len(g.CIDRs)), FilterListIds: append([]uuid.UUID{}, g.FilterListIDs...), Allowlist: append([]string{}, g.Allowlist...),
		SafeSearch: SafeSearch{Google: g.SafeSearch.Google, Bing: g.SafeSearch.Bing, Duckduckgo: g.SafeSearch.DuckDuckGo, Youtube: SafeSearchYoutube(g.SafeSearch.YouTube)},
	}
	for _, c := range g.CIDRs {
		out.Cidrs = append(out.Cidrs, c.String())
	}
	return out
}

func globalSafeSearchOut(s store.GlobalSafeSearch) GlobalSafeSearch {
	return GlobalSafeSearch{Google: s.Google, Bing: s.Bing, Duckduckgo: s.DuckDuckGo, Youtube: GlobalSafeSearchYoutube(s.YouTube), Revision: s.Revision}
}

func (h *handlers) ListPolicyGroups(ctx context.Context, _ ListPolicyGroupsRequestObject) (ListPolicyGroupsResponseObject, error) {
	groups, err := store.ListPolicyGroups(ctx, h.d.Store.Pool)
	if err != nil {
		return nil, err
	}
	out := make(ListPolicyGroups200JSONResponse, 0, len(groups))
	for _, g := range groups {
		out = append(out, policyGroupOut(g))
	}
	return out, nil
}

func (h *handlers) GetPolicyGroup(ctx context.Context, req GetPolicyGroupRequestObject) (GetPolicyGroupResponseObject, error) {
	g, err := store.GetPolicyGroup(ctx, h.d.Store.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	return GetPolicyGroup200JSONResponse(policyGroupOut(g)), nil
}

func (h *handlers) CreatePolicyGroup(ctx context.Context, req CreatePolicyGroupRequestObject) (CreatePolicyGroupResponseObject, error) {
	g, err := validatePolicyGroup(*req.Body)
	var after PolicyGroup
	if err == nil {
		err = h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
			if err := checkPolicyGroupScope(ctx, tx, g); err != nil {
				return auth.Change{}, err
			}
			created, err := store.CreatePolicyGroup(ctx, tx, g)
			after = policyGroupOut(created)
			return auth.Change{Action: "createPolicyGroup", TargetType: "policy_group", TargetID: created.ID.String(), After: after}, err
		})
	}
	if status, e, ok := policyFailure(err); ok {
		if status == http.StatusConflict {
			return CreatePolicyGroup409JSONResponse{ErrorJSONResponse(e)}, nil
		}
		return CreatePolicyGroup422JSONResponse(e), nil
	}
	if err != nil {
		return nil, err
	}
	return CreatePolicyGroup201JSONResponse(after), nil
}

func (h *handlers) UpdatePolicyGroup(ctx context.Context, req UpdatePolicyGroupRequestObject) (UpdatePolicyGroupResponseObject, error) {
	b := req.Body
	g, err := validatePolicyGroup(PolicyGroupInput{Name: b.Name, Description: b.Description, Cidrs: b.Cidrs,
		FilterListIds: b.FilterListIds, Allowlist: b.Allowlist, SafeSearch: b.SafeSearch, EngineGroupId: b.EngineGroupId})
	var after PolicyGroup
	if err == nil {
		g.ID, g.Revision = req.Id, b.Revision
		err = h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
			before, err := store.GetPolicyGroup(ctx, tx, req.Id)
			if err != nil {
				return auth.Change{}, err
			}
			if err := checkPolicyGroupScope(ctx, tx, g); err != nil {
				return auth.Change{}, err
			}
			updated, err := store.UpdatePolicyGroup(ctx, tx, g)
			after = policyGroupOut(updated)
			return auth.Change{Action: "updatePolicyGroup", TargetType: "policy_group", TargetID: req.Id.String(),
				Before: policyGroupOut(before), After: after}, err
		})
	}
	if status, e, ok := policyFailure(err); ok {
		if status == http.StatusConflict {
			return UpdatePolicyGroup409JSONResponse(e), nil
		}
		return UpdatePolicyGroup422JSONResponse(e), nil
	}
	if err != nil {
		return nil, err
	}
	return UpdatePolicyGroup200JSONResponse(after), nil
}

func (h *handlers) DeletePolicyGroup(ctx context.Context, req DeletePolicyGroupRequestObject) (DeletePolicyGroupResponseObject, error) {
	err := h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := store.GetPolicyGroup(ctx, tx, req.Id)
		if err != nil {
			return auth.Change{}, err
		}
		return auth.Change{Action: "deletePolicyGroup", TargetType: "policy_group", TargetID: req.Id.String(), Before: policyGroupOut(before)},
			store.DeletePolicyGroup(ctx, tx, req.Id, req.Params.Revision)
	})
	if err != nil {
		return nil, err
	}
	return DeletePolicyGroup204Response{}, nil
}

func (h *handlers) GetGlobalSafeSearch(ctx context.Context, _ GetGlobalSafeSearchRequestObject) (GetGlobalSafeSearchResponseObject, error) {
	s, err := store.GetGlobalSafeSearch(ctx, h.d.Store.Pool)
	if err != nil {
		return nil, err
	}
	return GetGlobalSafeSearch200JSONResponse(globalSafeSearchOut(s)), nil
}

func (h *handlers) UpdateGlobalSafeSearch(ctx context.Context, req UpdateGlobalSafeSearchRequestObject) (UpdateGlobalSafeSearchResponseObject, error) {
	b := req.Body
	ss, err := safeSearchFields(b.Google, b.Bing, b.Duckduckgo, string(b.Youtube))
	if err != nil {
		// updateGlobalSafeSearch declares no 422: an out-of-enum youtube is a malformed request.
		return nil, invalid("%s", err.Error())
	}
	var after GlobalSafeSearch
	err = h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := store.GetGlobalSafeSearch(ctx, tx)
		if err != nil {
			return auth.Change{}, err
		}
		updated, err := store.UpdateGlobalSafeSearch(ctx, tx, store.GlobalSafeSearch{SafeSearch: ss, Revision: b.Revision})
		after = globalSafeSearchOut(updated)
		return auth.Change{Action: "updateGlobalSafeSearch", TargetType: "safe_search", TargetID: "global",
			Before: globalSafeSearchOut(before), After: after}, err
	})
	if err != nil {
		return nil, err
	}
	return UpdateGlobalSafeSearch200JSONResponse(after), nil
}

// checkPolicyGroupScope requires the policy group's engine group to exist and each selected filter
// list to be global or in that engine group.
func checkPolicyGroupScope(ctx context.Context, tx pgx.Tx, g store.PolicyGroup) error {
	if err := requireEngineGroup(ctx, tx, g.EngineGroupID); err != nil {
		return err
	}
	if len(g.FilterListIDs) == 0 {
		return nil
	}
	var name string
	err := tx.QueryRow(ctx, `select name from filter_lists where id = any($1) and engine_group_id is not null
		and engine_group_id is distinct from $2 order by name limit 1`, g.FilterListIDs, g.EngineGroupID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return coded(http.StatusUnprocessableEntity, "engine_group_scope",
		"filter list %q belongs to another engine group; a policy group may select only global lists or lists of its own engine group", name)
}
