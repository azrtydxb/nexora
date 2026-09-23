package api

import (
	"context"
	"net/http"
)

// catalogZones returns the catalog zone service, or 501 while none is configured.
func (h *handlers) catalogZones() (CatalogZoneService, error) {
	if h.d.CatalogZones == nil {
		return nil, coded(http.StatusNotImplemented, "not_implemented", "catalog zones are not available")
	}
	return h.d.CatalogZones, nil
}

// ListCatalogZones lists the catalog zones.
func (h *handlers) ListCatalogZones(ctx context.Context, _ ListCatalogZonesRequestObject) (ListCatalogZonesResponseObject, error) {
	svc, err := h.catalogZones()
	if err != nil {
		return nil, err
	}
	list, err := svc.List(ctx)
	if err != nil {
		return nil, err
	}
	return ListCatalogZones200JSONResponse(list), nil
}

// CreateCatalogZone creates a producer or consumer catalog zone.
func (h *handlers) CreateCatalogZone(ctx context.Context, req CreateCatalogZoneRequestObject) (CreateCatalogZoneResponseObject, error) {
	svc, err := h.catalogZones()
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, invalid("request body is required")
	}
	cz, err := svc.Create(ctx, PrincipalFrom(ctx).Actor(), *req.Body)
	if err != nil {
		return nil, err
	}
	return CreateCatalogZone201JSONResponse(cz), nil
}

// GetCatalogZone returns one catalog zone with its members.
func (h *handlers) GetCatalogZone(ctx context.Context, req GetCatalogZoneRequestObject) (GetCatalogZoneResponseObject, error) {
	svc, err := h.catalogZones()
	if err != nil {
		return nil, err
	}
	cz, err := svc.Get(ctx, req.CatalogZoneId)
	if err != nil {
		return nil, err
	}
	return GetCatalogZone200JSONResponse(cz), nil
}

// DeleteCatalogZone deletes a catalog zone.
func (h *handlers) DeleteCatalogZone(ctx context.Context, req DeleteCatalogZoneRequestObject) (DeleteCatalogZoneResponseObject, error) {
	svc, err := h.catalogZones()
	if err != nil {
		return nil, err
	}
	if err := svc.Delete(ctx, PrincipalFrom(ctx).Actor(), req.CatalogZoneId); err != nil {
		return nil, err
	}
	return DeleteCatalogZone204Response{}, nil
}
