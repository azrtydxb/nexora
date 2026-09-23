package api

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/odoh"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// odohService adapts package odoh to ODoHService. Responses carry key metadata, never seeds.
type odohService struct {
	st    *store.Store
	keys  *odoh.Keys
	build snapshot.BuildConfig
}

// NewODoHService returns the ODoH settings and key backend over st and keys.
func NewODoHService(st *store.Store, keys *odoh.Keys, build snapshot.BuildConfig) ODoHService {
	return &odohService{st: st, keys: keys, build: build}
}

// Get returns the settings with the unexpired keys, newest first.
func (s *odohService) Get(ctx context.Context) (OdohSettings, error) {
	settings, err := odoh.GetSettings(ctx, s.st.Pool)
	if err != nil {
		return OdohSettings{}, err
	}
	return s.view(ctx, settings)
}

// Update validates in and publishes it as a new config version, audited as updateOdohSettings.
func (s *odohService) Update(ctx context.Context, actor auth.Actor, in OdohSettingsUpdate) (OdohSettings, error) {
	if in.ProxyTimeoutMs < math.MinInt32 || in.ProxyTimeoutMs > math.MaxInt32 {
		return OdohSettings{}, invalid("proxy_timeout_ms: must be between 100 and 10000")
	}
	if in.KeyRotationHours < math.MinInt32 || in.KeyRotationHours > math.MaxInt32 {
		return OdohSettings{}, invalid("key_rotation_hours: must be between 1 and 720")
	}
	want := odoh.Settings{TargetEnabled: in.TargetEnabled, ProxyEnabled: in.ProxyEnabled, ProxyTimeoutMS: int32(in.ProxyTimeoutMs),
		KeyRotationHours: int32(in.KeyRotationHours), ProxyTargets: make([]odoh.ProxyTarget, 0, len(in.ProxyTargets))}
	for _, t := range in.ProxyTargets {
		want.ProxyTargets = append(want.ProxyTargets, odoh.ProxyTarget{Host: t.Host, CAPEM: t.CaPem})
	}
	if err := odoh.Validate(want); err != nil {
		var verr *odoh.ValidationError
		if errors.As(err, &verr) {
			return OdohSettings{}, invalid("%s: %s", verr.Field, verr.Message)
		}
		return OdohSettings{}, err
	}
	var saved odoh.Settings
	_, err := snapshot.Mutate(ctx, s.st, s.build, actor, func(tx pgx.Tx) (auth.Change, error) {
		before, err := odoh.GetSettings(ctx, tx)
		if err != nil {
			return auth.Change{}, err
		}
		if saved, err = odoh.UpdateSettings(ctx, tx, want, in.Revision); err != nil {
			return auth.Change{}, err
		}
		return auth.Change{Action: "updateOdohSettings", TargetType: "odoh_settings", TargetID: "odoh",
			Before: odohAudit(before), After: odohAudit(saved)}, nil
	})
	if err != nil {
		return OdohSettings{}, err
	}
	return s.view(ctx, saved)
}

// Rotate forces a new key, audited as rotateOdohKey in the rotation's transaction. Without key
// storage it fails with secrets.ErrUnconfigured (503) and creates nothing.
func (s *odohService) Rotate(ctx context.Context, actor auth.Actor) (OdohSettings, error) {
	rotated, err := s.keys.Rotate(ctx, &actor)
	if err != nil {
		return OdohSettings{}, err
	}
	if !rotated {
		return OdohSettings{}, fmt.Errorf("%w: another management plane instance is rotating the ODoH keys; retry", store.ErrConflict)
	}
	return s.Get(ctx)
}

// odohAudit is the audit form of the settings (no keys exist in Settings).
func odohAudit(s odoh.Settings) map[string]any {
	return map[string]any{"target_enabled": s.TargetEnabled, "proxy_enabled": s.ProxyEnabled, "proxy_targets": s.ProxyTargets,
		"proxy_timeout_ms": s.ProxyTimeoutMS, "key_rotation_hours": s.KeyRotationHours, "revision": s.Revision}
}

func (s *odohService) view(ctx context.Context, settings odoh.Settings) (OdohSettings, error) {
	infos, err := s.keys.List(ctx)
	if err != nil {
		return OdohSettings{}, err
	}
	out := OdohSettings{TargetEnabled: settings.TargetEnabled, ProxyEnabled: settings.ProxyEnabled,
		ProxyTimeoutMs: int(settings.ProxyTimeoutMS), KeyRotationHours: int(settings.KeyRotationHours),
		Revision: settings.Revision, UpdatedAt: settings.UpdatedAt,
		ProxyTargets: make([]OdohProxyTarget, 0, len(settings.ProxyTargets)), Keys: make([]OdohKeyInfo, 0, len(infos))}
	for _, t := range settings.ProxyTargets {
		out.ProxyTargets = append(out.ProxyTargets, OdohProxyTarget{Host: t.Host, CaPem: t.CAPEM})
	}
	for _, k := range infos {
		out.Keys = append(out.Keys, OdohKeyInfo{Id: k.ID, CreatedAt: k.CreatedAt, PublishAfter: k.PublishAfter, NotAfter: k.NotAfter})
	}
	return out, nil
}
