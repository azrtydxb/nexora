package api

import (
	"context"
	"errors"
	"net/http"
	"net/netip"

	"github.com/google/uuid"

	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

func zoneEndpointsOut(eps []zone.Endpoint) []ZoneEndpoint {
	out := make([]ZoneEndpoint, 0, len(eps))
	for _, ep := range eps {
		out = append(out, ZoneEndpoint{Address: ep.Address, TsigKeyId: ep.TSIGKeyID})
	}
	return out
}

func zoneEndpointsIn(eps []ZoneEndpoint) []zone.Endpoint {
	out := make([]zone.Endpoint, 0, len(eps))
	for _, ep := range eps {
		out = append(out, zone.Endpoint{Address: ep.Address, TSIGKeyID: ep.TsigKeyId})
	}
	return out
}

// prefixesOut renders prefixes as strings; the result is never nil.
func prefixesOut(ps []netip.Prefix) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.String())
	}
	return out
}

func zoneOut(z *zone.Zone) Zone {
	updateCIDRs := prefixesOut(z.UpdateAllowCIDRs)
	out := Zone{
		Id: z.ID, EngineGroupId: z.EngineGroupID, Name: z.Name, Kind: ZoneKind(z.Kind), Revision: z.Revision, Serial: int64(z.Serial),
		DefaultTtl: int64(z.DefaultTTL), DnssecEnabled: z.DNSSECEnabled, CreatedAt: z.CreatedAt, UpdatedAt: z.UpdatedAt,
		Soa: ZoneSOA{Mname: z.SOA.MName, Rname: z.SOA.RName, Refresh: int64(z.SOA.Refresh), Retry: int64(z.SOA.Retry),
			Expire: int64(z.SOA.Expire), Minimum: int64(z.SOA.Minimum), Ttl: int64(z.SOA.TTL)},
		Transfer:        ZoneTransfer{AllowCidrs: prefixesOut(z.TransferAllowCIDRs), TsigKeyId: z.TransferTSIGKeyID},
		Notify:          zoneEndpointsOut(z.Notify),
		Update:          ZoneUpdatePolicy{TsigKeyIds: append([]uuid.UUID{}, z.UpdateTSIGKeyIDs...), AllowCidrs: &updateCIDRs},
		Primaries:       zoneEndpointsOut(z.Primaries),
		AllowQueryCidrs: prefixesOut(z.AllowQueryCIDRs),
	}
	if z.Kind == "secondary" {
		out.SecondaryStatus = &ZoneSecondaryStatus{
			LastRefreshAt: z.LastRefreshAt, LastSuccessAt: z.LastSuccessAt, NextRefreshAt: z.NextRefreshAt, ExpiresAt: z.ExpiresAt,
			Expired: z.Expired, LastError: z.LastError, LastTrigger: z.LastTrigger,
		}
	}
	return out
}

func recordOut(r *zone.Record) Record {
	return Record{Id: r.ID, Name: r.Name, Type: r.Type, Ttl: int64(r.TTL), Data: r.Data, Revision: r.Revision}
}

// u32 range-checks an API integer that the service stores as uint32.
func u32(field string, v int64) (uint32, error) {
	if v < 0 || v > 2147483647 {
		return 0, invalid("%s must be 0..2147483647", field)
	}
	return uint32(v), nil
}

// soaIn converts an SOA input; nil timers take the values of base.
func soaIn(in ZoneSOAInput, base zone.SOA) (zone.SOA, error) {
	out := base
	out.MName, out.RName = in.Mname, in.Rname
	for _, f := range []struct {
		name string
		v    *int64
		dst  *uint32
	}{
		{"soa.refresh", in.Refresh, &out.Refresh}, {"soa.retry", in.Retry, &out.Retry}, {"soa.expire", in.Expire, &out.Expire},
		{"soa.minimum", in.Minimum, &out.Minimum}, {"soa.ttl", in.Ttl, &out.TTL},
	} {
		if f.v == nil {
			continue
		}
		v, err := u32(f.name, *f.v)
		if err != nil {
			return out, err
		}
		*f.dst = v
	}
	return out, nil
}

func transferIn(t *ZoneTransfer) zone.TransferInput {
	if t == nil {
		return zone.TransferInput{}
	}
	return zone.TransferInput{AllowCIDRs: t.AllowCidrs, TSIGKeyID: t.TsigKeyId}
}

func recordIn(name string, typ string, ttl int64, data string) (zone.RecordInput, error) {
	t, err := u32("ttl", ttl)
	return zone.RecordInput{Name: name, Type: typ, TTL: t, Data: data}, err
}

func (h *handlers) ListZones(ctx context.Context, _ ListZonesRequestObject) (ListZonesResponseObject, error) {
	zones, err := h.d.Zones.ListZones(ctx)
	if err != nil {
		return nil, err
	}
	out := make(ListZones200JSONResponse, 0, len(zones))
	for i := range zones {
		out = append(out, zoneOut(&zones[i]))
	}
	return out, nil
}

func (h *handlers) GetZone(ctx context.Context, req GetZoneRequestObject) (GetZoneResponseObject, error) {
	z, err := h.d.Zones.GetZone(ctx, req.ZoneId)
	if err != nil {
		return nil, err
	}
	return GetZone200JSONResponse(zoneOut(z)), nil
}

func (h *handlers) CreateZone(ctx context.Context, req CreateZoneRequestObject) (CreateZoneResponseObject, error) {
	b := req.Body
	in := zone.CreateZoneInput{Name: b.Name, Kind: string(b.Kind), DefaultTTL: 3600, Transfer: transferIn(b.Transfer), EngineGroupID: b.EngineGroupId}
	if b.DefaultTtl != nil {
		ttl, err := u32("default_ttl", *b.DefaultTtl)
		if err != nil {
			return nil, err
		}
		in.DefaultTTL = ttl
	}
	switch {
	case b.Soa != nil:
		soa, err := soaIn(*b.Soa, zone.SOA{})
		if err != nil {
			return nil, err
		}
		in.SOA = soa
	case b.Kind == ZoneCreateKindPrimary:
		return nil, invalid("soa is required for primary zones")
	}
	if b.Nameservers != nil {
		in.Nameservers = *b.Nameservers
	}
	if b.Primaries != nil {
		in.Primaries = zoneEndpointsIn(*b.Primaries)
	}
	if b.Notify != nil {
		in.Notify = zoneEndpointsIn(*b.Notify)
	}
	if b.AllowQueryCidrs != nil {
		cidrs, err := maskedCIDRs("allow_query_cidrs", *b.AllowQueryCidrs)
		if err != nil {
			return nil, err
		}
		in.AllowQueryCIDRs = cidrs
	}
	if b.Update != nil {
		in.UpdateTSIGKeyIDs = b.Update.TsigKeyIds
		if b.Update.AllowCidrs != nil {
			cidrs, err := maskedCIDRs("update.allow_cidrs", *b.Update.AllowCidrs)
			if err != nil {
				return nil, err
			}
			in.UpdateAllowCIDRs = cidrs
		}
	}
	z, err := h.d.Zones.CreateZone(ctx, PrincipalFrom(ctx).Actor(), in)
	if errors.Is(err, zone.ErrUnknownEngineGroup) {
		return nil, coded(http.StatusUnprocessableEntity, "engine_group_not_found", "engine group %s does not exist", *b.EngineGroupId)
	}
	if err != nil {
		return nil, err
	}
	return CreateZone201JSONResponse(zoneOut(z)), nil
}

func (h *handlers) RefreshZone(ctx context.Context, req RefreshZoneRequestObject) (RefreshZoneResponseObject, error) {
	if err := h.d.Zones.RefreshNow(ctx, req.ZoneId); err != nil {
		return nil, err
	}
	return RefreshZone202Response{}, nil
}

func (h *handlers) UpdateZone(ctx context.Context, req UpdateZoneRequestObject) (UpdateZoneResponseObject, error) {
	b := req.Body
	in := zone.UpdateZoneInput{Revision: b.Revision}
	if b.DefaultTtl != nil {
		ttl, err := u32("default_ttl", *b.DefaultTtl)
		if err != nil {
			return nil, err
		}
		in.DefaultTTL = &ttl
	}
	if b.Soa != nil {
		current, err := h.d.Zones.GetZone(ctx, req.ZoneId)
		if err != nil {
			return nil, err
		}
		soa, err := soaIn(*b.Soa, current.SOA)
		if err != nil {
			return nil, err
		}
		in.SOA = &soa
	}
	if b.Primaries != nil {
		eps := zoneEndpointsIn(*b.Primaries)
		in.Primaries = &eps
	}
	if b.Notify != nil {
		eps := zoneEndpointsIn(*b.Notify)
		in.Notify = &eps
	}
	if b.Transfer != nil {
		t := transferIn(b.Transfer)
		in.Transfer = &t
	}
	if b.AllowQueryCidrs != nil {
		cidrs, err := maskedCIDRs("allow_query_cidrs", *b.AllowQueryCidrs)
		if err != nil {
			return nil, err
		}
		in.AllowQueryCIDRs = &cidrs
	}
	if b.Update != nil {
		ids := b.Update.TsigKeyIds
		in.UpdateTSIGKeyIDs = &ids
		if b.Update.AllowCidrs != nil {
			cidrs, err := maskedCIDRs("update.allow_cidrs", *b.Update.AllowCidrs)
			if err != nil {
				return nil, err
			}
			in.UpdateAllowCIDRs = &cidrs
		}
	}
	z, err := h.d.Zones.UpdateZone(ctx, PrincipalFrom(ctx).Actor(), req.ZoneId, in)
	if err != nil {
		return nil, err
	}
	return UpdateZone200JSONResponse(zoneOut(z)), nil
}

func (h *handlers) DeleteZone(ctx context.Context, req DeleteZoneRequestObject) (DeleteZoneResponseObject, error) {
	if err := h.d.Zones.DeleteZone(ctx, PrincipalFrom(ctx).Actor(), req.ZoneId, req.Params.Revision); err != nil {
		return nil, err
	}
	return DeleteZone204Response{}, nil
}

func (h *handlers) ListZoneRecords(ctx context.Context, req ListZoneRecordsRequestObject) (ListZoneRecordsResponseObject, error) {
	p := req.Params
	deref := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	limit := 200
	if p.Limit != nil {
		if *p.Limit < 1 || *p.Limit > 1000 {
			return nil, invalid("limit must be 1..1000")
		}
		limit = *p.Limit
	}
	recs, next, err := h.d.Zones.ListRecords(ctx, req.ZoneId, deref(p.Name), deref(p.Type), deref(p.Cursor), limit)
	if err != nil {
		return nil, err
	}
	out := ListZoneRecords200JSONResponse{Items: make([]Record, 0, len(recs))}
	for i := range recs {
		out.Items = append(out.Items, recordOut(&recs[i]))
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

func (h *handlers) CreateZoneRecord(ctx context.Context, req CreateZoneRecordRequestObject) (CreateZoneRecordResponseObject, error) {
	b := req.Body
	in, err := recordIn(b.Name, string(b.Type), b.Ttl, b.Data)
	if err != nil {
		return nil, err
	}
	r, err := h.d.Zones.CreateRecord(ctx, PrincipalFrom(ctx).Actor(), req.ZoneId, in)
	if err != nil {
		return nil, err
	}
	return CreateZoneRecord201JSONResponse(recordOut(r)), nil
}

func (h *handlers) UpdateZoneRecord(ctx context.Context, req UpdateZoneRecordRequestObject) (UpdateZoneRecordResponseObject, error) {
	b := req.Body
	in, err := recordIn(b.Name, string(b.Type), b.Ttl, b.Data)
	if err != nil {
		return nil, err
	}
	r, err := h.d.Zones.UpdateRecord(ctx, PrincipalFrom(ctx).Actor(), req.ZoneId, req.RecordId, b.Revision, in)
	if err != nil {
		return nil, err
	}
	return UpdateZoneRecord200JSONResponse(recordOut(r)), nil
}

func (h *handlers) DeleteZoneRecord(ctx context.Context, req DeleteZoneRecordRequestObject) (DeleteZoneRecordResponseObject, error) {
	if err := h.d.Zones.DeleteRecord(ctx, PrincipalFrom(ctx).Actor(), req.ZoneId, req.RecordId, req.Params.Revision); err != nil {
		return nil, err
	}
	return DeleteZoneRecord204Response{}, nil
}
