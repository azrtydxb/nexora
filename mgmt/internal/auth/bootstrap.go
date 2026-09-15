package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// The system user and token name the bootstrap token file provisions (used by the Kubernetes operator).
const (
	BootstrapUsername  = "nexora-operator"
	BootstrapTokenName = "bootstrap"
)

// Bootstrap token errors and metric.
var (
	ErrBootstrapTokenInvalid = errors.New("bootstrap token: the file does not hold an nxt_ token")
	ErrBootstrapUserConflict = errors.New("bootstrap token: user nexora-operator exists and is not a system user")
	BootstrapTokenErrors     = prometheus.NewCounter(prometheus.CounterOpts{Name: "nexora_mgmt_bootstrap_token_errors_total", Help: "Failed applications of the bootstrap token file."})
)

// EnsureBootstrapToken makes token the only unrevoked bootstrap token of the system user (created when
// missing) and reports whether it wrote anything; a write also writes the audit action ensureBootstrapToken.
// Only the token's SHA-256 hash and its 12-character prefix are stored; neither the token nor its hash
// reaches the audit log.
func (s *Service) EnsureBootstrapToken(ctx context.Context, token string) (changed bool, err error) {
	if !strings.HasPrefix(token, apiTokenPrefix) || len(token) <= len(apiTokenPrefix)+8 {
		return false, ErrBootstrapTokenInvalid
	}
	hash := hashToken(token)
	prefix := token[:len(apiTokenPrefix)+8]
	err = s.st.InTx(ctx, func(tx pgx.Tx) error {
		changed = false
		// Every replica runs this loop; the lock serialises them so one token row is created.
		if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtext('nexora:bootstrap_token'))"); err != nil {
			return err
		}
		var userID, source, role string
		var disabled bool
		err := tx.QueryRow(ctx, "select id::text, source, role, disabled from users where username = $1 for update", BootstrapUsername).
			Scan(&userID, &source, &role, &disabled)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if err := tx.QueryRow(ctx, `insert into users(username, role, source, disabled)
				values ($1, 'admin', 'system', false) returning id::text`, BootstrapUsername).Scan(&userID); err != nil {
				return err
			}
			changed = true
		case err != nil:
			return err
		case source != "system":
			return ErrBootstrapUserConflict
		case role != string(RoleAdmin) || disabled:
			if _, err := tx.Exec(ctx, `update users set role = 'admin', disabled = false, revision = revision + 1, updated_at = now()
				where id = $1`, userID); err != nil {
				return err
			}
			changed = true
		}
		current, err := bootstrapTokenValid(ctx, tx, userID, hash)
		if err != nil {
			return err
		}
		if !current {
			// A revoked or expired row with the same hash (the file rolled back to an old token) would
			// violate the unique token_hash; the new row replaces it.
			if _, err := tx.Exec(ctx, `delete from api_tokens where user_id = $1 and token_hash = $2`, userID, hash); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into api_tokens(user_id, name, prefix, token_hash, role)
				values ($1, $2, $3, $4, 'admin')`, userID, BootstrapTokenName, prefix, hash); err != nil {
				return err
			}
			changed = true
		}
		tag, err := tx.Exec(ctx, `update api_tokens set revoked_at = now()
			where user_id = $1 and name = $2 and revoked_at is null and token_hash <> $3`, userID, BootstrapTokenName, hash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			changed = true
		}
		if !changed {
			return nil
		}
		return WriteAudit(ctx, tx, Actor{Type: "system", ID: "bootstrap", Name: "bootstrap"},
			Change{Action: "ensureBootstrapToken", TargetType: "user", TargetID: userID,
				After: map[string]any{"token_prefix": prefix}}, nil)
	})
	return changed, err
}

// bootstrapTokenValid reports whether the user holds an unrevoked, unexpired bootstrap token with hash.
func bootstrapTokenValid(ctx context.Context, tx pgx.Tx, userID string, hash []byte) (bool, error) {
	rows, err := tx.Query(ctx, `select token_hash from api_tokens where user_id = $1 and name = $2
		and revoked_at is null and (expires_at is null or expires_at > now())`, userID, BootstrapTokenName)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var h []byte
		if err := rows.Scan(&h); err != nil {
			return false, err
		}
		if subtle.ConstantTimeCompare(h, hash) == 1 {
			found = true
		}
	}
	return found, rows.Err()
}

// RunBootstrapToken applies file at start and every interval until ctx ends. Errors are logged through
// logf without the file content and counted in BootstrapTokenErrors.
func (s *Service) RunBootstrapToken(ctx context.Context, file string, interval time.Duration, logf func(format string, args ...any)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := s.applyBootstrapTokenFile(ctx, file); err != nil && ctx.Err() == nil {
			logf("bootstrap token: %v", err)
			BootstrapTokenErrors.Inc()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) applyBootstrapTokenFile(ctx context.Context, file string) error {
	b, err := os.ReadFile(file)
	if err != nil {
		// The *PathError names the file and the cause only, never content.
		return err
	}
	_, err = s.EnsureBootstrapToken(ctx, strings.TrimSpace(string(b)))
	return store.MapError(err)
}
