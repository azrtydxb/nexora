package api

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/dnssecconf"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// maxNTALifetime bounds how far ahead a negative trust anchor may expire.
const maxNTALifetime = 30 * 24 * time.Hour

var trustAnchorStates = map[controlv1.TrustAnchorState]DnssecStatusEnginesTrustAnchorsState{
	controlv1.TrustAnchorState_TRUST_ANCHOR_STATE_CONFIGURED: DnssecStatusEnginesTrustAnchorsStateConfigured,
	controlv1.TrustAnchorState_TRUST_ANCHOR_STATE_ADD_PEND:   DnssecStatusEnginesTrustAnchorsStateAddPend,
	controlv1.TrustAnchorState_TRUST_ANCHOR_STATE_VALID:      DnssecStatusEnginesTrustAnchorsStateValid,
	controlv1.TrustAnchorState_TRUST_ANCHOR_STATE_MISSING:    DnssecStatusEnginesTrustAnchorsStateMissing,
	controlv1.TrustAnchorState_TRUST_ANCHOR_STATE_REVOKED:    DnssecStatusEnginesTrustAnchorsStateRevoked,
}

func dnssecOut(s store.DnssecSettings) DnssecSettings {
	return DnssecSettings{Validation: s.Validation, ValidateForwarded: s.ValidateForwarded, Rfc5011: s.RFC5011, Revision: s.Revision}
}

func trustAnchorOut(a store.TrustAnchor) TrustAnchor {
	return TrustAnchor{Id: a.ID, Zone: a.Zone, Ds: a.DS, Source: TrustAnchorSource(a.Source), CreatedAt: a.CreatedAt}
}

func ntaOut(n store.NegativeTrustAnchor) NegativeTrustAnchor {
	return NegativeTrustAnchor{Id: n.ID, Domain: n.Domain, Reason: n.Reason, ExpiresAt: n.ExpiresAt, CreatedBy: n.CreatedBy, CreatedAt: n.CreatedAt}
}

func (h *handlers) GetDnssecSettings(ctx context.Context, _ GetDnssecSettingsRequestObject) (GetDnssecSettingsResponseObject, error) {
	s, err := store.GetDnssecSettings(ctx, h.d.Store.Pool)
	if err != nil {
		return nil, err
	}
	return GetDnssecSettings200JSONResponse(dnssecOut(s)), nil
}

func (h *handlers) UpdateDnssecSettings(ctx context.Context, req UpdateDnssecSettingsRequestObject) (UpdateDnssecSettingsResponseObject, error) {
	b := req.Body
	var after DnssecSettings
	err := h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := store.GetDnssecSettings(ctx, tx)
		if err != nil {
			return auth.Change{}, err
		}
		updated, err := store.UpdateDnssecSettings(ctx, tx, store.DnssecSettings{
			Validation: b.Validation, ValidateForwarded: b.ValidateForwarded, RFC5011: b.Rfc5011, Revision: b.Revision,
		})
		after = dnssecOut(updated)
		return auth.Change{Action: "updateDnssecSettings", TargetType: "dnssec", TargetID: "global", Before: dnssecOut(before), After: after}, err
	})
	if err != nil {
		return nil, err
	}
	return UpdateDnssecSettings200JSONResponse(after), nil
}

// GetDnssecStatus reports each live engine's latest DnssecStats; unreadable reports are skipped.
func (h *handlers) GetDnssecStatus(ctx context.Context, _ GetDnssecStatusRequestObject) (GetDnssecStatusResponseObject, error) {
	reports, err := store.ListEngineDnssecStatus(ctx, h.d.Store.Pool)
	if err != nil {
		return nil, err
	}
	var out DnssecStatus
	out.Engines = makeOf(out.Engines, 0)
	for _, r := range reports {
		var st controlv1.DnssecStats
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(r.Stats, &st); err != nil {
			continue
		}
		out.Engines = append(out.Engines, makeOf(out.Engines, 1)...)
		e := &out.Engines[len(out.Engines)-1]
		e.EngineId, e.EngineName, e.ReportedAt = r.EngineID, r.EngineName, r.ReportedAt
		e.Secure, e.Insecure, e.Bogus, e.Indeterminate = clampInt64(st.Secure), clampInt64(st.Insecure), clampInt64(st.Bogus), clampInt64(st.Indeterminate)
		e.ActiveNegativeTrustAnchors = int(st.ActiveNegativeTrustAnchors)
		e.TrustAnchors = makeOf(e.TrustAnchors, len(st.TrustAnchors))
		for i, a := range st.TrustAnchors {
			t := &e.TrustAnchors[i]
			t.Zone, t.KeyTag, t.Algorithm, t.LastError = a.Zone, int(a.KeyTag), int(a.Algorithm), a.LastError
			t.State = trustAnchorStates[a.State]
			if t.State == "" {
				t.State = DnssecStatusEnginesTrustAnchorsStateConfigured
			}
			t.LastRefreshSuccess, t.HoldDownUntil = unixOrNil(a.LastRefreshSuccessUnix), unixOrNil(a.HoldDownUntilUnix)
		}
	}
	return GetDnssecStatus200JSONResponse(out), nil
}

func unixOrNil(sec int64) *time.Time {
	if sec == 0 {
		return nil
	}
	t := time.Unix(sec, 0).UTC()
	return &t
}

func clampInt64(v uint64) int64 { return int64(min(v, 1<<63-1)) }

func (h *handlers) ListTrustAnchors(ctx context.Context, _ ListTrustAnchorsRequestObject) (ListTrustAnchorsResponseObject, error) {
	anchors, err := store.ListTrustAnchors(ctx, h.d.Store.Pool)
	if err != nil {
		return nil, err
	}
	out := make(ListTrustAnchors200JSONResponse, 0, len(anchors))
	for _, a := range anchors {
		out = append(out, trustAnchorOut(a))
	}
	return out, nil
}

func (h *handlers) CreateTrustAnchor(ctx context.Context, req CreateTrustAnchorRequestObject) (CreateTrustAnchorResponseObject, error) {
	zone, err := dnssecconf.ValidateDomain(req.Body.Zone)
	if err != nil {
		return nil, invalid("zone: %s", err.Error())
	}
	ds := strings.ToUpper(strings.Join(strings.Fields(req.Body.Ds), " "))
	if err := dnssecconf.ValidateDS(ds); err != nil {
		return nil, invalid("ds: %s", err.Error())
	}
	var after TrustAnchor
	err = h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		created, err := store.CreateTrustAnchor(ctx, tx, zone, ds)
		after = trustAnchorOut(created)
		return auth.Change{Action: "createTrustAnchor", TargetType: "trust_anchor", TargetID: created.ID.String(), After: after}, err
	})
	if err != nil {
		return nil, err
	}
	return CreateTrustAnchor201JSONResponse(after), nil
}

func (h *handlers) DeleteTrustAnchor(ctx context.Context, req DeleteTrustAnchorRequestObject) (DeleteTrustAnchorResponseObject, error) {
	err := h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		deleted, err := store.DeleteTrustAnchor(ctx, tx, req.Id)
		return auth.Change{Action: "deleteTrustAnchor", TargetType: "trust_anchor", TargetID: req.Id.String(), Before: trustAnchorOut(deleted)}, err
	})
	if err != nil {
		return nil, err
	}
	return DeleteTrustAnchor204Response{}, nil
}

func (h *handlers) ListNegativeTrustAnchors(ctx context.Context, _ ListNegativeTrustAnchorsRequestObject) (ListNegativeTrustAnchorsResponseObject, error) {
	ntas, err := store.ListNegativeTrustAnchors(ctx, h.d.Store.Pool)
	if err != nil {
		return nil, err
	}
	out := make(ListNegativeTrustAnchors200JSONResponse, 0, len(ntas))
	for _, n := range ntas {
		out = append(out, ntaOut(n))
	}
	return out, nil
}

func (h *handlers) CreateNegativeTrustAnchor(ctx context.Context, req CreateNegativeTrustAnchorRequestObject) (CreateNegativeTrustAnchorResponseObject, error) {
	b := req.Body
	domain, err := dnssecconf.ValidateDomain(b.Domain)
	if err != nil {
		return nil, invalid("domain: %s", err.Error())
	}
	now := time.Now()
	if !b.ExpiresAt.After(now) || b.ExpiresAt.After(now.Add(maxNTALifetime)) {
		return nil, invalid("expires_at must be in the future and at most 30 days ahead")
	}
	reason := ""
	if b.Reason != nil {
		reason = *b.Reason
	}
	if len([]rune(reason)) > 500 {
		return nil, invalid("reason must be at most 500 characters")
	}
	var after NegativeTrustAnchor
	err = h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		created, err := store.CreateNegativeTrustAnchor(ctx, tx, store.NegativeTrustAnchor{
			Domain: domain, Reason: reason, ExpiresAt: b.ExpiresAt, CreatedBy: PrincipalFrom(ctx).Actor().Name,
		})
		after = ntaOut(created)
		return auth.Change{Action: "createNegativeTrustAnchor", TargetType: "negative_trust_anchor", TargetID: created.ID.String(), After: after}, err
	})
	if err != nil {
		return nil, err
	}
	return CreateNegativeTrustAnchor201JSONResponse(after), nil
}

func (h *handlers) DeleteNegativeTrustAnchor(ctx context.Context, req DeleteNegativeTrustAnchorRequestObject) (DeleteNegativeTrustAnchorResponseObject, error) {
	err := h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		deleted, err := store.DeleteNegativeTrustAnchor(ctx, tx, req.Id)
		return auth.Change{Action: "deleteNegativeTrustAnchor", TargetType: "negative_trust_anchor", TargetID: req.Id.String(), Before: ntaOut(deleted)}, err
	})
	if err != nil {
		return nil, err
	}
	return DeleteNegativeTrustAnchor204Response{}, nil
}
