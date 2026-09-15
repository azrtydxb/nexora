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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/proposal"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/catalog"
	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/control"
	"github.com/piwi3910/nexora/mgmt/internal/dnssec"
	"github.com/piwi3910/nexora/mgmt/internal/pki"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/tsigkey"
	"github.com/piwi3910/nexora/mgmt/internal/webui"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

// Version is the build version reported by getHealth (set by nexora-mgmt from its stamped version).
var Version = "dev"

// maxBodyBytes bounds every API request body except zone file imports (maxZoneImportBytes).
const (
	maxBodyBytes       = 4 << 20
	maxZoneImportBytes = 64 << 20
)

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
	HTTPMetrics       *Metrics // optional per-operation request metrics
	RefreshFilterList func(ctx context.Context, p auth.Principal, id string) error
	DNSTLS            *control.DNSTLSFanout // optional: the DNS serving certificate this instance pushes
	Secrets           *secrets.Box          // key storage for RPZ TSIG secrets; nil behaves as unconfigured
	Zones             *zone.Service         // hosted zones and records
	TSIGKeys          *tsigkey.Service      // TSIG keys of hosted zones
	ZoneDNSSEC        *dnssec.Service       // DNSSEC signing settings, keys and rollovers of hosted zones
	Catalog           *catalog.Catalog      // the embedded filter category catalog; nil serves an empty catalog
	EngineLogs        EngineLogReader       // nil: getEngineLogs answers 501 engine_unsupported
	AIDisabledReason  string                // "" when AI is on; getAiStatus reports it
	AI                *AIRuntime            // nil while AI is off: AI operations answer 503 ai_disabled
	MCP               http.Handler          // nil: no /mcp endpoint
}

// AIRuntime holds the AI collaborators of the handlers while AI is on.
type AIRuntime struct {
	Service       *ai.Service
	Tasks         *ai.Tasks
	Proposals     *proposal.Validator
	Config        config.AIConfig
	InstanceStart time.Time
	TaskKinds     map[ai.TaskKind]bool // registered task kinds; a missing kind answers 503 feature_disabled
	Agents        map[string]bool      // registered agents; one missing is reported disabled and cannot run
}

type handlers struct {
	d    Deps
	root http.Handler // the full router, for in-process replay of API operations
}

// aiRuntime gates every AI operation except getAiStatus: 503 ai_disabled while AI is off.
func (h *handlers) aiRuntime() (*AIRuntime, error) {
	if h.d.AI != nil {
		return h.d.AI, nil
	}
	return nil, apiError{status: http.StatusServiceUnavailable, code: "ai_disabled", msg: "AI is not configured: " + h.d.AIDisabledReason}
}

var _ StrictServerInterface = (*handlers)(nil)

// NewHandler routes /api/v1 to the API, /metrics to d.Metrics (when set) and everything else to
// the embedded GUI.
func NewHandler(d Deps) http.Handler {
	_, r := newHandlers(d)
	return r
}

// newHandlers is NewHandler that also returns the handlers, for in-package tests.
func newHandlers(d Deps) (*handlers, http.Handler) {
	if d.Catalog == nil {
		d.Catalog = &catalog.Catalog{}
	}
	h := &handlers{d: d}
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer)
	middlewares := []StrictMiddlewareFunc{h.authz}
	if d.HTTPMetrics != nil {
		middlewares = append(middlewares, d.HTTPMetrics.Middleware()) // the last is outermost
	}
	strict := NewStrictHandlerWithOptions(h, middlewares, StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		},
		ResponseErrorHandlerFunc: mapError,
	})
	r.Group(func(g chi.Router) {
		if d.HTTPMetrics != nil {
			g.Use(d.HTTPMetrics.wrap)
		}
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
	if d.MCP != nil {
		r.Handle("/mcp", d.MCP)
	}
	r.Handle("/*", webui.Handler())
	h.root = r
	return h, r
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
		limit := int64(maxBodyBytes)
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/zones/") && strings.HasSuffix(r.URL.Path, "/import") {
			limit = maxZoneImportBytes
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
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
	writeErrorBody(w, status, Error{Code: code, Message: message})
}

func writeErrorBody(w http.ResponseWriter, status int, body Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeZoneValidation answers 422 with the validation code and its per-line details.
func writeZoneValidation(w http.ResponseWriter, zve *zone.ValidationError) {
	body := Error{Code: zve.Code, Message: zve.Message}
	if len(zve.Details) > 0 {
		details := make([]struct {
			Line    int    `json:"line"`
			Message string `json:"message"`
		}, len(zve.Details))
		for i, d := range zve.Details {
			details[i].Line, details[i].Message = d.Line, d.Message
		}
		body.Details = &details
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_ = json.NewEncoder(w).Encode(body)
}

// mapError turns handler errors into JSON error responses.
func mapError(w http.ResponseWriter, r *http.Request, err error) {
	var aerr apiError
	var verr validationError
	var pgErr *pgconn.PgError
	var maxBytes *http.MaxBytesError
	var zve *zone.ValidationError
	switch {
	case errors.As(err, &aerr):
		body := Error{Code: aerr.code, Message: aerr.msg}
		if len(aerr.details) > 0 {
			details := make([]struct {
				Line    int    `json:"line"`
				Message string `json:"message"`
			}, len(aerr.details))
			for i, d := range aerr.details {
				details[i].Message = d
			}
			body.Details = &details
		}
		writeErrorBody(w, aerr.status, body)
	case errors.As(err, &verr):
		writeError(w, http.StatusBadRequest, "invalid_request", verr.msg)
	case errors.Is(err, auth.ErrWeakPassword):
		writeError(w, http.StatusBadRequest, "invalid_request", auth.ErrWeakPassword.Error())
	case errors.As(err, &maxBytes):
		writeError(w, http.StatusBadRequest, "invalid_request", "request body too large")
	case errors.Is(err, auth.ErrTooManyAttempts):
		writeError(w, http.StatusTooManyRequests, "too_many_attempts", auth.ErrTooManyAttempts.Error())
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "unauthenticated", auth.ErrInvalidCredentials.Error())
	case errors.Is(err, auth.ErrUnauthenticated):
		writeError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
	case errors.Is(err, auth.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", err.Error())
	case errors.As(err, &zve):
		writeZoneValidation(w, zve)
	case errors.Is(err, zone.ErrReadOnly):
		writeError(w, http.StatusUnprocessableEntity, "zone_read_only", zone.ErrReadOnly.Error())
	case errors.Is(err, tsigkey.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, tsigkey.ErrInUse):
		writeError(w, http.StatusConflict, "tsig_key_in_use", "the TSIG key is referenced by a zone")
	case errors.Is(err, dnssec.ErrAlgorithmRollover):
		writeError(w, http.StatusUnprocessableEntity, "algorithm_rollover_unsupported", dnssec.ErrAlgorithmRollover.Error())
	case errors.Is(err, dnssec.ErrRolloverInProgress):
		writeError(w, http.StatusConflict, "rollover_in_progress", dnssec.ErrRolloverInProgress.Error())
	case errors.Is(err, dnssec.ErrNotPendingKSK):
		writeError(w, http.StatusUnprocessableEntity, "ksk_not_pending", dnssec.ErrNotPendingKSK.Error())
	case errors.Is(err, dnssec.ErrNotEnabled):
		writeError(w, http.StatusUnprocessableEntity, "dnssec_disabled", dnssec.ErrNotEnabled.Error())
	case errors.Is(err, dnssec.ErrNotPrimary):
		writeError(w, http.StatusUnprocessableEntity, "zone_not_primary", dnssec.ErrNotPrimary.Error())
	case errors.Is(err, dnssec.ErrInvalidSettings):
		writeError(w, http.StatusUnprocessableEntity, "invalid_dnssec_settings", err.Error())
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
		slog.Warn("query log search", "err", err) // the cause names internal endpoints: log it, answer generically
		writeError(w, http.StatusServiceUnavailable, "querylog_unavailable", querylog.ErrBackendUnavailable.Error())
	case errors.Is(err, querylog.ErrInvalidCursor):
		writeError(w, http.StatusBadRequest, "invalid_request", querylog.ErrInvalidCursor.Error())
	case errors.Is(err, secrets.ErrUnconfigured):
		writeError(w, http.StatusServiceUnavailable, "key_storage_unconfigured", "Key storage is not configured on the management plane (NEXORA_KEK_FILE)")
	case errors.Is(err, secrets.ErrBackendUnavailable):
		slog.Warn("key storage", "err", err) // never carries key material
		writeError(w, http.StatusServiceUnavailable, "key_backend_unavailable", "The requested key storage backend is not configured on this management plane")
	case errors.Is(err, ai.ErrBusy):
		writeError(w, http.StatusTooManyRequests, "ai_busy", "the AI model is busy; retry shortly")
	case errors.Is(err, ai.ErrBudgetExhausted):
		writeError(w, http.StatusTooManyRequests, "ai_budget_exhausted", "the daily AI token budget is exhausted")
	case errors.Is(err, store.ErrLastRootAnchor):
		writeError(w, http.StatusConflict, "last_root_anchor", store.ErrLastRootAnchor.Error())
	case errors.As(err, &pgErr) && (strings.HasPrefix(pgErr.Code, "22") || pgErr.Code == "23514" || pgErr.Code == "23502"):
		// Data exceptions and check/not-null violations: the database rejected the input.
		writeError(w, http.StatusBadRequest, "invalid_request", pgErr.Message)
	default:
		slog.Error("http api", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}
