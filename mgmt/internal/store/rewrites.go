package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Rewrite is a custom DNS answer. GroupID nil means global. Type is "A", "AAAA" or "CNAME".
type Rewrite struct {
	ID                   uuid.UUID
	GroupID              *uuid.UUID
	EngineGroupID        *uuid.UUID // global rewrites only; a policy group rewrite follows its policy group
	Name, Type, Value    string
	TTL                  int32
	Revision             int64
	CreatedAt, UpdatedAt time.Time
}

const rewriteColumns = "id, group_id, engine_group_id, name, type, value, ttl, revision, created_at, updated_at"

const selectRewrites = "select " + rewriteColumns + " from rewrites"

func scanRewrite(row pgx.Row) (Rewrite, error) {
	var r Rewrite
	err := row.Scan(&r.ID, &r.GroupID, &r.EngineGroupID, &r.Name, &r.Type, &r.Value, &r.TTL, &r.Revision, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// ListRewrites returns every rewrite when allScopes, else the global ones (groupID nil) or one
// group's, ordered by scope, name, type, value.
func ListRewrites(ctx context.Context, q PolicyQuerier, groupID *uuid.UUID, allScopes bool) ([]Rewrite, error) {
	sql, args := selectRewrites, []any{}
	switch {
	case allScopes:
	case groupID == nil:
		sql += " where group_id is null"
	default:
		sql += " where group_id = $1"
		args = append(args, *groupID)
	}
	rows, err := q.Query(ctx, sql+" order by group_id nulls first, name, type, value", args...)
	if err != nil {
		return nil, MapError(err)
	}
	defer rows.Close()
	out := []Rewrite{}
	for rows.Next() {
		r, err := scanRewrite(rows)
		if err != nil {
			return nil, MapError(err)
		}
		out = append(out, r)
	}
	return out, MapError(rows.Err())
}

// GetRewrite returns one rewrite or ErrNotFound.
func GetRewrite(ctx context.Context, q PolicyQuerier, id uuid.UUID) (Rewrite, error) {
	r, err := scanRewrite(q.QueryRow(ctx, selectRewrites+" where id = $1", id))
	return r, MapError(err)
}

// CreateRewrite inserts r.
func CreateRewrite(ctx context.Context, tx pgx.Tx, r Rewrite) (Rewrite, error) {
	if err := checkCNAME(ctx, tx, r); err != nil {
		return Rewrite{}, err
	}
	created, err := scanRewrite(tx.QueryRow(ctx, `insert into rewrites(group_id, name, type, value, ttl) values ($1, $2, $3, $4, $5)
		returning `+rewriteColumns, r.GroupID, r.Name, r.Type, r.Value, r.TTL))
	if err != nil {
		return Rewrite{}, policyError(err)
	}
	return created, nil
}

// UpdateRewrite replaces r using r.Revision as the expected revision.
func UpdateRewrite(ctx context.Context, tx pgx.Tx, r Rewrite) (Rewrite, error) {
	if err := checkCNAME(ctx, tx, r); err != nil {
		return Rewrite{}, err
	}
	updated, err := scanRewrite(tx.QueryRow(ctx, `update rewrites set group_id = $2, name = $3, type = $4, value = $5, ttl = $6,
		revision = revision + 1, updated_at = now() where id = $1 and revision = $7
		returning `+rewriteColumns,
		r.ID, r.GroupID, r.Name, r.Type, r.Value, r.TTL, r.Revision))
	if errors.Is(err, pgx.ErrNoRows) {
		return Rewrite{}, missingOrStale(ctx, tx, "rewrites", r.ID)
	}
	if err != nil {
		return Rewrite{}, policyError(err)
	}
	return updated, nil
}

// DeleteRewrite deletes the rewrite at the expected revision.
func DeleteRewrite(ctx context.Context, tx pgx.Tx, id uuid.UUID, revision int64) error {
	tag, err := tx.Exec(ctx, "delete from rewrites where id = $1 and revision = $2", id, revision)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return missingOrStale(ctx, tx, "rewrites", id)
	}
	return nil
}

// checkCNAME refuses a CNAME beside any other rewrite of the same scope and name, and any rewrite
// beside an existing CNAME. The advisory lock serialises concurrent writers of one scope and name,
// which row locks alone cannot (the conflicting row may not exist yet).
func checkCNAME(ctx context.Context, tx pgx.Tx, r Rewrite) error {
	scope := ""
	if r.GroupID != nil {
		scope = r.GroupID.String()
	}
	if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtext('nexora:rewrite:' || $1 || ':' || $2))", scope, r.Name); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, "select type from rewrites where group_id is not distinct from $1 and name = $2 and id <> $3 for update",
		r.GroupID, r.Name, r.ID)
	if err != nil {
		return err
	}
	types, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, t := range types {
		if r.Type == "CNAME" || t == "CNAME" {
			return ErrRewriteCNAMEConflict
		}
	}
	return nil
}
