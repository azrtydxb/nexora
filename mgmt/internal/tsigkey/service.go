// Package tsigkey manages the TSIG keys hosted zones use for transfers, NOTIFY and dynamic
// updates. Secrets are sealed with secrets.Box before any database write, are returned only once
// (on create) and reach engines only in the in-memory KeyMaterial control message.
package tsigkey

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Errors callers map to API responses.
var (
	ErrInUse   = errors.New("tsig key is in use")
	ErrInvalid = errors.New("invalid tsig key")
)

// Key is a TSIG key without its secret.
type Key struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Algorithm string    `json:"algorithm"`
	Revision  int64     `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
}

// Created is a new key with its base64 secret, returned once.
type Created struct {
	Key
	Secret string
}

// Service is the TSIG key store.
type Service struct {
	Store *store.Store
	Build snapshot.BuildConfig
	Box   *secrets.Box
}

// secretLen is the generated secret length per algorithm: the HMAC output size (RFC 8945 §6).
var secretLen = map[string]int{"hmac-sha256": 32, "hmac-sha384": 48, "hmac-sha512": 64}

var nameRE = regexp.MustCompile(`^([a-z0-9_-]{1,63}\.)+$`)

// Create stores a key. An empty secretB64 generates one; a provided secret must be base64 of
// 16..64 bytes. The secret is sealed before the transaction, so unconfigured key storage refuses
// without writing.
func (s *Service) Create(ctx context.Context, actor auth.Actor, name, algorithm, secretB64 string) (*Created, error) {
	name = strings.ToLower(name)
	if len(name) > 254 || !nameRE.MatchString(name) {
		return nil, fmt.Errorf("%w: name must be an absolute domain name of letters, digits, '-' and '_' (e.g. xfr-key.)", ErrInvalid)
	}
	n, ok := secretLen[algorithm]
	if !ok {
		return nil, fmt.Errorf("%w: algorithm must be hmac-sha256, hmac-sha384 or hmac-sha512", ErrInvalid)
	}
	var secret []byte
	if secretB64 == "" {
		secret = make([]byte, n)
		if _, err := rand.Read(secret); err != nil {
			return nil, err
		}
	} else {
		raw, err := base64.StdEncoding.DecodeString(secretB64)
		if err != nil || len(raw) < 16 || len(raw) > 64 {
			clear(raw)
			return nil, fmt.Errorf("%w: secret must be base64 of 16..64 bytes", ErrInvalid)
		}
		secret = raw
	}
	defer clear(secret)
	id := uuid.New()
	envelope, err := s.Box.Seal(secrets.TSIGPurpose(id, name, algorithm), secret)
	if err != nil {
		return nil, err
	}
	out := &Created{Secret: base64.StdEncoding.EncodeToString(secret)}
	_, err = snapshot.Mutate(ctx, s.Store, s.Build, actor, func(tx pgx.Tx) (auth.Change, error) {
		err := tx.QueryRow(ctx, `INSERT INTO tsig_keys (id, name, algorithm, secret_envelope) VALUES ($1, $2, $3, $4)
			RETURNING id, name, algorithm, revision, created_at`, id, name, algorithm, envelope).
			Scan(&out.ID, &out.Name, &out.Algorithm, &out.Revision, &out.CreatedAt)
		if err != nil {
			return auth.Change{}, store.MapError(err)
		}
		return auth.Change{Action: "createTsigKey", TargetType: "tsig_key", TargetID: id.String(), After: out.Key}, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// List returns every key ordered by name.
func (s *Service) List(ctx context.Context) ([]Key, error) {
	rows, err := s.Store.Pool.Query(ctx, `SELECT id, name, algorithm, revision, created_at FROM tsig_keys ORDER BY name`)
	if err != nil {
		return nil, store.MapError(err)
	}
	keys, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Key, error) {
		var k Key
		err := r.Scan(&k.ID, &k.Name, &k.Algorithm, &k.Revision, &k.CreatedAt)
		return k, err
	})
	if err != nil {
		return nil, store.MapError(err)
	}
	return keys, nil
}

// Delete removes the key at revision unless a zone references it.
func (s *Service) Delete(ctx context.Context, actor auth.Actor, id uuid.UUID, revision int64) error {
	_, err := snapshot.Mutate(ctx, s.Store, s.Build, actor, func(tx pgx.Tx) (auth.Change, error) {
		var k Key
		// The row lock serialises against zone writes, which take FOR SHARE on referenced keys.
		err := tx.QueryRow(ctx, `SELECT id, name, algorithm, revision, created_at FROM tsig_keys WHERE id = $1 FOR UPDATE`, id).
			Scan(&k.ID, &k.Name, &k.Algorithm, &k.Revision, &k.CreatedAt)
		if err != nil {
			return auth.Change{}, store.MapError(err)
		}
		if k.Revision != revision {
			return auth.Change{}, fmt.Errorf("tsig key revision %d is stale (current %d): %w", revision, k.Revision, store.ErrConflict)
		}
		var used bool
		err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM zones WHERE transfer_tsig_key_id = $1 OR $1 = ANY(update_tsig_key_ids)
			OR notify_targets @> jsonb_build_array(jsonb_build_object('tsig_key_id', $1::text))
			OR primaries @> jsonb_build_array(jsonb_build_object('tsig_key_id', $1::text)))`, id).Scan(&used)
		if err != nil {
			return auth.Change{}, err
		}
		if used {
			return auth.Change{}, ErrInUse
		}
		if _, err := tx.Exec(ctx, `DELETE FROM tsig_keys WHERE id = $1`, id); err != nil {
			return auth.Change{}, err
		}
		return auth.Change{Action: "deleteTsigKey", TargetType: "tsig_key", TargetID: id.String(), Before: k}, nil
	})
	return err
}

// Secret unseals one key for the management plane's own TSIG use. The caller clears secret.
func (s *Service) Secret(ctx context.Context, q snapshot.Querier, id uuid.UUID) (name, algorithm string, secret []byte, err error) {
	var envelope []byte
	if err := q.QueryRow(ctx, `SELECT name, algorithm, secret_envelope FROM tsig_keys WHERE id = $1`, id).Scan(&name, &algorithm, &envelope); err != nil {
		return "", "", nil, store.MapError(err)
	}
	secret, err = s.Box.Unseal(secrets.TSIGPurpose(id, name, algorithm), envelope)
	if err != nil {
		return "", "", nil, fmt.Errorf("tsig key %s: %w", name, err)
	}
	return name, algorithm, secret, nil
}
