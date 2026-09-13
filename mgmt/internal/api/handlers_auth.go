package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

func validateUsername(u string) error {
	if !usernameRE.MatchString(u) {
		return invalid("username must be 1-64 characters of letters, digits and . _ @ -")
	}
	return nil
}

func apiUser(u auth.User) User {
	return User{Id: uuid.MustParse(u.ID), Username: u.Username, Email: u.Email, Role: Role(u.Role),
		Source: UserSource(u.Source), Disabled: u.Disabled, Revision: u.Revision, CreatedAt: u.CreatedAt}
}

// sessionResponse sets the session cookie before writing the user.
type sessionResponse struct {
	user   User
	status int
	cookie *http.Cookie
}

func (s sessionResponse) write(w http.ResponseWriter) error {
	http.SetCookie(w, s.cookie)
	if s.status == http.StatusCreated {
		return CompleteSetup201JSONResponse(s.user).VisitCompleteSetupResponse(w)
	}
	return Login200JSONResponse(s.user).VisitLoginResponse(w)
}

func (s sessionResponse) VisitCompleteSetupResponse(w http.ResponseWriter) error { return s.write(w) }
func (s sessionResponse) VisitLoginResponse(w http.ResponseWriter) error         { return s.write(w) }

// redirectResponse is a 302 that optionally sets a cookie.
type redirectResponse struct {
	location string
	cookie   *http.Cookie
}

func (s redirectResponse) write(w http.ResponseWriter) error {
	if s.cookie != nil {
		http.SetCookie(w, s.cookie)
	}
	w.Header().Set("Location", s.location)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusFound)
	return nil
}

func (s redirectResponse) VisitStartOidcLoginResponse(w http.ResponseWriter) error { return s.write(w) }
func (s redirectResponse) VisitOidcCallbackResponse(w http.ResponseWriter) error   { return s.write(w) }

// logoutResponse expires the session cookie.
type logoutResponse struct{ cookie *http.Cookie }

func (s logoutResponse) VisitLogoutResponse(w http.ResponseWriter) error {
	http.SetCookie(w, s.cookie)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *handlers) GetHealth(ctx context.Context, _ GetHealthRequestObject) (GetHealthResponseObject, error) {
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := h.d.Store.Pool.Ping(pctx); err != nil {
		return GetHealth503JSONResponse{Status: HealthStatusDegraded, Database: HealthDatabaseUnavailable, Version: Version}, nil
	}
	return GetHealth200JSONResponse{Status: HealthStatusOk, Database: HealthDatabaseOk, Version: Version}, nil
}

func (h *handlers) GetSetupStatus(ctx context.Context, _ GetSetupStatusRequestObject) (GetSetupStatusResponseObject, error) {
	required, err := h.d.Auth.SetupRequired(ctx)
	if err != nil {
		return nil, err
	}
	return GetSetupStatus200JSONResponse{Required: required}, nil
}

func (h *handlers) CompleteSetup(ctx context.Context, req CompleteSetupRequestObject) (CompleteSetupResponseObject, error) {
	if err := validateUsername(req.Body.Username); err != nil {
		return nil, err
	}
	u, err := h.d.Auth.CompleteSetup(ctx, req.Body.Token, req.Body.Username, req.Body.Email, req.Body.Password)
	if err != nil {
		return nil, err
	}
	token, err := h.d.Auth.CreateSession(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	return sessionResponse{user: apiUser(u), status: http.StatusCreated, cookie: h.d.Auth.SessionCookie(token)}, nil
}

func (h *handlers) Login(ctx context.Context, req LoginRequestObject) (LoginResponseObject, error) {
	token, u, err := h.d.Auth.Login(ctx, req.Body.Username, req.Body.Password)
	if err != nil {
		return nil, err
	}
	return sessionResponse{user: apiUser(u), status: http.StatusOK, cookie: h.d.Auth.SessionCookie(token)}, nil
}

func (h *handlers) Logout(ctx context.Context, _ LogoutRequestObject) (LogoutResponseObject, error) {
	if PrincipalFrom(ctx).Kind == "session" {
		if c, err := requestFrom(ctx).Cookie(auth.SessionCookieName); err == nil {
			if err := h.d.Auth.Logout(ctx, c.Value); err != nil {
				return nil, err
			}
		}
	}
	expired := h.d.Auth.SessionCookie("")
	expired.MaxAge = -1
	return logoutResponse{cookie: expired}, nil
}

func (h *handlers) GetCurrentUser(ctx context.Context, _ GetCurrentUserRequestObject) (GetCurrentUserResponseObject, error) {
	p := PrincipalFrom(ctx)
	u, err := auth.ScanUser(h.d.Store.Pool.QueryRow(ctx, "select "+auth.UserColumns+" from users where id = $1", p.UserID))
	if err != nil {
		return nil, store.MapError(err)
	}
	u.Role = p.Role // an API token may carry less than its owner
	return GetCurrentUser200JSONResponse(apiUser(u)), nil
}

func (h *handlers) ListAuthProviders(context.Context, ListAuthProvidersRequestObject) (ListAuthProvidersResponseObject, error) {
	return ListAuthProviders200JSONResponse{Local: true, Oidc: h.d.OIDC.Enabled()}, nil
}

func (h *handlers) StartOidcLogin(ctx context.Context, req StartOidcLoginRequestObject) (StartOidcLoginResponseObject, error) {
	returnTo := "/"
	if req.Params.ReturnTo != nil {
		returnTo = *req.Params.ReturnTo
	}
	location, err := h.d.OIDC.Start(ctx, returnTo)
	if err != nil {
		if errors.Is(err, auth.ErrOIDCUnavailable) {
			return StartOidcLogin503JSONResponse{ErrorJSONResponse{Code: "unavailable", Message: "identity provider unavailable"}}, nil
		}
		return nil, err
	}
	return redirectResponse{location: location}, nil
}

func (h *handlers) OidcCallback(ctx context.Context, req OidcCallbackRequestObject) (OidcCallbackResponseObject, error) {
	u, returnTo, err := h.d.OIDC.Callback(ctx, req.Params.State, req.Params.Code)
	if err != nil {
		return nil, err
	}
	token, err := h.d.Auth.CreateSession(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	return redirectResponse{location: auth.SafeReturnTo(returnTo), cookie: h.d.Auth.SessionCookie(token)}, nil
}
