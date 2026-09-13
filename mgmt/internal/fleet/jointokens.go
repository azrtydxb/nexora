package fleet

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/pki"
)

// Join token refusals; Enroll answers PermissionDenied with exactly these messages.
var (
	ErrJoinTokenUnknown   = errors.New("join token unknown")
	ErrJoinTokenExpired   = errors.New("join token expired")
	ErrJoinTokenExhausted = errors.New("join token exhausted")
	ErrJoinTokenRevoked   = errors.New("join token revoked")
)

// JoinTokenGrant is what one use of a join token gives the enrolling engine.
type JoinTokenGrant struct {
	ID, EngineGroupID uuid.UUID
	Labels            map[string]string
}

// ConsumeJoinToken counts one use of the token whose secret is secret, atomically with its
// validity checks (a single conditional UPDATE, so concurrent enrollments never exceed max_uses).
func ConsumeJoinToken(ctx context.Context, tx pgx.Tx, secret string) (JoinTokenGrant, error) {
	hash := pki.HashSecret(secret)
	var g JoinTokenGrant
	err := tx.QueryRow(ctx, `update join_tokens set uses = uses + 1
		where secret_hash = $1 and revoked_at is null and expires_at > now() and (max_uses is null or uses < max_uses)
		returning id, engine_group_id, labels`, hash).Scan(&g.ID, &g.EngineGroupID, &g.Labels)
	if err == nil {
		return g, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return JoinTokenGrant{}, err
	}
	var revoked, expired, exhausted bool
	err = tx.QueryRow(ctx, `select revoked_at is not null, expires_at <= now(), max_uses is not null and uses >= max_uses
		from join_tokens where secret_hash = $1`, hash).Scan(&revoked, &expired, &exhausted)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return JoinTokenGrant{}, ErrJoinTokenUnknown
	case err != nil:
		return JoinTokenGrant{}, err
	case revoked:
		return JoinTokenGrant{}, ErrJoinTokenRevoked
	case expired:
		return JoinTokenGrant{}, ErrJoinTokenExpired
	case exhausted:
		return JoinTokenGrant{}, ErrJoinTokenExhausted
	}
	// Became usable between the two statements (cannot happen for a token only ever consumed).
	return JoinTokenGrant{}, ErrJoinTokenUnknown
}
