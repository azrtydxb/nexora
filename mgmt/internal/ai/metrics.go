package ai

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// Model-call metrics, shared by every Service in the process (names fixed by the M11 spec).
var (
	Enabled = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nexora_mgmt_ai_enabled", Help: "1 when the AI layer is configured and on",
	})
	Requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nexora_mgmt_ai_requests_total", Help: "AI model requests by feature and outcome",
	}, []string{"feature", "outcome"})
	RequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "nexora_mgmt_ai_request_duration_seconds", Help: "AI model request duration by feature, excluding queue wait",
		Buckets: []float64{1, 2, 5, 10, 20, 40, 80, 160, 320},
	}, []string{"feature"})
	Tokens = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nexora_mgmt_ai_tokens_total", Help: "AI model tokens by feature and kind (input, output, reasoning)",
	}, []string{"feature", "kind"})
	ValidationRetries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nexora_mgmt_ai_validation_retries_total", Help: "AI model calls repeated because the answer was invalid",
	}, []string{"feature"})
	InflightRequests = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nexora_mgmt_ai_inflight_requests", Help: "AI model requests holding a concurrency slot",
	})
	QueueWait = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "nexora_mgmt_ai_queue_wait_seconds", Help: "Time AI requests waited for a rate token and a concurrency slot",
		Buckets: []float64{0.01, 0.1, 0.5, 1, 5, 30, 120, 600},
	})
)

func registerMetrics(reg prometheus.Registerer) error {
	for _, c := range []prometheus.Collector{Enabled, Requests, RequestDuration, Tokens, ValidationRetries, InflightRequests, QueueWait} {
		if err := reg.Register(c); err != nil {
			var already prometheus.AlreadyRegisteredError
			if !errors.As(err, &already) {
				return err
			}
		}
	}
	return nil
}

// outcome is the requests_total outcome label of a finished call.
func outcome(err error) string {
	switch code := Code(err); {
	case err == nil:
		return "ok"
	case code == "busy":
		return "rate_limited"
	case code != "":
		return code
	default:
		return "provider_error"
	}
}
