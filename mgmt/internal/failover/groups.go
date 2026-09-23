// Package failover owns desired availability groups, independently of DNS policy
// groups. Persisting a group never advertises an IP or makes a backend eligible.
package failover

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// ErrInvalid indicates an invalid desired group, not a transient backend outage.
var ErrInvalid = errors.New("invalid failover group")
var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Group is desired state. Generation is an optimistic concurrency token, NOT a
// frontend ownership lease. FrontendIP is immutable in Update.
type Group struct {
	ID                   uuid.UUID
	Name                 string
	FrontendIP           string
	Members              [2]uuid.UUID
	Generation           int64
	Lifecycle            string
	CreatedAt, UpdatedAt time.Time
}

// Validate rejects noncanonical addresses and unsupported families. The initial
// adapter contract is IPv4 only; IPv6 must be proven before accepting it here.
func (g Group) Validate() error {
	if !nameRE.MatchString(g.Name) {
		return fmt.Errorf("%w: invalid name", ErrInvalid)
	}
	ip, err := netip.ParseAddr(g.FrontendIP)
	if err != nil || !ip.Is4() || ip.String() != g.FrontendIP || !ip.IsGlobalUnicast() || ip.As4()[0] == 0 || ip.As4()[0] >= 224 {
		return fmt.Errorf("%w: frontend must be a canonical IPv4 unicast address", ErrInvalid)
	}
	if g.Members[0] == uuid.Nil || g.Members[1] == uuid.Nil || g.Members[0] == g.Members[1] {
		return fmt.Errorf("%w: two distinct persistent engine IDs are required", ErrInvalid)
	}
	return nil
}

const columns = `id, name, host(frontend_ip), member_a, member_b, generation, lifecycle, created_at, updated_at`

func scan(row pgx.Row) (Group, error) {
	var g Group
	err := row.Scan(&g.ID, &g.Name, &g.FrontendIP, &g.Members[0], &g.Members[1], &g.Generation, &g.Lifecycle, &g.CreatedAt, &g.UpdatedAt)
	return g, store.MapError(err)
}

// Get returns desired state, without implying operational health.
func Get(ctx context.Context, q store.PolicyQuerier, id uuid.UUID) (Group, error) {
	return scan(q.QueryRow(ctx, "select "+columns+" from public.failover_groups where id=$1", id))
}

// List returns non-deleted groups ordered by name; Get retains tombstone access.
func List(ctx context.Context, q store.PolicyQuerier) ([]Group, error) {
	rows, err := q.Query(ctx, "select "+columns+" from public.failover_groups where lifecycle <> 'deleted' order by name")
	if err != nil {
		return nil, store.MapError(err)
	}
	groups, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Group, error) { return scan(r) })
	return groups, store.MapError(err)
}

// Create assigns an ID and generation. Call within Store.InTx; audit belongs in
// that same transaction. A failed operation must roll back the transaction.
func Create(ctx context.Context, tx pgx.Tx, g Group) (Group, error) {
	if err := g.Validate(); err != nil {
		return Group{}, err
	}
	if err := validateMembers(ctx, tx, g.Members); err != nil {
		return Group{}, err
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx, `insert into public.failover_groups(name,frontend_ip,member_a,member_b)
 values($1,$2::inet,$3,$4) returning id`, g.Name, g.FrontendIP, g.Members[0], g.Members[1]).Scan(&id)
	if err != nil {
		return Group{}, store.MapError(err)
	}
	g.ID = id
	if err := insertMembers(ctx, tx, g); err != nil {
		return Group{}, err
	}
	return Get(ctx, tx, id)
}

// Update changes name/membership at an expected generation. Moving a frontend IP
// needs a dedicated guarded migration, not an ordinary CRUD edit. Identity changes
// enter whole-pair draining and retain removed-member reservations until verified
// withdrawal. Caller-provided lifecycle is never applied.
func Update(ctx context.Context, tx pgx.Tx, g Group, expected int64) (Group, error) {
	if err := g.Validate(); err != nil {
		return Group{}, err
	}
	current, err := lock(ctx, tx, g.ID, expected)
	if err != nil {
		return Group{}, err
	}
	if current.Lifecycle != "active" && current.Lifecycle != "withdrawn" {
		return Group{}, fmt.Errorf("%w: lifecycle mutation pending", ErrInvalid)
	}
	if current.FrontendIP != g.FrontendIP {
		return Group{}, fmt.Errorf("%w: frontend IP is immutable", ErrInvalid)
	}
	if err := validateMembers(ctx, tx, g.Members); err != nil {
		return Group{}, err
	}
	next := current.Lifecycle
	if !sameMembers(current.Members, g.Members) {
		next = "draining"
	}
	_, err = tx.Exec(ctx, `update public.failover_groups set name=$2,member_a=$3,member_b=$4,lifecycle=$5,generation=generation+1,updated_at=now() where id=$1`, g.ID, g.Name, g.Members[0], g.Members[1], next)
	if err != nil {
		return Group{}, store.MapError(err)
	}
	if _, err = tx.Exec(ctx, `delete from public.failover_members where group_id=$1`, g.ID); err != nil {
		return Group{}, store.MapError(err)
	}
	if err = insertMembers(ctx, tx, g); err != nil {
		return Group{}, err
	}
	return Get(ctx, tx, g.ID)
}

func lock(ctx context.Context, tx pgx.Tx, id uuid.UUID, expected int64) (Group, error) {
	g, err := scan(tx.QueryRow(ctx, "select "+columns+" from public.failover_groups where id=$1 for update", id))
	if err != nil {
		return Group{}, err
	}
	if expected <= 0 || g.Generation != expected {
		return Group{}, fmt.Errorf("%w: stale failover generation", store.ErrConflict)
	}
	return g, nil
}

func insertMembers(ctx context.Context, tx pgx.Tx, g Group) error {
	_, err := tx.Exec(ctx, `insert into public.failover_members(group_id,engine_id,slot) values($1,$2,1),($1,$3,2)`, g.ID, g.Members[0], g.Members[1])
	return store.MapError(err)
}

// validateMembers is admission validation, not live eligibility. Locks serialize
// with engine edits during this transaction. Later revocation/movement must remove
// the backend from eligibility, not prevent emergency engine revocation.
// node_name is self-reported: the platform adapter MUST also verify actual
// distinct failure-domain nodes (prefixed engine names are not that proof).
func validateMembers(ctx context.Context, tx pgx.Tx, ids [2]uuid.UUID) error {
	rows, err := tx.Query(ctx, `select node_name,engine_group_id,deleted_at is not null or revoked_at is not null
 from public.engines where id in ($1,$2) order by id for update`, ids[0], ids[1])
	if err != nil {
		return store.MapError(err)
	}
	defer rows.Close()
	var nodes []string
	var policies []uuid.UUID
	for rows.Next() {
		var node string
		var policy uuid.UUID
		var inactive bool
		if err := rows.Scan(&node, &policy, &inactive); err != nil {
			return err
		}
		if inactive || strings.TrimSpace(node) == "" {
			return fmt.Errorf("%w: inactive or unplaced engine", ErrInvalid)
		}
		nodes = append(nodes, node)
		policies = append(policies, policy)
	}
	if err := rows.Err(); err != nil {
		return store.MapError(err)
	}
	if len(nodes) != 2 {
		return fmt.Errorf("%w: unknown engine identity", ErrInvalid)
	}
	if nodes[0] == nodes[1] {
		return fmt.Errorf("%w: members report the same node", ErrInvalid)
	}
	// Conservative admission rule until cross-policy snapshot equivalence is proven.
	if policies[0] != policies[1] {
		return fmt.Errorf("%w: members must share a policy engine group", ErrInvalid)
	}
	return nil
}

func sameMembers(a, b [2]uuid.UUID) bool { return a == b || a[0] == b[1] && a[1] == b[0] }
