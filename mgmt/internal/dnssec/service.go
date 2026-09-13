package dnssec

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

// Errors of the DNSSEC API operations.
var (
	ErrAlgorithmRollover  = errors.New("changing the DNSSEC algorithm of a signed zone (an algorithm rollover) is not supported; disable and re-enable signing instead")
	ErrRolloverInProgress = errors.New("a rollover of this key role is already in progress")
	ErrNotPendingKSK      = errors.New("the key is not an active KSK whose DS is pending at the parent")
	ErrNotEnabled         = errors.New("DNSSEC is not enabled for this zone")
	ErrInvalidSettings    = errors.New("invalid DNSSEC settings")
)

// Service implements the zone DNSSEC API: settings, keys, DS records and rollover actions. Every
// change runs through zone.Service.Mutate, so it re-signs the zone and publishes a version.
type Service struct {
	Store *store.Store
	Box   *secrets.Box
	Zones *zone.Service
}

// View is a zone's DNSSEC state. DS holds the DS records (SHA-256) of the newest active KSK, the one
// the parent should list; DNSKEYs the served DNSKEY RRset.
type View struct {
	Enabled  bool
	Settings Settings
	Keys     []KeyView
	DS       []string
	DNSKEYs  []string
}

// KeyView is one signing key (removed keys included).
type KeyView struct {
	ID, Role, State, DSState, Backend string
	Algorithm                         uint8
	KeyTag, Flags                     uint16
	PublicKey                         string
	PublishedAt                       time.Time
	ActivatedAt, RetiredAt, RemovedAt *time.Time
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func (s *Service) now() time.Time {
	if s.Zones != nil && s.Zones.Now != nil {
		return s.Zones.Now()
	}
	return time.Now()
}

// loadSettings returns the stored settings of zoneID and whether signing is enabled; a zone without
// a settings row gets the defaults (ZSK lifetime 90 days) and enabled=false.
func loadSettings(ctx context.Context, q querier, box *secrets.Box, zoneID uuid.UUID) (Settings, bool, error) {
	var alg int16
	var mode, backend string
	var prop, parent, lifetime int32
	var enabled bool
	err := q.QueryRow(ctx, `SELECT enabled, algorithm, nsec_mode, key_backend, propagation_delay_seconds, parent_ds_ttl_seconds, zsk_lifetime_days
		FROM zone_dnssec WHERE zone_id = $1`, zoneID).Scan(&enabled, &alg, &mode, &backend, &prop, &parent, &lifetime)
	if errors.Is(err, pgx.ErrNoRows) {
		return withDefaults(Settings{ZSKLifetimeDays: 90}, box), false, nil
	}
	if err != nil {
		return Settings{}, false, store.MapError(err)
	}
	return Settings{Algorithm: uint8(alg), NSECMode: mode, KeyBackend: secrets.Backend(backend), PropagationDelay: time.Duration(prop) * time.Second,
		ParentDSTTL: time.Duration(parent) * time.Second, ZSKLifetimeDays: int(lifetime)}, enabled, nil
}

// loadKeyStates returns the keys of zoneID that are not removed, oldest first.
func loadKeyStates(ctx context.Context, q querier, zoneID uuid.UUID) ([]KeyState, error) {
	rows, err := q.Query(ctx, `SELECT id::text, role, state, ds_state, published_at, activated_at, retired_at, removed_at, ds_seen_at
		FROM dnssec_keys WHERE zone_id = $1 AND state <> 'removed' ORDER BY published_at, id`, zoneID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (KeyState, error) {
		var k KeyState
		return k, r.Scan(&k.ID, &k.Role, &k.State, &k.DSState, &k.PublishedAt, &k.ActivatedAt, &k.RetiredAt, &k.RemovedAt, &k.DSSeenAt)
	})
}

func policyOf(st Settings, soaTTL, maxTTL uint32) Policy {
	return Policy{
		DNSKEYTTL: time.Duration(soaTTL) * time.Second, MaxZoneTTL: time.Duration(max(soaTTL, maxTTL)) * time.Second,
		Propagation: st.PropagationDelay, ParentDSTTL: st.ParentDSTTL, ZSKLifetime: time.Duration(st.ZSKLifetimeDays) * 24 * time.Hour,
	}
}

// Get returns the DNSSEC state of zoneID.
func (s *Service) Get(ctx context.Context, zoneID uuid.UUID) (*View, error) {
	z, err := s.Zones.GetZone(ctx, zoneID)
	if err != nil {
		return nil, err
	}
	st, enabled, err := loadSettings(ctx, s.Store.Pool, s.Box, zoneID)
	if err != nil {
		return nil, err
	}
	v := &View{Enabled: enabled, Settings: st, Keys: []KeyView{}, DS: []string{}, DNSKEYs: []string{}}
	rows, err := s.Store.Pool.Query(ctx, `SELECT id::text, role, state, ds_state, backend, algorithm, key_tag, public_key, published_at,
		activated_at, retired_at, removed_at FROM dnssec_keys WHERE zone_id = $1 ORDER BY published_at, id`, zoneID)
	if err != nil {
		return nil, store.MapError(err)
	}
	v.Keys, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (KeyView, error) {
		var k KeyView
		var alg int16
		var tag int32
		err := r.Scan(&k.ID, &k.Role, &k.State, &k.DSState, &k.Backend, &alg, &tag, &k.PublicKey, &k.PublishedAt, &k.ActivatedAt, &k.RetiredAt, &k.RemovedAt)
		k.Algorithm, k.KeyTag, k.Flags = uint8(alg), uint16(tag), 256
		if k.Role == "ksk" {
			k.Flags = 257
		}
		return k, err
	})
	if err != nil {
		return nil, store.MapError(err)
	}
	var newest *dns.DNSKEY
	var newestAt time.Time
	for _, k := range v.Keys {
		if k.State == "removed" {
			continue
		}
		key := &dns.DNSKEY{Hdr: hdr(z.Name, dns.TypeDNSKEY, z.SOA.TTL), Flags: k.Flags, Protocol: 3, Algorithm: k.Algorithm, PublicKey: k.PublicKey}
		v.DNSKEYs = append(v.DNSKEYs, key.String())
		if k.Role == "ksk" && k.State == "active" && k.ActivatedAt != nil && !k.ActivatedAt.Before(newestAt) {
			newest, newestAt = key, *k.ActivatedAt
		}
	}
	if newest != nil {
		if ds := newest.ToDS(dns.SHA256); ds != nil {
			v.DS = append(v.DS, ds.String())
		}
	}
	return v, nil
}

type settingsAudit struct {
	Enabled  bool     `json:"enabled"`
	Settings Settings `json:"settings"`
}

// Update enables (creating keys when the zone has none), reconfigures or disables signing of zoneID
// at revision. Changing the algorithm of a signed zone is refused with ErrAlgorithmRollover.
func (s *Service) Update(ctx context.Context, actor auth.Actor, zoneID uuid.UUID, revision int64, enabled bool, st Settings) (*View, error) {
	_, err := s.Zones.Mutate(ctx, zoneID, func(tx pgx.Tx, z *zone.Zone) (string, any, any, zone.RebuildOptions, error) {
		if z.Revision != revision {
			return "", nil, nil, zone.RebuildOptions{}, fmt.Errorf("zone revision %d is stale (current %d): %w", revision, z.Revision, store.ErrConflict)
		}
		cur, wasEnabled, err := loadSettings(ctx, tx, s.Box, zoneID)
		if err != nil {
			return "", nil, nil, zone.RebuildOptions{}, err
		}
		before, after := settingsAudit{wasEnabled, cur}, settingsAudit{enabled, st}
		if enabled {
			if s.Box.Configured() && wasEnabled && withDefaults(st, s.Box).Algorithm != cur.Algorithm {
				err = ErrAlgorithmRollover
			} else {
				err = Enable(ctx, tx, s.Box, zoneID, st)
			}
		} else {
			err = Disable(ctx, tx, zoneID)
		}
		return "updateZoneDnssec", before, after, zone.RebuildOptions{Force: true}, err
	}, actor)
	if err != nil {
		return nil, err
	}
	s.destroyPending(ctx)
	return s.Get(ctx, zoneID)
}

// StartRollover starts a ZSK pre-publish rollover (a new published ZSK) or a KSK double-signature
// rollover (a new active KSK whose DS is pending) on zoneID.
func (s *Service) StartRollover(ctx context.Context, actor auth.Actor, zoneID uuid.UUID, role string) (*View, error) {
	if role != "zsk" && role != "ksk" {
		return nil, fmt.Errorf("%w: role %q must be zsk or ksk", ErrInvalidSettings, role)
	}
	_, err := s.Zones.Mutate(ctx, zoneID, func(tx pgx.Tx, z *zone.Zone) (string, any, any, zone.RebuildOptions, error) {
		st, enabled, err := loadSettings(ctx, tx, s.Box, zoneID)
		if err != nil {
			return "", nil, nil, zone.RebuildOptions{}, err
		}
		if !enabled {
			return "", nil, nil, zone.RebuildOptions{}, ErrNotEnabled
		}
		keys, err := loadKeyStates(ctx, tx, zoneID)
		if err != nil {
			return "", nil, nil, zone.RebuildOptions{}, err
		}
		busy := 0
		for _, k := range keys {
			switch {
			case role == "zsk" && k.Role == "zsk" && (k.State == "published" || k.State == "retired"):
				busy++
			case role == "ksk" && k.Role == "ksk" && k.State == "active":
				busy++
			}
		}
		audit := map[string]string{"role": role}
		if role == "zsk" {
			if busy > 0 {
				return "", nil, nil, zone.RebuildOptions{}, ErrRolloverInProgress
			}
			return "startZoneKeyRollover", nil, audit, zone.RebuildOptions{}, createKey(ctx, tx, s.Box, zoneID, st, "zsk", "published", "none", s.now())
		}
		if busy >= 2 {
			return "", nil, nil, zone.RebuildOptions{}, ErrRolloverInProgress
		}
		return "startZoneKeyRollover", nil, audit, zone.RebuildOptions{}, createKey(ctx, tx, s.Box, zoneID, st, "ksk", "active", "pending", s.now())
	}, actor)
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, zoneID)
}

// ConfirmDS records that the parent publishes the DS of KSK keyID: its CDS/CDNSKEY are withdrawn
// and, in a double-signature rollover, the older KSK is removed after the parent DS TTL.
func (s *Service) ConfirmDS(ctx context.Context, actor auth.Actor, zoneID, keyID uuid.UUID) (*View, error) {
	_, err := s.Zones.Mutate(ctx, zoneID, func(tx pgx.Tx, z *zone.Zone) (string, any, any, zone.RebuildOptions, error) {
		tag, err := tx.Exec(ctx, `UPDATE dnssec_keys SET ds_state = 'seen', ds_seen_at = $3
			WHERE id = $1 AND zone_id = $2 AND role = 'ksk' AND state = 'active' AND ds_state = 'pending'`, keyID, zoneID, s.now())
		if err == nil && tag.RowsAffected() == 0 {
			err = ErrNotPendingKSK
		}
		return "confirmZoneKskDs", nil, map[string]string{"key_id": keyID.String()}, zone.RebuildOptions{}, err
	}, actor)
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, zoneID)
}

// advance applies the key transitions due at now to z inside tx: state changes are stored (removed
// keys lose their envelope; PKCS#11 ones are queued for destruction) and a ZSK whose lifetime
// ended gets a pre-published successor.
func (s *Service) advance(ctx context.Context, tx pgx.Tx, z *zone.Zone, now time.Time) error {
	st, enabled, err := loadSettings(ctx, tx, s.Box, z.ID)
	if err != nil || !enabled {
		return err
	}
	keys, err := loadKeyStates(ctx, tx, z.ID)
	if err != nil {
		return err
	}
	var maxTTL int64
	if err := tx.QueryRow(ctx, "SELECT coalesce(max(ttl), 0) FROM zone_records WHERE zone_id = $1", z.ID).Scan(&maxTTL); err != nil {
		return err
	}
	out, act, _ := Advance(keys, policyOf(st, z.SOA.TTL, uint32(maxTTL)), now)
	for i, k := range out {
		if k.State == keys[i].State {
			continue
		}
		rows, err := tx.Query(ctx, `UPDATE dnssec_keys SET state = $2, activated_at = $3, retired_at = $4, removed_at = $5,
			private_envelope = CASE WHEN $2::text = 'removed' THEN NULL ELSE private_envelope END
			WHERE id = $1 RETURNING backend, key_ref`, k.ID, k.State, k.ActivatedAt, k.RetiredAt, k.RemovedAt)
		if err != nil {
			return err
		}
		changed, err := pgx.CollectRows(rows, scanStoredRef)
		if err != nil {
			return err
		}
		if k.State == "removed" {
			if err := enqueueDestruction(ctx, tx, changed); err != nil {
				return err
			}
		}
	}
	if act.CreateZSK {
		return createKey(ctx, tx, s.Box, z.ID, st, "zsk", "published", "none", now)
	}
	return nil
}

// destroyPending runs DestroyPending after a committed change; failures stay queued for the
// maintainer.
func (s *Service) destroyPending(ctx context.Context) {
	if err := DestroyPending(ctx, s.Store, s.Box); err != nil {
		slog.Warn("destroy removed PKCS#11 signing keys; the DNSSEC maintainer retries", "err", err)
	}
}
