package odoh

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

const (
	// PublishDelay keeps a new key out of /.well-known/odohconfigs until every engine holds it.
	PublishDelay = 5 * time.Minute
	// SealPurpose binds the seed envelopes to their use.
	SealPurpose = "odoh-seed"
	// ChannelKeys is notified whenever the key set changes.
	ChannelKeys = "nexora_odoh_keys"
)

const seedLen = 32

// scheduledActor records the rotations Rotate makes on its own schedule.
var scheduledActor = auth.Actor{Type: "system", ID: "odoh-rotate", Name: "system:odoh"}

// KeyInfo describes one key without its seed.
type KeyInfo struct {
	ID                                uuid.UUID
	CreatedAt, PublishAfter, NotAfter time.Time
}

// Keys rotates, lists and loads the ODoH key seeds. Seeds are sealed at rest, unsealed only by
// Load for the control stream, and never logged.
type Keys struct {
	Pool *pgxpool.Pool
	Box  *secrets.Box
	Now  func() time.Time // nil = time.Now
}

func (k *Keys) now() time.Time {
	if k.Now == nil {
		return time.Now()
	}
	return k.Now()
}

// List returns the keys that have not expired, newest first.
func (k *Keys) List(ctx context.Context) ([]KeyInfo, error) {
	rows, err := k.Pool.Query(ctx, `select id, created_at, publish_after, not_after from odoh_keys
		where not_after > $1 order by created_at desc`, k.now())
	if err != nil {
		return nil, store.MapError(err)
	}
	infos, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (KeyInfo, error) {
		var i KeyInfo
		err := r.Scan(&i.ID, &i.CreatedAt, &i.PublishAfter, &i.NotAfter)
		return i, err
	})
	return infos, store.MapError(err)
}

// Rotate deletes expired keys and creates a new key when forcedBy is set, or when the target role
// is on and no key is younger than the rotation interval. It reports whether a key was created. An
// instance that does not win the advisory lock returns (false, nil). Every change notifies
// ChannelKeys. The new key's audit row is written in the rotation's transaction: rotateOdohKey by
// forcedBy, or rotateOdohKeyScheduled by the system for a scheduled rotation.
func (k *Keys) Rotate(ctx context.Context, forcedBy *auth.Actor) (bool, error) {
	force := forcedBy != nil
	now := k.now()
	tx, err := k.Pool.Begin(ctx)
	if err != nil {
		return false, store.MapError(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var locked bool
	if err := tx.QueryRow(ctx, `select pg_try_advisory_xact_lock(hashtext('nexora:odoh-rotate'))`).Scan(&locked); err != nil {
		return false, store.MapError(err)
	}
	if !locked {
		return false, nil
	}
	var targetEnabled bool
	var rotationHours int32
	if err := tx.QueryRow(ctx, `select target_enabled, key_rotation_hours from odoh_settings`).Scan(&targetEnabled, &rotationHours); err != nil {
		return false, store.MapError(err)
	}
	rotation := time.Duration(rotationHours) * time.Hour
	deleted, err := tx.Exec(ctx, `delete from odoh_keys where not_after <= $1`, now)
	if err != nil {
		return false, store.MapError(err)
	}
	rotate := force
	if !force && targetEnabled {
		var fresh bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from odoh_keys where created_at > $1)`, now.Add(-rotation)).Scan(&fresh); err != nil {
			return false, store.MapError(err)
		}
		rotate = !fresh
	}
	if !rotate && deleted.RowsAffected() == 0 {
		return false, nil
	}
	if rotate {
		id, err := k.insertKey(ctx, tx, now, rotation)
		if err != nil {
			return false, err
		}
		actor, change := scheduledActor, auth.Change{Action: "rotateOdohKeyScheduled", TargetType: "odoh_key", TargetID: id.String()}
		if force {
			actor, change.Action = *forcedBy, "rotateOdohKey"
			change.After = map[string]any{"publish_after": now.Add(PublishDelay), "not_after": now.Add(2 * rotation)}
		}
		if err := auth.WriteAudit(ctx, tx, actor, change, nil); err != nil {
			return false, store.MapError(err)
		}
	}
	if _, err := tx.Exec(ctx, `select pg_notify($1, '')`, ChannelKeys); err != nil {
		return false, store.MapError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, store.MapError(err)
	}
	return rotate, nil
}

// insertKey seals a fresh random seed and stores it with its publication window, returning its id.
func (k *Keys) insertKey(ctx context.Context, tx pgx.Tx, now time.Time, rotation time.Duration) (uuid.UUID, error) {
	seed := make([]byte, seedLen)
	defer clear(seed)
	if _, err := rand.Read(seed); err != nil {
		return uuid.Nil, err
	}
	envelope, err := k.Box.Seal(SealPurpose, seed)
	if err != nil {
		return uuid.Nil, fmt.Errorf("seal odoh seed: %w", err)
	}
	var id uuid.UUID
	err = tx.QueryRow(ctx, `insert into odoh_keys (seed_envelope, created_at, publish_after, not_after) values ($1, $2, $3, $4) returning id`,
		envelope, now, now.Add(PublishDelay), now.Add(2*rotation)).Scan(&id)
	return id, store.MapError(err)
}

// Load returns the unexpired key set, newest first, with its seeds unsealed, and the lowercase hex
// SHA-256 of its deterministic encoding ("" for an empty set), as the control hub digests key sets.
// The caller must not log the set and clears the seeds when done.
func (k *Keys) Load(ctx context.Context) (*controlv1.OdohKeys, string, error) {
	rows, err := k.Pool.Query(ctx, `select id, seed_envelope, publish_after, not_after from odoh_keys
		where not_after > $1 order by created_at desc`, k.now())
	if err != nil {
		return nil, "", store.MapError(err)
	}
	defer rows.Close()
	keys := &controlv1.OdohKeys{}
	for rows.Next() {
		var id uuid.UUID
		var envelope []byte
		var publishAfter, notAfter time.Time
		if err := rows.Scan(&id, &envelope, &publishAfter, &notAfter); err != nil {
			return nil, "", store.MapError(err)
		}
		seed, err := k.Box.Unseal(SealPurpose, envelope)
		if err != nil {
			return nil, "", fmt.Errorf("odoh key %s: %w", id, err)
		}
		if len(seed) != seedLen {
			clear(seed)
			return nil, "", fmt.Errorf("odoh key %s: seed is %d octets, want %d", id, len(seed), seedLen)
		}
		keys.Keys = append(keys.Keys, &controlv1.OdohKey{Seed: seed, PublishAfterUnix: publishAfter.Unix(), NotAfterUnix: notAfter.Unix()})
	}
	if err := rows.Err(); err != nil {
		return nil, "", store.MapError(err)
	}
	if len(keys.Keys) == 0 {
		return keys, "", nil
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(keys)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(raw)
	clear(raw)
	return keys, hex.EncodeToString(sum[:]), nil
}

// Run calls Rotate every tick until ctx ends. Errors are logged; they never carry seeds.
func (k *Keys) Run(ctx context.Context, tick time.Duration) error {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		if _, err := k.Rotate(ctx, nil); err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
			slog.Warn("rotate odoh keys", "err", err)
		}
	}
}
