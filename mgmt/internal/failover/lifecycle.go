package failover

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// ErrUnauthorized means the connection's original PostgreSQL LOGIN identity is
// not provisioned for this operation. SET ROLE does not grant this authority.
var ErrUnauthorized = errors.New("unauthorized failover source")

// Pin public authority before calling shared inventory helpers that use
// unqualified relations. Explicit pg_temp last prevents PostgreSQL's implicit
// temporary-schema precedence. The setting lasts only for the caller's tx.
func pinAuthoritySchema(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `set local search_path = pg_catalog, public, pg_temp`)
	return err
}

func authorizeSource(ctx context.Context, tx pgx.Tx, purpose string) error {
	if err := pinAuthoritySchema(ctx, tx); err != nil {
		return err
	}
	var role string
	err := tx.QueryRow(ctx, `select s.role_name::text from public.failover_sources s
 join pg_catalog.pg_roles r on r.oid=s.role_oid and r.rolname=s.role_name
 where s.role_name=session_user and s.purpose=$1 for share of s`, purpose).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUnauthorized
	}
	return err
}

// PublisherToken identifies a reporting incarnation, NOT a dataplane lease.
// Tokens must never be used to authorize advertisement.
type PublisherToken struct {
	GroupID           uuid.UUID
	Generation, Epoch int64
	Owner, Session    uuid.UUID
}

// RotatePublisher is restricted to a provisioned controller LOGIN. expectedEpoch
// is zero only on first registration. It fences delayed reports, not packets.
// Caller must commit with its audit entry before handing the token to a worker.
func RotatePublisher(ctx context.Context, tx pgx.Tx, id uuid.UUID, generation, expectedEpoch int64, owner uuid.UUID) (PublisherToken, error) {
	if err := authorizeSource(ctx, tx, "controller"); err != nil {
		return PublisherToken{}, err
	}
	g, err := lock(ctx, tx, id, generation)
	if err != nil {
		return PublisherToken{}, err
	}
	if owner == uuid.Nil || g.Lifecycle == "deleted" || g.Lifecycle == "withdrawn" {
		return PublisherToken{}, ErrInvalid
	}
	var epoch int64
	err = tx.QueryRow(ctx, `select epoch from public.failover_publishers where group_id=$1 for update`, id).Scan(&epoch)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return PublisherToken{}, err
	}
	if epoch != expectedEpoch {
		return PublisherToken{}, store.ErrConflict
	}
	t := PublisherToken{id, generation, epoch + 1, owner, uuid.New()}
	_, err = tx.Exec(ctx, `insert into public.failover_publishers(group_id,epoch,owner,session,generation) values($1,$2,$3,$4,$5)
 on conflict(group_id) do update set epoch=excluded.epoch,owner=excluded.owner,session=excluded.session,generation=excluded.generation,sequence=0`, id, t.Epoch, t.Owner, t.Session, t.Generation)
	if err != nil {
		return PublisherToken{}, err
	}
	_, err = tx.Exec(ctx, `insert into public.failover_publisher_history(group_id,epoch,owner,session,generation) values($1,$2,$3,$4,$5)`, id, t.Epoch, t.Owner, t.Session, t.Generation)
	return t, err
}

func lockPublisher(ctx context.Context, tx pgx.Tx, t PublisherToken) (Group, error) {
	g, err := lock(ctx, tx, t.GroupID, t.Generation)
	if err != nil {
		return Group{}, err
	}
	var epoch, generation int64
	var owner, session uuid.UUID
	err = tx.QueryRow(ctx, `select epoch,owner,session,generation from public.failover_publishers where group_id=$1 for update`, t.GroupID).Scan(&epoch, &owner, &session, &generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return Group{}, store.ErrConflict
	}
	if err != nil {
		return Group{}, err
	}
	if generation != t.Generation || epoch != t.Epoch || owner != t.Owner || session != t.Session || session == uuid.Nil {
		return Group{}, store.ErrConflict
	}
	return g, nil
}

// BeginDrain stops eligibility for the whole pair and retains every reservation.
// Mutations take an existing transaction so API authorization and audit can commit
// atomically with generation CAS. They do not perform transport side effects.
func BeginDrain(ctx context.Context, tx pgx.Tx, id uuid.UUID, expected int64) (Group, error) {
	return transition(ctx, tx, id, expected, "draining")
}

// RequestDeletion records pending deletion. It never deletes identities or frees
// the IP/members, even if the group has never received an observation.
func RequestDeletion(ctx context.Context, tx pgx.Tx, id uuid.UUID, expected int64) (Group, error) {
	return transition(ctx, tx, id, expected, "deleting")
}

// Resume requires completed trusted withdrawal; runtime must acquire a new
// generation-bound grant before serving. No prior observation becomes healthy.
func Resume(ctx context.Context, tx pgx.Tx, id uuid.UUID, expected int64) (Group, error) {
	return transition(ctx, tx, id, expected, "active")
}
func transition(ctx context.Context, tx pgx.Tx, id uuid.UUID, expected int64, next string) (Group, error) {
	g, err := lock(ctx, tx, id, expected)
	if err != nil {
		return Group{}, err
	}
	valid := next == "draining" && g.Lifecycle == "active" || next == "deleting" && (g.Lifecycle == "active" || g.Lifecycle == "draining" || g.Lifecycle == "withdrawn") || next == "active" && g.Lifecycle == "withdrawn"
	if !valid {
		return Group{}, fmt.Errorf("%w: invalid lifecycle transition", ErrInvalid)
	}
	_, err = tx.Exec(ctx, `update public.failover_groups set lifecycle=$2,generation=generation+1,updated_at=now() where id=$1`, id, next)
	if err != nil {
		return Group{}, store.MapError(err)
	}
	return Get(ctx, tx, id)
}

// WithdrawalScope covers all reporting incarnations and all retained members.
// The verifier must independently prove durable withdrawal/fencing of EVERY
// possible advertiser and backend writer, including crashed/paused predecessors.
// Time passage, lease ACK, pod Ready, and claimant booleans are not such proof.
type WithdrawalScope struct {
	Group           Group
	Publishers      []PublisherToken
	ReservedMembers []uuid.UUID
}

// WithdrawalVerifier is a trusted enforcement-controller seam, not an API input.
// Verify must authenticate opaque proof against the exact scope and ensure old
// grants can never reactivate after return (including delayed commands/reboot).
// There is deliberately no default implementation. Parent must supply an actual
// runtime verifier; a nil verifier cannot release anything.
type WithdrawalVerifier interface {
	VerifyWithdrawal(context.Context, WithdrawalScope, []byte) error
}

func missingDependency(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	}
	return false
}

// CompleteWithdrawal requires both a separately provisioned withdrawal LOGIN and
// a trusted runtime verifier. It retains a tombstone/evidence digest; it releases
// retired members after drain, or all reservations after pending deletion.
// The caller must roll back on ANY error and commit an audit row in this tx.
func CompleteWithdrawal(ctx context.Context, tx pgx.Tx, t PublisherToken, proof []byte, v WithdrawalVerifier) (Group, error) {
	if missingDependency(v) || len(proof) == 0 || len(proof) > 1<<20 {
		return Group{}, ErrInvalid
	}
	if err := authorizeSource(ctx, tx, "withdrawal"); err != nil {
		return Group{}, err
	}
	g, err := lockPublisher(ctx, tx, t)
	if err != nil {
		return Group{}, err
	}
	if g.Lifecycle != "draining" && g.Lifecycle != "deleting" {
		return Group{}, ErrInvalid
	}
	scope := WithdrawalScope{Group: g}
	rows, err := tx.Query(ctx, `select epoch,owner,session,generation from public.failover_publisher_history where group_id=$1 order by epoch`, g.ID)
	if err != nil {
		return Group{}, err
	}
	for rows.Next() {
		p := PublisherToken{GroupID: g.ID}
		if err := rows.Scan(&p.Epoch, &p.Owner, &p.Session, &p.Generation); err != nil {
			rows.Close()
			return Group{}, err
		}
		scope.Publishers = append(scope.Publishers, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Group{}, err
	}
	rows, err = tx.Query(ctx, `select engine_id from public.failover_engine_reservations where group_id=$1 order by engine_id for update`, g.ID)
	if err != nil {
		return Group{}, err
	}
	scope.ReservedMembers, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return Group{}, err
	}
	if err = v.VerifyWithdrawal(ctx, scope, append([]byte(nil), proof...)); err != nil {
		return Group{}, err
	}
	if err = ctx.Err(); err != nil {
		return Group{}, err
	}
	digest := sha256.Sum256(proof)
	_, err = tx.Exec(ctx, `insert into public.failover_withdrawals(group_id,generation,epoch,proof_sha256) values($1,$2,$3,$4)`, g.ID, g.Generation, t.Epoch, hex.EncodeToString(digest[:]))
	if err != nil {
		return Group{}, err
	}
	next := "withdrawn"
	if g.Lifecycle == "deleting" {
		next = "deleted"
	}
	_, err = tx.Exec(ctx, `update public.failover_groups set lifecycle=$2,generation=generation+1,updated_at=now() where id=$1`, g.ID, next)
	if err != nil {
		return Group{}, err
	}
	_, err = tx.Exec(ctx, `delete from public.failover_engine_reservations where group_id=$1 and ($2 or engine_id not in ($3,$4))`, g.ID, next == "deleted", g.Members[0], g.Members[1])
	if err != nil {
		return Group{}, err
	}
	if next == "deleted" {
		_, err = tx.Exec(ctx, `delete from public.failover_ip_reservations where group_id=$1`, g.ID)
		if err != nil {
			return Group{}, err
		}
	}
	return Get(ctx, tx, g.ID)
}
