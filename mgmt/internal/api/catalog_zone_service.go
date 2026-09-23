package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/catzone"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

// catalogZoneService adapts package catzone to CatalogZoneService.
type catalogZoneService struct{ s *catzone.Service }

// NewCatalogZoneService returns the catalog zone backend over s.
func NewCatalogZoneService(s *catzone.Service) CatalogZoneService {
	return &catalogZoneService{s: s}
}

func (c *catalogZoneService) List(ctx context.Context) ([]CatalogZone, error) {
	views, err := c.s.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]CatalogZone, 0, len(views))
	for _, v := range views {
		out = append(out, catalogZoneOut(v))
	}
	return out, nil
}

func (c *catalogZoneService) Get(ctx context.Context, id uuid.UUID) (CatalogZone, error) {
	v, err := c.s.Get(ctx, id)
	if err != nil {
		return CatalogZone{}, err
	}
	return catalogZoneOut(v), nil
}

func (c *catalogZoneService) Create(ctx context.Context, actor auth.Actor, in CatalogZoneCreate) (CatalogZone, error) {
	cin := catzone.CreateInput{Name: in.Name, Role: string(in.Role), EngineGroupID: in.EngineGroupId, Transfer: transferIn(in.Transfer)}
	if in.Primaries != nil {
		cin.Primaries = zoneEndpointsIn(*in.Primaries)
	}
	if in.Notify != nil {
		cin.Notify = zoneEndpointsIn(*in.Notify)
	}
	v, err := c.s.Create(ctx, actor, cin)
	if err != nil {
		return CatalogZone{}, catalogZoneErr(err)
	}
	return catalogZoneOut(v), nil
}

func (c *catalogZoneService) Delete(ctx context.Context, actor auth.Actor, id uuid.UUID) error {
	return catalogZoneErr(c.s.Delete(ctx, actor, id))
}

// catalogZoneErr answers the catalog and zone validation errors with 400 and their code; a
// duplicate zone name already maps to 409 conflict, as it does for zones.
func catalogZoneErr(err error) error {
	var zve *zone.ValidationError
	if errors.As(err, &zve) {
		return coded(http.StatusBadRequest, zve.Code, "%s", zve.Message)
	}
	return zoneErr(err)
}

func catalogZoneOut(v catzone.View) CatalogZone {
	out := CatalogZone{Id: v.ID, ZoneId: v.ZoneID, Name: v.Name, Role: CatalogZoneRole(v.Role), EngineGroupId: v.EngineGroupID,
		BrokenReason: v.BrokenReason, ProcessedSerial: v.ProcessedSerial, ProcessedAt: v.ProcessedAt, CreatedAt: v.CreatedAt,
		Members: make([]CatalogMember, 0, len(v.Members))}
	for _, m := range v.Members {
		out.Members = append(out.Members, CatalogMember{ZoneId: m.ZoneID, Name: m.Name, Label: m.Label,
			State: CatalogMemberState(m.State), Issue: m.Issue})
	}
	return out
}
