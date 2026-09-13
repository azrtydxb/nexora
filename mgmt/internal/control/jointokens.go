package control

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/pki"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// JoinToken is a stored join token (its secret is only ever returned at creation).
type JoinToken struct {
	ID, Name, CreatedBy  string
	CreatedAt, ExpiresAt time.Time
	RevokedAt            *time.Time
	Uses                 int
	EngineGroupID        uuid.UUID
	Labels               map[string]string
	MaxUses              *int
}

// JoinTokenSpec describes a join token to create. MaxUses nil means unlimited uses until expiry.
type JoinTokenSpec struct {
	Name, CreatedBy string
	TTL             time.Duration
	EngineGroupID   uuid.UUID
	MaxUses         *int
	Labels          map[string]string
}

// CreateJoinToken stores a new join token for the default engine group, without labels and with
// unlimited uses, valid for ttl, and returns it with the full token string.
func CreateJoinToken(ctx context.Context, tx pgx.Tx, ca *pki.CA, name, createdBy string, ttl time.Duration) (JoinToken, string, error) {
	return CreateJoinTokenFor(ctx, tx, ca, JoinTokenSpec{Name: name, CreatedBy: createdBy, TTL: ttl, EngineGroupID: store.DefaultEngineGroupID})
}

// CreateJoinTokenFor stores a new join token as spec describes (only the secret's hash is stored)
// and returns it with the full token string. An unknown engine group is store.ErrNotFound.
func CreateJoinTokenFor(ctx context.Context, tx pgx.Tx, ca *pki.CA, spec JoinTokenSpec) (JoinToken, string, error) {
	return InsertJoinToken(ctx, tx, ca.Fingerprint(), spec)
}

// InsertJoinToken is CreateJoinTokenFor for a CA known only by its fingerprint (the CLI).
func InsertJoinToken(ctx context.Context, tx pgx.Tx, caFingerprint string, spec JoinTokenSpec) (JoinToken, string, error) {
	if spec.Name == "" || spec.TTL <= 0 {
		return JoinToken{}, "", errors.New("join token needs a name and a positive ttl")
	}
	if spec.MaxUses != nil && *spec.MaxUses < 1 {
		return JoinToken{}, "", errors.New("join token max uses must be at least 1")
	}
	labels := spec.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	var exists bool
	if err := tx.QueryRow(ctx, "select exists(select 1 from engine_groups where id = $1)", spec.EngineGroupID).Scan(&exists); err != nil {
		return JoinToken{}, "", err
	}
	if !exists {
		return JoinToken{}, "", store.ErrNotFound
	}
	token, secret, err := pki.NewJoinToken(caFingerprint)
	if err != nil {
		return JoinToken{}, "", err
	}
	jt := JoinToken{Name: spec.Name, CreatedBy: spec.CreatedBy, EngineGroupID: spec.EngineGroupID, Labels: labels, MaxUses: spec.MaxUses}
	err = tx.QueryRow(ctx, `insert into join_tokens(name, secret_hash, created_by, expires_at, engine_group_id, labels, max_uses)
		values ($1, $2, $3, now() + $4 * interval '1 millisecond', $5, $6, $7) returning id::text, created_at, expires_at`,
		spec.Name, pki.HashSecret(secret), spec.CreatedBy, spec.TTL.Milliseconds(), spec.EngineGroupID, labels, spec.MaxUses).
		Scan(&jt.ID, &jt.CreatedAt, &jt.ExpiresAt)
	if err != nil {
		return JoinToken{}, "", err
	}
	return jt, token, nil
}
