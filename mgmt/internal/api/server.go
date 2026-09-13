package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/pki"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/webui"
)

// Version is the build version reported by getHealth (set with -ldflags -X).
var Version = "dev"

// maxBodyBytes bounds every API request body.
const maxBodyBytes = 4 << 20

// Deps are the collaborators of the HTTP API.
type Deps struct {
	Store             *store.Store
	Auth              *auth.Service
	OIDC              *auth.OIDC
	CA                *pki.CA
	Build             snapshot.BuildConfig
	QueryLog          querylog.Backend
	InstanceID        string
	PublicURL         string
	Metrics           http.Handler
	RefreshFilterList func(ctx context.Context, p auth.Principal, id string) error
}

type handlers struct{ d Deps }

var _ StrictServerInterface = (*handlers)(nil)

// NewHandler routes /api/v1 to the API, /metrics to d.Metrics (when set) and everything else to
// the embedded GUI.
func NewHandler(d Deps) http.Handler {
	h := &handlers{d: d}
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer)
	strict := NewStrictHandlerWithOptions(h, []StrictMiddlewareFunc{h.authz}, StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		},
		ResponseErrorHandlerFunc: mapError,
	})
	r.Group(func(g chi.Router) {
		g.Use(jsonOnly)
		HandlerWithOptions(strict, ChiServerOptions{
			BaseURL:    "/api/v1",
			BaseRouter: g,
			ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
				writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			},
		})
	})
	r.HandleFunc("/api/*", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such API route")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
	})
	if d.Metrics != nil {
		r.Handle("/metrics", d.Metrics)
	}
	r.Handle("/*", webui.Handler())
	return r
}

// jsonOnly bounds request bodies and rejects mutating requests that are not
// Content-Type: application/json. Browsers cannot send that type cross-site without a CORS
// preflight, which together with SameSite=Lax cookies is the CSRF defence.
func jsonOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mt != "application/json" {
				writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "requests must send Content-Type: application/json")
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

type (
	principalKey struct{}
	requestKey   struct{}
)

// PrincipalFrom returns the authenticated caller stored by the authorization middleware.
func PrincipalFrom(ctx context.Context) auth.Principal {
	p, _ := ctx.Value(principalKey{}).(auth.Principal)
	return p
}

func requestFrom(ctx context.Context) *http.Request {
	r, _ := ctx.Value(requestKey{}).(*http.Request)
	return r
}

// authz authenticates and authorizes every non-public operation. The generated code passes the
// Go method name, whose first letter is upper-cased from the OpenAPI operationId.
func (h *handlers) authz(f StrictHandlerFunc, operation string) StrictHandlerFunc {
	operationID := strings.ToLower(operation[:1]) + operation[1:]
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
		ctx = context.WithValue(ctx, requestKey{}, r)
		if auth.Public[operationID] {
			return f(ctx, w, r, request)
		}
		p, err := h.d.Auth.Authenticate(ctx, r)
		if err != nil {
			return nil, err
		}
		if err := auth.Authorize(p, operationID); err != nil {
			return nil, err
		}
		return f(context.WithValue(ctx, principalKey{}, p), w, r, request)
	}
}

// validationError is a request the handlers reject as invalid.
type validationError struct{ msg string }

func (e validationError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return validationError{msg: fmt.Sprintf(format, args...)}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Error{Code: code, Message: message})
}

// mapError turns handler errors into JSON error responses.
func mapError(w http.ResponseWriter, r *http.Request, err error) {
	var verr validationError
	var pgErr *pgconn.PgError
	var maxBytes *http.MaxBytesError
	switch {
	case errors.As(err, &verr):
		writeError(w, http.StatusBadRequest, "invalid_request", verr.msg)
	case errors.Is(err, auth.ErrWeakPassword):
		writeError(w, http.StatusBadRequest, "invalid_request", auth.ErrWeakPassword.Error())
	case errors.As(err, &maxBytes):
		writeError(w, http.StatusBadRequest, "invalid_request", "request body too large")
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "unauthenticated", auth.ErrInvalidCredentials.Error())
	case errors.Is(err, auth.ErrUnauthenticated):
		writeError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
	case errors.Is(err, auth.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, auth.ErrSetupDone):
		writeError(w, http.StatusConflict, "conflict", auth.ErrSetupDone.Error())
	case errors.Is(err, store.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "unavailable", "database unavailable")
	case errors.Is(err, auth.ErrOIDCUnavailable):
		writeError(w, http.StatusServiceUnavailable, "unavailable", "identity provider unavailable")
	case errors.Is(err, querylog.ErrBackendUnavailable):
		writeError(w, http.StatusServiceUnavailable, "querylog_unavailable", err.Error())
	case errors.As(err, &pgErr) && (strings.HasPrefix(pgErr.Code, "22") || pgErr.Code == "23514" || pgErr.Code == "23502"):
		// Data exceptions and check/not-null violations: the database rejected the input.
		writeError(w, http.StatusBadRequest, "invalid_request", pgErr.Message)
	default:
		slog.Error("http api", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}
