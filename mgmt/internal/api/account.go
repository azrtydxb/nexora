package api

import (
	"context"
	"errors"
	"net/http"
	"net/netip"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
)

// TrustedProxies are explicitly configured reverse proxies whose
// X-Forwarded-For header is believed; set from NEXORA_TRUSTED_PROXY_CIDRS.
var TrustedProxies []netip.Prefix

func clientAddr(r *http.Request) string {
	return auth.ClientAddr(r, TrustedProxies)
}

// accountError maps the self-service errors that only these handlers produce.
func accountError(err error) error {
	var verr auth.ValidationError
	switch {
	case errors.As(err, &verr):
		return invalid("%s", verr.Error())
	case errors.Is(err, auth.ErrInvalidCurrentPassword):
		return coded(http.StatusForbidden, "invalid_current_password", "%s", auth.ErrInvalidCurrentPassword)
	case errors.Is(err, auth.ErrManagedByIdentityProvider):
		return coded(http.StatusConflict, "managed_by_identity_provider", "%s", auth.ErrManagedByIdentityProvider)
	}
	return err
}

func (h *handlers) UpdateCurrentUser(ctx context.Context, req UpdateCurrentUserRequestObject) (UpdateCurrentUserResponseObject, error) {
	// Only these keys are read: role, disabled or username in the body never reach the service.
	in := auth.ProfileUpdate{Revision: req.Body.Revision, Email: req.Body.Email, DisplayName: req.Body.DisplayName}
	if p := req.Body.Preferences; p != nil {
		in.Preferences = &auth.Preferences{Theme: string(p.Theme), TimeZone: p.TimeZone, Clock24h: p.Clock24h, QuerylogLive: p.QuerylogLive}
	}
	p := PrincipalFrom(ctx)
	u, err := h.d.Auth.UpdateProfile(ctx, p, in)
	if err != nil {
		return nil, accountError(err)
	}
	u.Role = p.Role // an API token may carry less than its owner
	return UpdateCurrentUser200JSONResponse(apiUser(u)), nil
}

func (h *handlers) ChangeOwnPassword(ctx context.Context, req ChangeOwnPasswordRequestObject) (ChangeOwnPasswordResponseObject, error) {
	r := requestFrom(ctx)
	var session string
	if c, err := r.Cookie(auth.SessionCookieName); err == nil {
		session = c.Value
	}
	revokeOthers := req.Body.RevokeOtherSessions == nil || *req.Body.RevokeOtherSessions
	err := h.d.Auth.ChangePassword(ctx, PrincipalFrom(ctx), clientAddr(r), session,
		req.Body.CurrentPassword, req.Body.NewPassword, revokeOthers)
	if err != nil {
		return nil, accountError(err)
	}
	return ChangeOwnPassword204Response{}, nil
}
