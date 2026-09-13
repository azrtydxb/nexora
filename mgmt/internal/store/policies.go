package store

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Policy sentinel errors. They are returned bare (never wrapping a *pgconn.PgError) so MapError
// leaves them intact.
var (
	ErrCIDRInUse            = errors.New("cidr already belongs to a policy group")
	ErrUnknownFilterList    = errors.New("unknown block filter list")
	ErrUnknownGroup         = errors.New("unknown policy group")
	ErrRewriteCNAMEConflict = errors.New("a CNAME rewrite cannot coexist with other rewrites for the same name")
)

// PolicyQuerier is satisfied by *pgxpool.Pool, *pgx.Conn and pgx.Tx.
type PolicyQuerier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// SafeSearch selects the provider safe-search rewrites. YouTube is "off", "moderate" or "strict".
type SafeSearch struct {
	Google, Bing, DuckDuckGo bool
	YouTube                  string
}

// PolicyGroup is a client group selected by source CIDR.
type PolicyGroup struct {
	ID                   uuid.UUID
	EngineGroupID        *uuid.UUID // nil: every engine group
	Name, Description    string
	CIDRs                []netip.Prefix
	FilterListIDs        []uuid.UUID
	Allowlist            []string
	SafeSearch           SafeSearch
	Revision             int64
	CreatedAt, UpdatedAt time.Time
}

// GlobalSafeSearch is the safe-search selection for clients in no group.
type GlobalSafeSearch struct {
	SafeSearch
	Revision int64
}

const selectPolicyGroups = `select g.id, g.engine_group_id, g.name, g.description, g.safe_search_google, g.safe_search_bing,
	g.safe_search_duckduckgo, g.safe_search_youtube, g.revision, g.created_at, g.updated_at,
	array(select c.cidr from policy_group_cidrs c where c.group_id = g.id order by c.cidr),
	array(select l.filter_list_id from policy_group_filter_lists l where l.group_id = g.id order by l.filter_list_id),
	array(select a.domain from policy_group_allowlist a where a.group_id = g.id order by a.domain)
	from policy_groups g`

func scanPolicyGroup(row pgx.Row) (PolicyGroup, error) {
	var g PolicyGroup
	err := row.Scan(&g.ID, &g.EngineGroupID, &g.Name, &g.Description, &g.SafeSearch.Google, &g.SafeSearch.Bing, &g.SafeSearch.DuckDuckGo,
		&g.SafeSearch.YouTube, &g.Revision, &g.CreatedAt, &g.UpdatedAt, &g.CIDRs, &g.FilterListIDs, &g.Allowlist)
	return g, err
}

// ListPolicyGroups returns every group ordered by name.
func ListPolicyGroups(ctx context.Context, q PolicyQuerier) ([]PolicyGroup, error) {
	rows, err := q.Query(ctx, selectPolicyGroups+" order by g.name")
	if err != nil {
		return nil, MapError(err)
	}
	defer rows.Close()
	groups := []PolicyGroup{}
	for rows.Next() {
		g, err := scanPolicyGroup(rows)
		if err != nil {
			return nil, MapError(err)
		}
		groups = append(groups, g)
	}
	return groups, MapError(rows.Err())
}

// GetPolicyGroup returns one group or ErrNotFound.
func GetPolicyGroup(ctx context.Context, q PolicyQuerier, id uuid.UUID) (PolicyGroup, error) {
	g, err := scanPolicyGroup(q.QueryRow(ctx, selectPolicyGroups+" where g.id = $1", id))
	return g, MapError(err)
}

// CreatePolicyGroup inserts g and its CIDRs, filter lists and allowlist.
func CreatePolicyGroup(ctx context.Context, tx pgx.Tx, g PolicyGroup) (PolicyGroup, error) {
	ss := normalSafeSearch(g.SafeSearch)
	err := tx.QueryRow(ctx, `insert into policy_groups(name, description, safe_search_google, safe_search_bing,
		safe_search_duckduckgo, safe_search_youtube) values ($1, $2, $3, $4, $5, $6) returning id`,
		g.Name, g.Description, ss.Google, ss.Bing, ss.DuckDuckGo, ss.YouTube).Scan(&g.ID)
	if err != nil {
		return PolicyGroup{}, policyError(err)
	}
	if err := insertGroupChildren(ctx, tx, g); err != nil {
		return PolicyGroup{}, err
	}
	return GetPolicyGroup(ctx, tx, g.ID)
}

// UpdatePolicyGroup replaces g using g.Revision as the expected revision.
func UpdatePolicyGroup(ctx context.Context, tx pgx.Tx, g PolicyGroup) (PolicyGroup, error) {
	ss := normalSafeSearch(g.SafeSearch)
	var rev int64
	err := tx.QueryRow(ctx, `update policy_groups set name = $2, description = $3, safe_search_google = $4,
		safe_search_bing = $5, safe_search_duckduckgo = $6, safe_search_youtube = $7, revision = revision + 1,
		updated_at = now() where id = $1 and revision = $8 returning revision`,
		g.ID, g.Name, g.Description, ss.Google, ss.Bing, ss.DuckDuckGo, ss.YouTube, g.Revision).Scan(&rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return PolicyGroup{}, missingOrStale(ctx, tx, "policy_groups", g.ID)
	}
	if err != nil {
		return PolicyGroup{}, policyError(err)
	}
	for _, table := range []string{"policy_group_cidrs", "policy_group_filter_lists", "policy_group_allowlist"} {
		if _, err := tx.Exec(ctx, "delete from "+table+" where group_id = $1", g.ID); err != nil {
			return PolicyGroup{}, err
		}
	}
	if err := insertGroupChildren(ctx, tx, g); err != nil {
		return PolicyGroup{}, err
	}
	return GetPolicyGroup(ctx, tx, g.ID)
}

// DeletePolicyGroup deletes the group (and, by cascade, its rewrites) at the expected revision.
func DeletePolicyGroup(ctx context.Context, tx pgx.Tx, id uuid.UUID, revision int64) error {
	tag, err := tx.Exec(ctx, "delete from policy_groups where id = $1 and revision = $2", id, revision)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return missingOrStale(ctx, tx, "policy_groups", id)
	}
	return nil
}

// GetGlobalSafeSearch returns the safe-search selection for clients in no group.
func GetGlobalSafeSearch(ctx context.Context, q PolicyQuerier) (GlobalSafeSearch, error) {
	var s GlobalSafeSearch
	err := q.QueryRow(ctx, "select google, bing, duckduckgo, youtube, revision from global_safe_search").
		Scan(&s.Google, &s.Bing, &s.DuckDuckGo, &s.YouTube, &s.Revision)
	return s, MapError(err)
}

// UpdateGlobalSafeSearch stores s using s.Revision as the expected revision.
func UpdateGlobalSafeSearch(ctx context.Context, tx pgx.Tx, s GlobalSafeSearch) (GlobalSafeSearch, error) {
	s.SafeSearch = normalSafeSearch(s.SafeSearch)
	err := tx.QueryRow(ctx, `update global_safe_search set google = $1, bing = $2, duckduckgo = $3, youtube = $4,
		revision = revision + 1, updated_at = now() where revision = $5 returning revision`,
		s.Google, s.Bing, s.DuckDuckGo, s.YouTube, s.Revision).Scan(&s.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return GlobalSafeSearch{}, fmt.Errorf("%w: safe search revision %d is stale; reload and retry", ErrConflict, s.Revision)
	}
	return s, err
}

func normalSafeSearch(s SafeSearch) SafeSearch {
	if s.YouTube == "" {
		s.YouTube = "off"
	}
	return s
}

func insertGroupChildren(ctx context.Context, tx pgx.Tx, g PolicyGroup) error {
	for _, c := range g.CIDRs {
		if _, err := tx.Exec(ctx, "insert into policy_group_cidrs(cidr, group_id) values ($1, $2)", c, g.ID); err != nil {
			return policyError(err)
		}
	}
	for _, id := range g.FilterListIDs {
		// Only block lists: an allow-kind list would silently never reach the group's blocklists.
		tag, err := tx.Exec(ctx, `insert into policy_group_filter_lists(group_id, filter_list_id)
			select $1, id from filter_lists where id = $2 and kind = 'block'`, g.ID, id)
		if err != nil {
			return policyError(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrUnknownFilterList
		}
	}
	for _, d := range g.Allowlist {
		if _, err := tx.Exec(ctx, "insert into policy_group_allowlist(group_id, domain) values ($1, $2)", g.ID, d); err != nil {
			return policyError(err)
		}
	}
	return nil
}

// missingOrStale tells a zero-row revision-checked write on table apart: ErrNotFound when the row
// is gone, ErrConflict when its revision moved on.
func missingOrStale(ctx context.Context, q PolicyQuerier, table string, id uuid.UUID) error {
	var one int
	err := q.QueryRow(ctx, "select 1 from "+table+" where id = $1", id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: revision is stale; reload and retry", ErrConflict)
}

// policyError maps constraint violations of the policy tables to their sentinel errors.
func policyError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch {
	case pgErr.Code == "23505" && pgErr.ConstraintName == "policy_group_cidrs_pkey":
		return ErrCIDRInUse
	case pgErr.Code == "23505" && pgErr.ConstraintName == "policy_groups_name_key":
		return fmt.Errorf("%w: name already used", ErrConflict)
	case pgErr.Code == "23503" && pgErr.ConstraintName == "policy_group_filter_lists_filter_list_id_fkey":
		return ErrUnknownFilterList
	case pgErr.Code == "23503" && pgErr.ConstraintName == "rewrites_group_id_fkey":
		return ErrUnknownGroup
	}
	return err
}
