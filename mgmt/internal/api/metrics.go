package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics records HTTP API request counts and durations per OpenAPI operation.
type Metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewMetrics registers the HTTP API metrics on reg.
func NewMetrics(reg *prometheus.Registry) *Metrics {
	m := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nexora_mgmt_http_requests_total", Help: "HTTP API requests by operation and status code",
		}, []string{"operation", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "nexora_mgmt_http_request_duration_seconds", Help: "HTTP API request duration by operation",
			Buckets: prometheus.DefBuckets,
		}, []string{"operation"}),
	}
	reg.MustRegister(m.requests, m.duration)
	return m
}

type operationKey struct{}

// Middleware names the operation of the request for wrap. It must be the outermost strict
// middleware so that requests rejected by authorization are counted too.
func (m *Metrics) Middleware() StrictMiddlewareFunc {
	return func(f StrictHandlerFunc, operation string) StrictHandlerFunc {
		operationID := strings.ToLower(operation[:1]) + operation[1:]
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
			if op, ok := r.Context().Value(operationKey{}).(*string); ok {
				*op = operationID
			}
			return f(ctx, w, r, request)
		}
	}
}

// wrap observes each request that reached an operation, once the response status is known (the
// strict handler writes it after the middlewares return). Unmatched routes are not recorded, which
// keeps the label set bounded.
func (m *Metrics) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var op string
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), operationKey{}, &op)))
		if op == "" {
			return
		}
		m.requests.WithLabelValues(op, strconv.Itoa(sw.code)).Inc()
		m.duration.WithLabelValues(op).Observe(time.Since(start).Seconds())
	})
}

type statusWriter struct {
	http.ResponseWriter
	code        int
	wroteHeader bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wroteHeader {
		s.code, s.wroteHeader = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	s.wroteHeader = true
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
