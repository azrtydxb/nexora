package control

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/pki"
)

// JoinToken is a stored join token (its secret is only ever returned at creation).
type JoinToken struct {
	ID, Name, CreatedBy  string
	CreatedAt, ExpiresAt time.Time
	RevokedAt            *time.Time
	Uses                 int
}

// CreateJoinToken stores a new join token valid for ttl and returns it with the full token string.
func CreateJoinToken(ctx context.Context, tx pgx.Tx, ca *pki.CA, name, createdBy string, ttl time.Duration) (JoinToken, string, error) {
	if name == "" || ttl <= 0 {
		return JoinToken{}, "", errors.New("join token needs a name and a positive ttl")
	}
	token, secret, err := pki.NewJoinToken(ca.Fingerprint())
	if err != nil {
		return JoinToken{}, "", err
	}
	jt := JoinToken{Name: name, CreatedBy: createdBy}
	err = tx.QueryRow(ctx, `insert into join_tokens(name, secret_hash, created_by, expires_at)
		values ($1, $2, $3, now() + $4 * interval '1 millisecond') returning id::text, created_at, expires_at`,
		name, pki.HashSecret(secret), createdBy, ttl.Milliseconds()).Scan(&jt.ID, &jt.CreatedAt, &jt.ExpiresAt)
	if err != nil {
		return JoinToken{}, "", err
	}
	return jt, token, nil
}

// lookupJoinToken returns the id of a usable (not revoked, not expired) token, locking its row.
func lookupJoinToken(ctx context.Context, tx pgx.Tx, secret string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `select id::text from join_tokens
		where secret_hash = $1 and revoked_at is null and expires_at > now() for update`,
		pki.HashSecret(secret)).Scan(&id)
	return id, err
}
