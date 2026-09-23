package api

import (
	"context"
	"net/http"
)

// odoh returns the Oblivious DoH service, or 501 while none is configured.
func (h *handlers) odoh() (ODoHService, error) {
	if h.d.ODoH == nil {
		return nil, coded(http.StatusNotImplemented, "not_implemented", "Oblivious DoH is not available")
	}
	return h.d.ODoH, nil
}

// GetOdohSettings returns the Oblivious DoH settings and key metadata.
func (h *handlers) GetOdohSettings(ctx context.Context, _ GetOdohSettingsRequestObject) (GetOdohSettingsResponseObject, error) {
	svc, err := h.odoh()
	if err != nil {
		return nil, err
	}
	out, err := svc.Get(ctx)
	if err != nil {
		return nil, err
	}
	return GetOdohSettings200JSONResponse(out), nil
}

// UpdateOdohSettings replaces the Oblivious DoH settings.
func (h *handlers) UpdateOdohSettings(ctx context.Context, req UpdateOdohSettingsRequestObject) (UpdateOdohSettingsResponseObject, error) {
	svc, err := h.odoh()
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, invalid("request body is required")
	}
	out, err := svc.Update(ctx, PrincipalFrom(ctx).Actor(), *req.Body)
	if err != nil {
		return nil, err
	}
	return UpdateOdohSettings200JSONResponse(out), nil
}

// RotateOdohKey creates a new Oblivious DoH key now.
func (h *handlers) RotateOdohKey(ctx context.Context, _ RotateOdohKeyRequestObject) (RotateOdohKeyResponseObject, error) {
	svc, err := h.odoh()
	if err != nil {
		return nil, err
	}
	out, err := svc.Rotate(ctx, PrincipalFrom(ctx).Actor())
	if err != nil {
		return nil, err
	}
	return RotateOdohKey200JSONResponse(out), nil
}
