package api

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// audited runs fn in a transaction and writes its change to the audit log (no config version).
func (h *handlers) audited(ctx context.Context, fn func(tx pgx.Tx) (auth.Change, error)) error {
	return h.d.Store.InTx(ctx, func(tx pgx.Tx) error {
		c, err := fn(tx)
		if err != nil {
			return err
		}
		return auth.WriteAudit(ctx, tx, PrincipalFrom(ctx).Actor(), c, nil)
	})
}

// ---- users ----

func (h *handlers) ListUsers(ctx context.Context, _ ListUsersRequestObject) (ListUsersResponseObject, error) {
	rows, err := h.d.Store.Pool.Query(ctx, "select "+auth.UserColumns+" from users order by username")
	if err != nil {
		return nil, store.MapError(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (User, error) {
		u, err := auth.ScanUser(r)
		if err != nil {
			return User{}, err
		}
		return apiUser(u), nil
	})
	if err != nil {
		return nil, store.MapError(err)
	}
	return ListUsers200JSONResponse(out), nil
}

func (h *handlers) CreateUser(ctx context.Context, req CreateUserRequestObject) (CreateUserResponseObject, error) {
	if err := validateUsername(req.Body.Username); err != nil {
		return nil, err
	}
	if !req.Body.Role.Valid() {
		return nil, invalid("role must be viewer, operator or admin")
	}
	var u auth.User
	err := h.audited(ctx, func(tx pgx.Tx) (auth.Change, error) {
		var err error
		u, err = auth.CreateUser(ctx, tx, req.Body.Username, strings.TrimSpace(req.Body.Email), req.Body.Password, auth.Role(req.Body.Role))
		if err != nil {
			return auth.Change{}, err
		}
		return auth.Change{Action: "createUser", TargetType: "user", TargetID: u.ID, After: apiUser(u)}, nil
	})
	if err != nil {
		return nil, err
	}
	return CreateUser201JSONResponse(apiUser(u)), nil
}

func lockUser(ctx context.Context, tx pgx.Tx, id uuid.UUID, revision int64) (auth.User, error) {
	u, err := auth.ScanUser(tx.QueryRow(ctx, "select "+auth.UserColumns+" from users where id = $1 for update", id))
	if err != nil {
		return u, store.MapError(err)
	}
	return u, checkRevision(u.Revision, revision)
}

// ensureOtherAdmin refuses to remove the last enabled admin. Admin rows are locked so two
// concurrent demotions cannot both pass.
func ensureOtherAdmin(ctx context.Context, tx pgx.Tx, id string) error {
	rows, err := tx.Query(ctx, "select id from users where role = 'admin' and not disabled and id <> $1 for update", id)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return fmt.Errorf("%w: the last enabled admin cannot be demoted, disabled or deleted", store.ErrConflict)
	}
	return nil
}

func (h *handlers) UpdateUser(ctx context.Context, req UpdateUserRequestObject) (UpdateUserResponseObject, error) {
	in := *req.Body
	if !in.Role.Valid() {
		return nil, invalid("role must be viewer, operator or admin")
	}
	var hash *string
	if in.Password != nil {
		if len(*in.Password) < auth.MinPasswordLength {
			return nil, auth.ErrWeakPassword
		}
		hashed, err := auth.HashPassword(*in.Password)
		if err != nil {
			return nil, err
		}
		hash = &hashed
	}
	var after auth.User
	err := h.audited(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := lockUser(ctx, tx, req.Id, in.Revision)
		if err != nil {
			return auth.Change{}, err
		}
		if hash != nil && before.Source != "local" {
			return auth.Change{}, invalid("passwords can only be set for local users")
		}
		if before.Role == auth.RoleAdmin && !before.Disabled && (in.Role != RoleAdmin || in.Disabled) {
			if err := ensureOtherAdmin(ctx, tx, before.ID); err != nil {
				return auth.Change{}, err
			}
		}
		after, err = auth.ScanUser(tx.QueryRow(ctx, `update users set email = $2, role = $3, disabled = $4,
			password_hash = coalesce($5, password_hash), revision = revision + 1, updated_at = now()
			where id = $1 returning `+auth.UserColumns, req.Id, strings.TrimSpace(in.Email), string(in.Role), in.Disabled, hash))
		if err != nil {
			return auth.Change{}, err
		}
		// Disabling a user or changing its password ends its existing sessions.
		if in.Disabled || hash != nil {
			if _, err := tx.Exec(ctx, "delete from sessions where user_id = $1", req.Id); err != nil {
				return auth.Change{}, err
			}
		}
		return auth.Change{Action: "updateUser", TargetType: "user", TargetID: before.ID, Before: apiUser(before), After: apiUser(after)}, nil
	})
	if err != nil {
		return nil, err
	}
	return UpdateUser200JSONResponse(apiUser(after)), nil
}

func (h *handlers) DeleteUser(ctx context.Context, req DeleteUserRequestObject) (DeleteUserResponseObject, error) {
	err := h.audited(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := lockUser(ctx, tx, req.Id, req.Params.Revision)
		if err != nil {
			return auth.Change{}, err
		}
		if before.Role == auth.RoleAdmin && !before.Disabled {
			if err := ensureOtherAdmin(ctx, tx, before.ID); err != nil {
				return auth.Change{}, err
			}
		}
		_, err = tx.Exec(ctx, "delete from users where id = $1", req.Id)
		return auth.Change{Action: "deleteUser", TargetType: "user", TargetID: before.ID, Before: apiUser(before)}, err
	})
	if err != nil {
		return nil, err
	}
	return DeleteUser204Response{}, nil
}

// ---- API tokens ----

const apiTokenColumns = "id::text, user_id::text, name, prefix, role, created_at, expires_at, last_used_at, revoked_at"

func scanAPIToken(row pgx.Row) (ApiToken, error) {
	var t ApiToken
	var id, userID, role string
	if err := row.Scan(&id, &userID, &t.Name, &t.Prefix, &role, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &t.RevokedAt); err != nil {
		return t, store.MapError(err)
	}
	t.Id, t.UserId, t.Role = uuid.MustParse(id), uuid.MustParse(userID), Role(role)
	return t, nil
}

func (h *handlers) ListApiTokens(ctx context.Context, _ ListApiTokensRequestObject) (ListApiTokensResponseObject, error) {
	rows, err := h.d.Store.Pool.Query(ctx, "select "+apiTokenColumns+" from api_tokens order by created_at desc")
	if err != nil {
		return nil, store.MapError(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ApiToken, error) { return scanAPIToken(r) })
	if err != nil {
		return nil, store.MapError(err)
	}
	return ListApiTokens200JSONResponse(out), nil
}

func (h *handlers) CreateApiToken(ctx context.Context, req CreateApiTokenRequestObject) (CreateApiTokenResponseObject, error) {
	name := strings.TrimSpace(req.Body.Name)
	if len(name) < 1 || len(name) > 64 {
		return nil, invalid("name must be 1-64 characters")
	}
	if !req.Body.Role.Valid() {
		return nil, invalid("role must be viewer, operator or admin")
	}
	if req.Body.ExpiresAt != nil && !req.Body.ExpiresAt.After(time.Now()) {
		return nil, invalid("expires_at must be in the future")
	}
	var created ApiTokenCreated
	err := h.audited(ctx, func(tx pgx.Tx) (auth.Change, error) {
		t, token, err := h.d.Auth.CreateAPIToken(ctx, tx, PrincipalFrom(ctx), name, auth.Role(req.Body.Role), req.Body.ExpiresAt)
		if err != nil {
			return auth.Change{}, err
		}
		created = ApiTokenCreated{Token: token, ApiToken: ApiToken{Id: uuid.MustParse(t.ID), UserId: uuid.MustParse(t.UserID),
			Name: t.Name, Prefix: t.Prefix, Role: Role(t.Role), CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt}}
		return auth.Change{Action: "createApiToken", TargetType: "api_token", TargetID: t.ID, After: created.ApiToken}, nil
	})
	if err != nil {
		return nil, err
	}
	return CreateApiToken201JSONResponse(created), nil
}

func (h *handlers) RevokeApiToken(ctx context.Context, req RevokeApiTokenRequestObject) (RevokeApiTokenResponseObject, error) {
	err := h.audited(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := scanAPIToken(tx.QueryRow(ctx, "select "+apiTokenColumns+" from api_tokens where id = $1 for update", req.Id))
		if err != nil {
			return auth.Change{}, err
		}
		after, err := scanAPIToken(tx.QueryRow(ctx, `update api_tokens set revoked_at = coalesce(revoked_at, now())
			where id = $1 returning `+apiTokenColumns, req.Id))
		return auth.Change{Action: "revokeApiToken", TargetType: "api_token", TargetID: req.Id.String(), Before: before, After: after}, err
	})
	if err != nil {
		return nil, err
	}
	return RevokeApiToken204Response{}, nil
}

// ---- audit and query log ----

func (h *handlers) ListAuditEvents(ctx context.Context, req ListAuditEventsRequestObject) (ListAuditEventsResponseObject, error) {
	limit, err := limitParam(req.Params.Limit, 100, 500)
	if err != nil {
		return nil, err
	}
	rows, err := h.d.Store.Pool.Query(ctx, `select id, at, actor_type, actor_id, actor_name, action, target_type, target_id,
		diff, config_version from audit_log where $1::bigint is null or id < $1 order by id desc limit $2`,
		req.Params.BeforeId, limit)
	if err != nil {
		return nil, store.MapError(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (AuditEvent, error) {
		var a AuditEvent
		var actorType string
		err := r.Scan(&a.Id, &a.At, &actorType, &a.ActorId, &a.ActorName, &a.Action, &a.TargetType, &a.TargetId, &a.Diff, &a.ConfigVersion)
		a.ActorType = AuditEventActorType(actorType)
		return a, err
	})
	if err != nil {
		return nil, store.MapError(err)
	}
	return ListAuditEvents200JSONResponse(out), nil
}

func (h *handlers) SearchQueryLog(ctx context.Context, req SearchQueryLogRequestObject) (SearchQueryLogResponseObject, error) {
	p := req.Params
	limit, err := limitParam(p.Limit, 100, 1000)
	if err != nil {
		return nil, err
	}
	q := querylog.Query{Client: deref(p.Client), Name: deref(p.Name), Limit: limit, Cursor: deref(p.Cursor)}
	if p.From != nil {
		q.From = *p.From
	}
	if p.To != nil {
		q.To = *p.To
	}
	for _, f := range []struct {
		name  string
		dst   *[]string
		src   []string
		valid func(string) bool
	}{
		{"qtype", &q.QTypes, strs(p.Qtype), nil},
		{"rcode", &q.RCodes, strs(p.Rcode), nil},
		{"cache", &q.Caches, strs(p.Cache), func(v string) bool { return SearchQueryLogParamsCache(v).Valid() }},
		{"filter", &q.Filters, strs(p.Filter), func(v string) bool { return SearchQueryLogParamsFilter(v).Valid() }},
		{"category", &q.Categories, strs(p.Category), nil},
		{"source", &q.Sources, strs(p.Source), func(v string) bool { return SearchQueryLogParamsSource(v).Valid() }},
		{"list_id", &q.ListIDs, strs(p.ListId), nil},
		{"policy_group", &q.PolicyGroups, strs(p.PolicyGroup), nil},
		{"engine_id", &q.EngineIDs, strs(p.EngineId), nil},
	} {
		// The cap applies to the values as sent, before duplicates are dropped.
		if len(f.src) > maxQueryLogValues {
			return nil, invalid("at most %d values per parameter", maxQueryLogValues)
		}
		f.src = paramValues(f.src)
		for _, v := range f.src {
			if f.valid != nil && !f.valid(v) {
				return nil, invalid("unknown %s value %q", f.name, v)
			}
		}
		*f.dst = f.src
	}
	page, err := h.d.QueryLog.Search(ctx, q)
	if err != nil {
		return nil, err
	}
	records, err := resolveRecordNames(ctx, h.d.Store.Pool, h.d.Catalog, page.Records, h.d.AI != nil)
	if err != nil {
		return nil, err
	}
	return SearchQueryLog200JSONResponse(QueryLogPage{Backend: h.d.QueryLog.Name(), NextCursor: page.NextCursor, Records: records}), nil
}

// maxQueryLogValues caps the values of one repeated query-log parameter.
const maxQueryLogValues = 32

// strs is a repeated query parameter as strings; nil when absent.
func strs[T ~string](v *[]T) []string {
	if v == nil {
		return nil
	}
	out := make([]string, len(*v))
	for i, s := range *v {
		out[i] = string(s)
	}
	return out
}

// paramValues trims values and drops empty ones and duplicates.
func paramValues(values []string) []string {
	var out []string
	for _, raw := range values {
		if s := strings.TrimSpace(raw); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}
