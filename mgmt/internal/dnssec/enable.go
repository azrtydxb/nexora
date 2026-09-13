package dnssec

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// ErrNotPrimary refuses signing a secondary zone: it serves its primary's data as transferred.
var ErrNotPrimary = errors.New("only primary zones are signed")

// Settings are a zone's DNSSEC settings. Zero Algorithm, NSECMode, KeyBackend, PropagationDelay and
// ParentDSTTL take the defaults (13, nsec3, the box's default backend, 1 h, 24 h); ZSKLifetimeDays
// 0 means manual ZSK rollovers only.
type Settings struct {
	Algorithm                     uint8
	NSECMode                      string
	KeyBackend                    secrets.Backend
	PropagationDelay, ParentDSTTL time.Duration
	ZSKLifetimeDays               int
}

// maxTagAttempts bounds key generation retries that avoid a key tag the zone already uses.
const maxTagAttempts = 8

// Enable turns signing on for primary zone zoneID and, when the zone has no keys yet, creates an
// active KSK (its DS pending at the parent) and an active ZSK. The caller rebuilds the zone.
func Enable(ctx context.Context, tx pgx.Tx, box *secrets.Box, zoneID uuid.UUID, st Settings) error {
	if !box.Configured() {
		return secrets.ErrUnconfigured
	}
	st = withDefaults(st, box)
	if !box.HasBackend(st.KeyBackend) {
		return secrets.ErrBackendUnavailable
	}
	if err := validate(st); err != nil {
		return err
	}
	var kind string
	if err := tx.QueryRow(ctx, "SELECT kind FROM zones WHERE id = $1", zoneID).Scan(&kind); err != nil {
		return fmt.Errorf("zone %s: %w", zoneID, store.MapError(err))
	}
	if kind != "primary" {
		return ErrNotPrimary
	}
	_, err := tx.Exec(ctx, `INSERT INTO zone_dnssec (zone_id, enabled, algorithm, nsec_mode, key_backend, propagation_delay_seconds,
		parent_ds_ttl_seconds, zsk_lifetime_days) VALUES ($1, true, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (zone_id) DO UPDATE SET enabled = true, algorithm = EXCLUDED.algorithm, nsec_mode = EXCLUDED.nsec_mode,
		key_backend = EXCLUDED.key_backend, propagation_delay_seconds = EXCLUDED.propagation_delay_seconds,
		parent_ds_ttl_seconds = EXCLUDED.parent_ds_ttl_seconds, zsk_lifetime_days = EXCLUDED.zsk_lifetime_days`,
		zoneID, int16(st.Algorithm), st.NSECMode, string(st.KeyBackend), int32(st.PropagationDelay/time.Second),
		int32(st.ParentDSTTL/time.Second), int32(st.ZSKLifetimeDays))
	if err != nil {
		return store.MapError(err)
	}
	var keys int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM dnssec_keys WHERE zone_id = $1 AND state <> 'removed'", zoneID).Scan(&keys); err != nil {
		return err
	}
	if keys > 0 {
		return nil
	}
	if err := createKey(ctx, tx, box, zoneID, st, "ksk", "pending"); err != nil {
		return err
	}
	return createKey(ctx, tx, box, zoneID, st, "zsk", "none")
}

func withDefaults(st Settings, box *secrets.Box) Settings {
	if st.Algorithm == 0 {
		st.Algorithm = dns.ECDSAP256SHA256
	}
	if st.NSECMode == "" {
		st.NSECMode = "nsec3"
	}
	if st.KeyBackend == "" {
		st.KeyBackend = box.DefaultBackend()
	}
	if st.PropagationDelay == 0 {
		st.PropagationDelay = time.Hour
	}
	if st.ParentDSTTL == 0 {
		st.ParentDSTTL = 24 * time.Hour
	}
	return st
}

func validate(st Settings) error {
	switch {
	case st.Algorithm != dns.ECDSAP256SHA256 && st.Algorithm != dns.RSASHA256:
		return fmt.Errorf("DNSSEC algorithm %d is not supported (13 or 8)", st.Algorithm)
	case st.NSECMode != "nsec" && st.NSECMode != "nsec3":
		return fmt.Errorf("nsec_mode %q must be nsec or nsec3", st.NSECMode)
	case st.PropagationDelay < time.Second || st.PropagationDelay > 7*24*time.Hour:
		return fmt.Errorf("propagation delay %v must be between 1 s and 7 days", st.PropagationDelay)
	case st.ParentDSTTL < time.Second || st.ParentDSTTL > 7*24*time.Hour:
		return fmt.Errorf("parent DS TTL %v must be between 1 s and 7 days", st.ParentDSTTL)
	case st.ZSKLifetimeDays < 0 || st.ZSKLifetimeDays > 3650:
		return fmt.Errorf("ZSK lifetime %d days must be between 0 and 3650", st.ZSKLifetimeDays)
	}
	return nil
}

// createKey generates an active key of role in st.KeyBackend whose key tag no key of the zone uses
// (a shared tag would make validators try the wrong key) and stores it.
// debt: a PKCS#11 key generated in a transaction that later rolls back stays in the token
// unreferenced; revisit with a sweep of token objects without a dnssec_keys row.
func createKey(ctx context.Context, tx pgx.Tx, box *secrets.Box, zoneID uuid.UUID, st Settings, role, dsState string) error {
	rows, err := tx.Query(ctx, "SELECT key_tag FROM dnssec_keys WHERE zone_id = $1", zoneID)
	if err != nil {
		return err
	}
	tags, err := pgx.CollectRows(rows, pgx.RowTo[int32])
	if err != nil {
		return err
	}
	used := map[uint16]bool{}
	for _, t := range tags {
		used[uint16(t)] = true
	}
	flags := uint16(256)
	if role == "ksk" {
		flags = 257
	}
	for range maxTagAttempts {
		sk, err := box.GenerateSigningKey(ctx, st.KeyBackend, st.Algorithm)
		if err != nil {
			return err
		}
		tag := (&dns.DNSKEY{Flags: flags, Protocol: 3, Algorithm: sk.Algorithm, PublicKey: sk.PublicKey}).KeyTag()
		if used[tag] {
			if err := box.DestroySigningKey(sk); err != nil {
				return err
			}
			continue
		}
		_, err = tx.Exec(ctx, `INSERT INTO dnssec_keys (zone_id, role, algorithm, key_tag, public_key, backend, key_ref, private_envelope,
			state, ds_state, activated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'active', $9, now())`,
			zoneID, role, int16(sk.Algorithm), int32(tag), sk.PublicKey, string(sk.Backend), sk.KeyRef, sk.Envelope, dsState)
		return store.MapError(err)
	}
	return fmt.Errorf("zone %s: no %s with an unused key tag after %d attempts", zoneID, role, maxTagAttempts)
}

// Disable turns signing off for zoneID: its keys are removed (token objects destroyed, envelopes
// dropped) and the signature cache is cleared. The caller rebuilds the zone unsigned.
func Disable(ctx context.Context, tx pgx.Tx, box *secrets.Box, zoneID uuid.UUID) error {
	if _, err := tx.Exec(ctx, "UPDATE zone_dnssec SET enabled = false, next_maintenance_at = NULL WHERE zone_id = $1", zoneID); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `UPDATE dnssec_keys SET state = 'removed', removed_at = now(), private_envelope = NULL
		WHERE zone_id = $1 AND state <> 'removed' RETURNING backend, key_ref`, zoneID)
	if err != nil {
		return err
	}
	removed, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (secrets.StoredKey, error) {
		var k secrets.StoredKey
		var backend string
		err := r.Scan(&backend, &k.KeyRef)
		k.Backend = secrets.Backend(backend)
		return k, err
	})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM zone_signatures WHERE zone_id = $1", zoneID); err != nil {
		return err
	}
	// Token objects go last, once every row change succeeded.
	// debt: they are destroyed before the transaction commits, so a later failure in the same
	// transaction leaves rows naming destroyed keys; revisit with a post-commit destroy queue.
	for _, k := range removed {
		if err := box.DestroySigningKey(k); err != nil {
			return fmt.Errorf("destroy PKCS#11 key: %w", err)
		}
	}
	return nil
}
