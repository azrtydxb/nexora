package api

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/piwi3910/nexora/mgmt/internal/dnssec"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
)

func zoneDnssecOut(v *dnssec.View) ZoneDnssec {
	out := ZoneDnssec{
		Enabled: v.Enabled, Algorithm: ZoneDnssecAlgorithm(v.Settings.Algorithm), NsecMode: ZoneDnssecNsecMode(v.Settings.NSECMode),
		KeyBackend: string(v.Settings.KeyBackend), PropagationDelaySeconds: int(v.Settings.PropagationDelay / time.Second),
		ParentDsTtlSeconds: int(v.Settings.ParentDSTTL / time.Second), ZskLifetimeDays: v.Settings.ZSKLifetimeDays,
		Keys: make([]ZoneDnssecKey, 0, len(v.Keys)), Ds: v.DS, Dnskeys: v.DNSKEYs,
	}
	for _, k := range v.Keys {
		out.Keys = append(out.Keys, ZoneDnssecKey{
			Id: uuid.MustParse(k.ID), Role: ZoneDnssecKeyRole(k.Role), Algorithm: int(k.Algorithm), KeyTag: int(k.KeyTag), Flags: int(k.Flags),
			State: ZoneDnssecKeyState(k.State), DsState: ZoneDnssecKeyDsState(k.DSState), Backend: ZoneDnssecKeyBackend(k.Backend),
			PublicKey: k.PublicKey, PublishedAt: k.PublishedAt, ActivatedAt: k.ActivatedAt, RetiredAt: k.RetiredAt, RemovedAt: k.RemovedAt,
		})
	}
	return out
}

func (h *handlers) GetZoneDnssec(ctx context.Context, req GetZoneDnssecRequestObject) (GetZoneDnssecResponseObject, error) {
	v, err := h.d.ZoneDNSSEC.Get(ctx, req.ZoneId)
	if err != nil {
		return nil, err
	}
	return GetZoneDnssec200JSONResponse(zoneDnssecOut(v)), nil
}

// UpdateZoneDnssec applies the body on top of the zone's current settings (omitted fields keep
// their values; a zone never signed starts from the defaults).
func (h *handlers) UpdateZoneDnssec(ctx context.Context, req UpdateZoneDnssecRequestObject) (UpdateZoneDnssecResponseObject, error) {
	b := req.Body
	cur, err := h.d.ZoneDNSSEC.Get(ctx, req.ZoneId)
	if err != nil {
		return nil, err
	}
	st := cur.Settings
	if b.Algorithm != nil {
		st.Algorithm = uint8(*b.Algorithm)
		if !b.Algorithm.Valid() {
			return nil, invalid("algorithm must be 8 or 13")
		}
	}
	if b.NsecMode != nil {
		st.NSECMode = string(*b.NsecMode)
	}
	if b.KeyBackend != nil {
		st.KeyBackend = secrets.Backend(*b.KeyBackend)
	}
	for _, f := range []struct {
		name string
		v    *int
		dst  *time.Duration
	}{{"propagation_delay_seconds", b.PropagationDelaySeconds, &st.PropagationDelay}, {"parent_ds_ttl_seconds", b.ParentDsTtlSeconds, &st.ParentDSTTL}} {
		if f.v == nil {
			continue
		}
		if *f.v < 1 || *f.v > 604800 {
			return nil, invalid("%s must be 1..604800", f.name)
		}
		*f.dst = time.Duration(*f.v) * time.Second
	}
	if b.ZskLifetimeDays != nil {
		st.ZSKLifetimeDays = *b.ZskLifetimeDays
	}
	v, err := h.d.ZoneDNSSEC.Update(ctx, PrincipalFrom(ctx).Actor(), req.ZoneId, b.Revision, b.Enabled, st)
	if err != nil {
		return nil, err
	}
	return UpdateZoneDnssec200JSONResponse(zoneDnssecOut(v)), nil
}

func (h *handlers) StartZoneKeyRollover(ctx context.Context, req StartZoneKeyRolloverRequestObject) (StartZoneKeyRolloverResponseObject, error) {
	v, err := h.d.ZoneDNSSEC.StartRollover(ctx, PrincipalFrom(ctx).Actor(), req.ZoneId, string(req.Body.Role))
	if err != nil {
		return nil, err
	}
	return StartZoneKeyRollover202JSONResponse(zoneDnssecOut(v)), nil
}

func (h *handlers) ConfirmZoneKskDs(ctx context.Context, req ConfirmZoneKskDsRequestObject) (ConfirmZoneKskDsResponseObject, error) {
	v, err := h.d.ZoneDNSSEC.ConfirmDS(ctx, PrincipalFrom(ctx).Actor(), req.ZoneId, req.Body.KeyId)
	if err != nil {
		return nil, err
	}
	return ConfirmZoneKskDs200JSONResponse(zoneDnssecOut(v)), nil
}
