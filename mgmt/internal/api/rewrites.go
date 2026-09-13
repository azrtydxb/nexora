package api

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

func rewriteOut(r store.Rewrite) Rewrite {
	return Rewrite{Id: r.ID, GroupId: r.GroupID, EngineGroupId: r.EngineGroupID, Name: r.Name, Type: RewriteType(r.Type), Value: r.Value, Ttl: int(r.TTL),
		Revision: r.Revision, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

func (h *handlers) ListRewrites(ctx context.Context, req ListRewritesRequestObject) (ListRewritesResponseObject, error) {
	groupID, all, err := rewriteScope(req.Params.Scope)
	if _, e, ok := policyFailure(err); ok {
		return ListRewrites422JSONResponse{ErrorJSONResponse(e)}, nil
	}
	rewrites, err := store.ListRewrites(ctx, h.d.Store.Pool, groupID, all)
	if err != nil {
		return nil, err
	}
	out := make(ListRewrites200JSONResponse, 0, len(rewrites))
	for _, r := range rewrites {
		out = append(out, rewriteOut(r))
	}
	return out, nil
}

func (h *handlers) CreateRewrite(ctx context.Context, req CreateRewriteRequestObject) (CreateRewriteResponseObject, error) {
	r, err := validateRewrite(*req.Body)
	var after Rewrite
	if err == nil {
		err = h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
			if err := requireEngineGroup(ctx, tx, r.EngineGroupID); err != nil {
				return auth.Change{}, err
			}
			created, err := store.CreateRewrite(ctx, tx, r)
			after = rewriteOut(created)
			return auth.Change{Action: "createRewrite", TargetType: "rewrite", TargetID: created.ID.String(), After: after}, err
		})
	}
	if status, e, ok := policyFailure(err); ok {
		if status == http.StatusConflict {
			return CreateRewrite409JSONResponse{ErrorJSONResponse(e)}, nil
		}
		return CreateRewrite422JSONResponse(e), nil
	}
	if err != nil {
		return nil, err
	}
	return CreateRewrite201JSONResponse(after), nil
}

func (h *handlers) UpdateRewrite(ctx context.Context, req UpdateRewriteRequestObject) (UpdateRewriteResponseObject, error) {
	b := req.Body
	r, err := validateRewrite(RewriteInput{GroupId: b.GroupId, EngineGroupId: b.EngineGroupId, Name: b.Name, Type: RewriteInputType(b.Type), Value: b.Value, Ttl: b.Ttl})
	var after Rewrite
	if err == nil {
		r.ID, r.Revision = req.Id, b.Revision
		err = h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
			before, err := store.GetRewrite(ctx, tx, req.Id)
			if err != nil {
				return auth.Change{}, err
			}
			if err := requireEngineGroup(ctx, tx, r.EngineGroupID); err != nil {
				return auth.Change{}, err
			}
			updated, err := store.UpdateRewrite(ctx, tx, r)
			after = rewriteOut(updated)
			return auth.Change{Action: "updateRewrite", TargetType: "rewrite", TargetID: req.Id.String(), Before: rewriteOut(before), After: after}, err
		})
	}
	if status, e, ok := policyFailure(err); ok {
		if status == http.StatusConflict {
			return UpdateRewrite409JSONResponse(e), nil
		}
		return UpdateRewrite422JSONResponse(e), nil
	}
	if err != nil {
		return nil, err
	}
	return UpdateRewrite200JSONResponse(after), nil
}

func (h *handlers) DeleteRewrite(ctx context.Context, req DeleteRewriteRequestObject) (DeleteRewriteResponseObject, error) {
	err := h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := store.GetRewrite(ctx, tx, req.Id)
		if err != nil {
			return auth.Change{}, err
		}
		return auth.Change{Action: "deleteRewrite", TargetType: "rewrite", TargetID: req.Id.String(), Before: rewriteOut(before)},
			store.DeleteRewrite(ctx, tx, req.Id, req.Params.Revision)
	})
	if err != nil {
		return nil, err
	}
	return DeleteRewrite204Response{}, nil
}
