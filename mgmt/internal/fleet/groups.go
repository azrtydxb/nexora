package fleet

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Engine group errors.
var (
	ErrEngineGroupNotEmpty  = errors.New("engine group is not empty")
	ErrEngineGroupProtected = errors.New("the default engine group cannot be renamed or deleted")
	ErrEngineGroupNameTaken = errors.New("engine group name already exists")
)

// LabelKeyRE is the engine label key rule of docs/architecture.md ("Engine groups and scoping").
var LabelKeyRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9./-]{0,61}[a-z0-9])?$`)

// EngineGroupNameRE is the engine_groups.name CHECK constraint.
var EngineGroupNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

const (
	maxLabels        = 32
	maxLabelValueLen = 63
)

// ValidateLabels checks engine or join token labels; the error names the offending key.
func ValidateLabels(labels map[string]string) error {
	if len(labels) > maxLabels {
		return fmt.Errorf("at most %d labels are allowed, got %d", maxLabels, len(labels))
	}
	for k, v := range labels {
		if !LabelKeyRE.MatchString(k) {
			return fmt.Errorf("label key %q must be 1-63 characters of a-z, 0-9, '.', '/', '-' starting and ending with a-z or 0-9", k)
		}
		if len(v) > maxLabelValueLen {
			return fmt.Errorf("label %q value must be at most %d characters", k, maxLabelValueLen)
		}
	}
	return nil
}

// EngineGroup is one engine_groups row with the number of its non-deleted engines.
type EngineGroup struct {
	ID                                            uuid.UUID
	Name, Description, UpstreamMode, OTLPEndpoint string
	RolloutStrategy                               string
	ExtraACLCIDRs                                 []string
	CanaryCount, CanaryPercent                    int
	AckTimeoutSeconds, HealthWindowSeconds        int
	MaxServfailRatio                              float64
	MinHealthQueries                              int
	RolloutsPaused                                bool
	StableVersion                                 *uint64
	Revision                                      int64
	EngineCount                                   int
	CreatedAt, UpdatedAt                          time.Time
}

const engineGroupColumns = `g.id, g.name, g.description, g.upstream_mode, g.otlp_endpoint, g.rollout_strategy,
	array(select host(c) || '/' || masklen(c) from unnest(g.extra_acl_cidrs) with ordinality as u(c, n) order by n),
	g.canary_count, g.canary_percent, g.ack_timeout_seconds, g.health_window_seconds, g.max_servfail_ratio,
	g.min_health_queries, g.rollouts_paused, g.stable_version, g.revision,
	(select count(*) from engines e where e.engine_group_id = g.id and e.deleted_at is null), g.created_at, g.updated_at`

func scanEngineGroup(row pgx.Row) (EngineGroup, error) {
	var g EngineGroup
	var stable *int64
	err := row.Scan(&g.ID, &g.Name, &g.Description, &g.UpstreamMode, &g.OTLPEndpoint, &g.RolloutStrategy, &g.ExtraACLCIDRs,
		&g.CanaryCount, &g.CanaryPercent, &g.AckTimeoutSeconds, &g.HealthWindowSeconds, &g.MaxServfailRatio,
		&g.MinHealthQueries, &g.RolloutsPaused, &stable, &g.Revision, &g.EngineCount, &g.CreatedAt, &g.UpdatedAt)
	if stable != nil {
		v := uint64(*stable)
		g.StableVersion = &v
	}
	return g, err
}

// ListEngineGroups returns every engine group, the default group first, then by name.
func ListEngineGroups(ctx context.Context, q store.PolicyQuerier) ([]EngineGroup, error) {
	rows, err := q.Query(ctx, "select "+engineGroupColumns+" from engine_groups g order by (g.id <> $1), g.name", store.DefaultEngineGroupID)
	if err != nil {
		return nil, store.MapError(err)
	}
	groups, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (EngineGroup, error) { return scanEngineGroup(r) })
	return groups, store.MapError(err)
}

// GetEngineGroup returns one engine group or store.ErrNotFound.
func GetEngineGroup(ctx context.Context, q store.PolicyQuerier, id uuid.UUID) (EngineGroup, error) {
	g, err := scanEngineGroup(q.QueryRow(ctx, "select "+engineGroupColumns+" from engine_groups g where g.id = $1", id))
	return g, store.MapError(err)
}

// CreateEngineGroup inserts g (ID, Revision, counts and timestamps are assigned).
func CreateEngineGroup(ctx context.Context, tx pgx.Tx, g EngineGroup) (EngineGroup, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `insert into engine_groups (name, description, upstream_mode, extra_acl_cidrs, otlp_endpoint,
		rollout_strategy, canary_count, canary_percent, ack_timeout_seconds, health_window_seconds, max_servfail_ratio, min_health_queries)
		values ($1, $2, $3, $4::cidr[], $5, $6, $7, $8, $9, $10, $11, $12) returning id`,
		g.Name, g.Description, g.UpstreamMode, nonNil(g.ExtraACLCIDRs), g.OTLPEndpoint, g.RolloutStrategy, g.CanaryCount, g.CanaryPercent,
		g.AckTimeoutSeconds, g.HealthWindowSeconds, g.MaxServfailRatio, g.MinHealthQueries).Scan(&id)
	if err != nil {
		return EngineGroup{}, groupError(err)
	}
	return GetEngineGroup(ctx, tx, id)
}

// UpdateEngineGroup stores g at the expected revision (store.ErrConflict when stale).
func UpdateEngineGroup(ctx context.Context, tx pgx.Tx, g EngineGroup, revision int64) (EngineGroup, error) {
	tag, err := tx.Exec(ctx, `update engine_groups set name = $2, description = $3, upstream_mode = $4, extra_acl_cidrs = $5::cidr[],
		otlp_endpoint = $6, rollout_strategy = $7, canary_count = $8, canary_percent = $9, ack_timeout_seconds = $10,
		health_window_seconds = $11, max_servfail_ratio = $12, min_health_queries = $13,
		revision = revision + 1, updated_at = now() where id = $1 and revision = $14`,
		g.ID, g.Name, g.Description, g.UpstreamMode, nonNil(g.ExtraACLCIDRs), g.OTLPEndpoint, g.RolloutStrategy, g.CanaryCount,
		g.CanaryPercent, g.AckTimeoutSeconds, g.HealthWindowSeconds, g.MaxServfailRatio, g.MinHealthQueries, revision)
	if err != nil {
		return EngineGroup{}, groupError(err)
	}
	if tag.RowsAffected() == 0 {
		if _, err := GetEngineGroup(ctx, tx, g.ID); err != nil {
			return EngineGroup{}, err
		}
		return EngineGroup{}, fmt.Errorf("%w: engine group revision %d is stale; reload and retry", store.ErrConflict, revision)
	}
	return GetEngineGroup(ctx, tx, g.ID)
}

// DeleteEngineGroup deletes an empty engine group at the expected revision. A group with engines,
// scoped configuration or usable join tokens is refused with ErrEngineGroupNotEmpty.
func DeleteEngineGroup(ctx context.Context, tx pgx.Tx, id uuid.UUID, revision int64) error {
	if id == store.DefaultEngineGroupID {
		return ErrEngineGroupProtected
	}
	g, err := scanEngineGroup(tx.QueryRow(ctx, "select "+engineGroupColumns+" from engine_groups g where g.id = $1 for update", id))
	if err != nil {
		return store.MapError(err)
	}
	if g.Revision != revision {
		return fmt.Errorf("%w: engine group revision %d is stale (current %d); reload and retry", store.ErrConflict, revision, g.Revision)
	}
	var scoped, tokens int
	for _, table := range store.EngineScopedTables {
		var n int
		if err := tx.QueryRow(ctx, "select count(*) from "+table+" where engine_group_id = $1", id).Scan(&n); err != nil {
			return err
		}
		scoped += n
	}
	if err := tx.QueryRow(ctx, `select count(*) from join_tokens where engine_group_id = $1 and revoked_at is null
		and expires_at > now() and (max_uses is null or uses < max_uses)`, id).Scan(&tokens); err != nil {
		return err
	}
	if g.EngineCount > 0 || scoped > 0 || tokens > 0 {
		return fmt.Errorf("%w: engine group has %d engines, %d scoped resources and %d active join tokens",
			ErrEngineGroupNotEmpty, g.EngineCount, scoped, tokens)
	}
	// Deleted engines keep their rows (audit history); they move to the default group so the
	// foreign key does not hold the group.
	if _, err := tx.Exec(ctx, "update engines set engine_group_id = $2 where engine_group_id = $1", id, store.DefaultEngineGroupID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "delete from engine_groups where id = $1", id)
	return err
}

func groupError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "engine_groups_name_key" {
		return ErrEngineGroupNameTaken
	}
	return store.MapError(err)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
