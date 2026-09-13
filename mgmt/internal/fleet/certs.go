package fleet

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Certificate check refusals; the control plane answers PermissionDenied with these messages.
var (
	ErrUnknownEngine      = errors.New("unknown or deleted engine")
	ErrCertificateRevoked = errors.New("certificate revoked")
	ErrEngineRevoked      = errors.New("engine is revoked")
)

// Channels carrying an engine id to every instance (the control hub listens on them).
const (
	channelEngineRevoked = "nexora_engine_revoked"
	channelEngineRotate  = "nexora_engine_rotate"
)

// RecordCertificate stores an issued engine certificate and makes it the engine's newest serial.
func RecordCertificate(ctx context.Context, q store.PolicyQuerier, engineID uuid.UUID, certDER []byte) error {
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return fmt.Errorf("record certificate: %w", err)
	}
	serial := cert.SerialNumber.Text(16)
	if _, err := q.Exec(ctx, `insert into engine_certificates (serial, engine_id, not_before, not_after)
		values (lower($1), $2, $3, $4) on conflict (serial) do nothing`, serial, engineID, cert.NotBefore, cert.NotAfter); err != nil {
		return err
	}
	_, err = q.Exec(ctx, "update engines set certificate_serial = lower($1) where id = $2", serial, engineID)
	return err
}

// CheckCertificate accepts only a live, non-revoked engine presenting an unrevoked certificate
// issued to it: ErrUnknownEngine for an unknown or deleted engine, ErrCertificateRevoked for a
// revoked engine or a revoked, superseded, unknown or foreign serial.
func CheckCertificate(ctx context.Context, q store.PolicyQuerier, engineID uuid.UUID, serialHex string) error {
	var deleted, engineRevoked, certRevoked bool
	var owner *uuid.UUID
	err := q.QueryRow(ctx, `select e.deleted_at is not null, e.revoked_at is not null, c.engine_id, c.revoked_at is not null
		from engines e left join engine_certificates c on c.serial = lower($2) where e.id = $1`, engineID, serialHex).
		Scan(&deleted, &engineRevoked, &owner, &certRevoked)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrUnknownEngine
	case err != nil:
		return store.MapError(err)
	case deleted:
		return ErrUnknownEngine
	case engineRevoked, owner == nil, certRevoked, *owner != engineID:
		return ErrCertificateRevoked
	}
	return nil
}

// SupersedeOlderCertificates revokes (reason superseded) the engine's unrevoked certificates issued
// before serialHex. Called once the engine has connected with serialHex, so a renewed certificate
// replaces the old one only after it is proven to work.
func SupersedeOlderCertificates(ctx context.Context, q store.PolicyQuerier, engineID uuid.UUID, serialHex string) error {
	_, err := q.Exec(ctx, `update engine_certificates set revoked_at = now(), revoke_reason = 'superseded'
		where engine_id = $1 and revoked_at is null
		and issued_at < (select issued_at from engine_certificates where serial = lower($2) and engine_id = $1)`, engineID, serialHex)
	return store.MapError(err)
}

// RevokeEngine revokes the engine and every certificate it holds, and tells every instance to end
// its streams (store.ErrNotFound for an unknown or deleted engine).
func RevokeEngine(ctx context.Context, tx pgx.Tx, engineID uuid.UUID) error {
	tag, err := tx.Exec(ctx, `update engines set revoked_at = coalesce(revoked_at, now()), revision = revision + 1
		where id = $1 and deleted_at is null`, engineID)
	if err != nil {
		return store.MapError(err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	if _, err := tx.Exec(ctx, `update engine_certificates set revoked_at = now(), revoke_reason = 'revoked'
		where engine_id = $1 and revoked_at is null`, engineID); err != nil {
		return store.MapError(err)
	}
	_, err = tx.Exec(ctx, "select pg_notify($1, $2::text)", channelEngineRevoked, engineID)
	return store.MapError(err)
}

// RequestRotation asks the engine for a new certificate: now over its live stream, and on every
// Connect until one is issued (ErrEngineRevoked for a revoked engine, store.ErrNotFound for an
// unknown or deleted one).
func RequestRotation(ctx context.Context, tx pgx.Tx, engineID uuid.UUID) error {
	var revoked bool
	err := tx.QueryRow(ctx, "select revoked_at is not null from engines where id = $1 and deleted_at is null for update", engineID).Scan(&revoked)
	if err != nil {
		return store.MapError(err)
	}
	if revoked {
		return ErrEngineRevoked
	}
	if _, err := tx.Exec(ctx, "update engines set cert_rotate_requested_at = now() where id = $1", engineID); err != nil {
		return store.MapError(err)
	}
	_, err = tx.Exec(ctx, "select pg_notify($1, $2::text)", channelEngineRotate, engineID)
	return store.MapError(err)
}
