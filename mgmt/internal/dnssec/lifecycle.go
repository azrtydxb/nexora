package dnssec

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// OrphanGrace is how long a token key must stay unreferenced by any committed dnssec_keys row
// before SweepTokenOrphans destroys it: it outlasts every transaction that generates a key and
// then signs the zone.
const OrphanGrace = time.Hour

const destructionBatch = 100

// enqueueDestruction queues the token objects of PKCS#11 keys removed in tx; DestroyPending
// destroys them once tx committed. KEK keys vanish with their envelope and are skipped.
func enqueueDestruction(ctx context.Context, tx pgx.Tx, keys []secrets.StoredKey) error {
	for _, k := range keys {
		if k.Backend != secrets.BackendPKCS11 {
			continue
		}
		if _, err := tx.Exec(ctx, "INSERT INTO dnssec_key_destruction (key_ref) VALUES ($1) ON CONFLICT DO NOTHING", k.KeyRef); err != nil {
			return err
		}
	}
	return nil
}

// DestroyPending destroys the token objects queued by committed transactions and dequeues them.
// It is idempotent (a key already gone destroys as a no-op) and safe to run from several
// instances; a failed destroy stays queued for the next run. Queued keys that a live row
// references again are dequeued without being destroyed.
func DestroyPending(ctx context.Context, st *store.Store, box *secrets.Box) error {
	rows, err := st.Pool.Query(ctx, `SELECT q.key_ref, EXISTS (SELECT 1 FROM dnssec_keys k WHERE k.key_ref = q.key_ref AND k.state <> 'removed')
		FROM dnssec_key_destruction q ORDER BY q.enqueued_at LIMIT $1`, destructionBatch)
	if err != nil {
		return store.MapError(err)
	}
	type queued struct {
		ref  []byte
		live bool
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (queued, error) {
		var q queued
		return q, r.Scan(&q.ref, &q.live)
	})
	if err != nil {
		return store.MapError(err)
	}
	if len(list) > 0 && !box.HasBackend(secrets.BackendPKCS11) {
		return fmt.Errorf("%d PKCS#11 keys await destruction: %w", len(list), secrets.ErrBackendUnavailable)
	}
	var errs []error
	for _, q := range list {
		if !q.live {
			if err := box.DestroySigningKey(secrets.StoredKey{Backend: secrets.BackendPKCS11, KeyRef: q.ref}); err != nil {
				errs = append(errs, fmt.Errorf("destroy PKCS#11 key %x: %w", q.ref, err))
				continue
			}
		}
		if _, err := st.Pool.Exec(ctx, "DELETE FROM dnssec_key_destruction WHERE key_ref = $1", q.ref); err != nil {
			errs = append(errs, store.MapError(err))
		}
	}
	return errors.Join(errs...)
}

// SweepTokenOrphans destroys Nexora's DNSSEC key objects in the token (label "nexora-dnssec",
// never other objects) that no live dnssec_keys row has referenced for grace, and returns how many
// it destroyed. A key is timed from the first sweep that saw it unreferenced; a key referenced
// again (its transaction committed) is forgotten. Without a token it does nothing.
// debt: the token is assumed to be dedicated to one Nexora installation (one database); revisit
// with an installation id in the object label if tokens are ever shared.
func SweepTokenOrphans(ctx context.Context, st *store.Store, box *secrets.Box, grace time.Duration) (int, error) {
	if !box.HasBackend(secrets.BackendPKCS11) {
		return 0, nil
	}
	refs, err := box.PKCS11SigningKeyRefs()
	if err != nil {
		return 0, fmt.Errorf("list token keys: %w", err)
	}
	if refs == nil {
		refs = [][]byte{} // an empty array, not NULL: every recorded orphan left the token
	}
	var due [][]byte
	err = st.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM dnssec_token_orphans o WHERE o.key_ref <> ALL($1::bytea[])
			OR EXISTS (SELECT 1 FROM dnssec_keys k WHERE k.key_ref = o.key_ref AND k.state <> 'removed')`, refs); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO dnssec_token_orphans (key_ref) SELECT r FROM unnest($1::bytea[]) AS r
			WHERE NOT EXISTS (SELECT 1 FROM dnssec_keys k WHERE k.key_ref = r AND k.state <> 'removed')
			ON CONFLICT DO NOTHING`, refs); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT key_ref FROM dnssec_token_orphans WHERE first_seen_at <= now() - make_interval(secs => $1)", grace.Seconds())
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, pgx.RowTo[[]byte])
		return err
	})
	if err != nil {
		return 0, store.MapError(err)
	}
	destroyed := 0
	var errs []error
	for _, ref := range due {
		if err := box.DestroySigningKey(secrets.StoredKey{Backend: secrets.BackendPKCS11, KeyRef: ref}); err != nil {
			errs = append(errs, fmt.Errorf("destroy orphaned PKCS#11 key %x: %w", ref, err))
			continue
		}
		destroyed++
		if _, err := st.Pool.Exec(ctx, "DELETE FROM dnssec_token_orphans WHERE key_ref = $1", ref); err != nil {
			errs = append(errs, store.MapError(err))
		}
	}
	return destroyed, errors.Join(errs...)
}
