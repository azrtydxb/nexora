package api

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/dnssecconf"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

func resolutionOut(s store.ResolutionSettings) ResolutionSettings {
	out := ResolutionSettings{
		Mode: ResolutionSettingsMode(s.Mode), QnameMinimisation: s.QnameMinimisation, AggressiveNsec: s.AggressiveNSEC,
		MaxUpstreamQueries: int(s.MaxUpstreamQueries), MaxDelegationDepth: int(s.MaxDelegationDepth),
		AuthorityPort: int(s.AuthorityPort), Revision: s.Revision, RootHints: make([]RootHint, 0, len(s.RootHints)),
	}
	for _, h := range s.RootHints {
		out.RootHints = append(out.RootHints, RootHint{Name: h.Name, Addresses: append([]string{}, h.Addresses...)})
	}
	return out
}

func forwardZoneOut(z store.ForwardZone) ForwardZone {
	return ForwardZone{Id: z.ID, Domain: z.Domain, Addresses: append([]string{}, z.Addresses...), Validate: z.Validate, Revision: z.Revision}
}

func (h *handlers) GetResolutionSettings(ctx context.Context, _ GetResolutionSettingsRequestObject) (GetResolutionSettingsResponseObject, error) {
	s, err := store.GetResolutionSettings(ctx, h.d.Store.Pool)
	if err != nil {
		return nil, err
	}
	return GetResolutionSettings200JSONResponse(resolutionOut(s)), nil
}

func (h *handlers) UpdateResolutionSettings(ctx context.Context, req UpdateResolutionSettingsRequestObject) (UpdateResolutionSettingsResponseObject, error) {
	s, err := validateResolution(*req.Body)
	if err != nil {
		return nil, err
	}
	var after ResolutionSettings
	err = h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := store.GetResolutionSettings(ctx, tx)
		if err != nil {
			return auth.Change{}, err
		}
		updated, err := store.UpdateResolutionSettings(ctx, tx, s)
		after = resolutionOut(updated)
		return auth.Change{Action: "updateResolutionSettings", TargetType: "resolution", TargetID: "global",
			Before: resolutionOut(before), After: after}, err
	})
	if err != nil {
		return nil, err
	}
	return UpdateResolutionSettings200JSONResponse(after), nil
}

func validateResolution(b ResolutionSettings) (store.ResolutionSettings, error) {
	if !b.Mode.Valid() {
		return store.ResolutionSettings{}, invalid("mode must be forward or recursive")
	}
	switch {
	case b.MaxUpstreamQueries < 1 || b.MaxUpstreamQueries > 1000:
		return store.ResolutionSettings{}, invalid("max_upstream_queries must be 1..1000")
	case b.MaxDelegationDepth < 1 || b.MaxDelegationDepth > 64:
		return store.ResolutionSettings{}, invalid("max_delegation_depth must be 1..64")
	case b.AuthorityPort < 1 || b.AuthorityPort > 65535:
		return store.ResolutionSettings{}, invalid("authority_port must be 1..65535")
	}
	hints := make([]store.RootHint, 0, len(b.RootHints))
	for _, rh := range b.RootHints {
		hints = append(hints, store.RootHint{Name: rh.Name, Addresses: rh.Addresses})
	}
	if err := dnssecconf.ValidateRootHints(hints); err != nil {
		return store.ResolutionSettings{}, invalid("%s", err.Error())
	}
	for i := range hints {
		name, err := dnssecconf.ValidateDomain(hints[i].Name)
		if err != nil {
			return store.ResolutionSettings{}, invalid("root_hints[%d].name: %s", i, err.Error())
		}
		hints[i].Name = name
	}
	return store.ResolutionSettings{
		Mode: string(b.Mode), QnameMinimisation: b.QnameMinimisation, AggressiveNSEC: b.AggressiveNsec,
		MaxUpstreamQueries: int32(b.MaxUpstreamQueries), MaxDelegationDepth: int32(b.MaxDelegationDepth),
		AuthorityPort: int32(b.AuthorityPort), RootHints: hints, Revision: b.Revision,
	}, nil
}

func validateForwardZone(domain string, addresses []string) (string, error) {
	d, err := dnssecconf.ValidateDomain(domain)
	if err != nil {
		return "", invalid("domain: %s", err.Error())
	}
	if len(addresses) < 1 || len(addresses) > 16 {
		return "", invalid("addresses must list 1..16 servers")
	}
	if err := dnssecconf.ValidateForwardAddresses(addresses); err != nil {
		return "", invalid("%s", err.Error())
	}
	return d, nil
}

func (h *handlers) ListForwardZones(ctx context.Context, _ ListForwardZonesRequestObject) (ListForwardZonesResponseObject, error) {
	zones, err := store.ListForwardZones(ctx, h.d.Store.Pool)
	if err != nil {
		return nil, err
	}
	out := make(ListForwardZones200JSONResponse, 0, len(zones))
	for _, z := range zones {
		out = append(out, forwardZoneOut(z))
	}
	return out, nil
}

func (h *handlers) CreateForwardZone(ctx context.Context, req CreateForwardZoneRequestObject) (CreateForwardZoneResponseObject, error) {
	b := req.Body
	domain, err := validateForwardZone(b.Domain, b.Addresses)
	if err != nil {
		return nil, err
	}
	var after ForwardZone
	err = h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		created, err := store.CreateForwardZone(ctx, tx, store.ForwardZone{Domain: domain, Addresses: b.Addresses, Validate: b.Validate})
		after = forwardZoneOut(created)
		return auth.Change{Action: "createForwardZone", TargetType: "forward_zone", TargetID: created.ID.String(), After: after}, err
	})
	if err != nil {
		return nil, err
	}
	return CreateForwardZone201JSONResponse(after), nil
}

func (h *handlers) UpdateForwardZone(ctx context.Context, req UpdateForwardZoneRequestObject) (UpdateForwardZoneResponseObject, error) {
	b := req.Body
	domain, err := validateForwardZone(b.Domain, b.Addresses)
	if err != nil {
		return nil, err
	}
	var after ForwardZone
	err = h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := store.GetForwardZone(ctx, tx, req.Id)
		if err != nil {
			return auth.Change{}, err
		}
		updated, err := store.UpdateForwardZone(ctx, tx, store.ForwardZone{ID: req.Id, Domain: domain, Addresses: b.Addresses, Validate: b.Validate, Revision: b.Revision})
		after = forwardZoneOut(updated)
		return auth.Change{Action: "updateForwardZone", TargetType: "forward_zone", TargetID: req.Id.String(),
			Before: forwardZoneOut(before), After: after}, err
	})
	if err != nil {
		return nil, err
	}
	return UpdateForwardZone200JSONResponse(after), nil
}

func (h *handlers) DeleteForwardZone(ctx context.Context, req DeleteForwardZoneRequestObject) (DeleteForwardZoneResponseObject, error) {
	err := h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := store.GetForwardZone(ctx, tx, req.Id)
		if err != nil {
			return auth.Change{}, err
		}
		return auth.Change{Action: "deleteForwardZone", TargetType: "forward_zone", TargetID: req.Id.String(), Before: forwardZoneOut(before)},
			store.DeleteForwardZone(ctx, tx, req.Id, req.Params.Revision)
	})
	if err != nil {
		return nil, err
	}
	return DeleteForwardZone204Response{}, nil
}
