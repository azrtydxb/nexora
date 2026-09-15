// Package ai is the management plane AI service (M11): the model provider, structured generation with
// validation and retries, the shared limits and token budget, the privacy guard and the metrics.
// Every model call of every feature goes through Generate.
package ai

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"

	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Feature is the feature label of a model call (an agent name, querylog_search, config_assistant or
// threat_check).
type Feature string

// Priority decides how long a call waits for a slot and which share of the budget it may use.
type Priority int

const (
	// Interactive calls wait at most SlotWait and may use the whole daily budget.
	Interactive Priority = iota
	// Background calls wait until their context ends and stop at the background budget share.
	Background
)

var (
	ErrDisabled           = errors.New("ai disabled")
	ErrBusy               = errors.New("ai busy")
	ErrBudgetExhausted    = errors.New("ai token budget exhausted")
	ErrInvalidOutput      = errors.New("ai output invalid")
	ErrTimeout            = errors.New("ai call timed out")
	ErrProvider           = errors.New("ai provider error")
	ErrEndpointNotPrivate = errors.New("ai endpoint is not on a private network")
)

// Code maps an error to its task error code, or "" when it has none.
func Code(err error) string {
	for _, c := range []struct {
		err  error
		code string
	}{
		{ErrInvalidOutput, "invalid_output"}, {ErrTimeout, "timeout"}, {ErrProvider, "provider_error"},
		{ErrBudgetExhausted, "budget_exhausted"}, {ErrEndpointNotPrivate, "endpoint_not_private"}, {ErrBusy, "busy"},
	} {
		if errors.Is(err, c.err) {
			return c.code
		}
	}
	return ""
}

// DefaultSlotWait is how long an interactive call waits for a rate token and a concurrency slot.
const DefaultSlotWait = 5 * time.Second

// Options configures New.
type Options struct {
	Config     config.AIConfig
	Store      *store.Store
	Registerer prometheus.Registerer  // nil: metrics are not registered
	Model      provider.LanguageModel // nil: NewModel(Config)
	Resolve    Resolver               // nil: net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	Now        func() time.Time
	SlotWait   time.Duration // 0: DefaultSlotWait
}

// Service is one instance's AI service. Its limits are shared by every feature on the instance; the
// budget is shared by every instance on the database.
type Service struct {
	cfg      config.AIConfig
	st       *store.Store
	model    provider.LanguageModel
	resolve  Resolver
	now      func() time.Time
	slotWait time.Duration
	sem      chan struct{}
	bucket   *tokenBucket
}

// New returns (nil, reason, nil) when AI is off; reason is DisabledReason or "endpoint_not_private".
// A base URL host that does not resolve yet leaves AI on; its calls fail with provider_error.
func New(ctx context.Context, o Options) (*Service, string, error) {
	if o.Registerer != nil {
		if err := registerMetrics(o.Registerer); err != nil {
			return nil, "", err
		}
	}
	if reason := o.Config.DisabledReason(); reason != "" {
		return nil, reason, nil
	}
	if o.Store == nil {
		return nil, "", errors.New("ai: store is required")
	}
	s := &Service{cfg: o.Config, st: o.Store, model: o.Model, resolve: o.Resolve, now: o.Now, slotWait: o.SlotWait,
		sem: make(chan struct{}, o.Config.MaxConcurrency)}
	if s.resolve == nil {
		s.resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.slotWait == 0 {
		s.slotWait = DefaultSlotWait
	}
	if err := CheckEndpoint(ctx, s.cfg.BaseURL, s.cfg.AllowPublicEndpoint, s.resolve); errors.Is(err, ErrEndpointNotPrivate) {
		return nil, "endpoint_not_private", nil
	}
	if s.model == nil {
		s.model = NewModel(s.cfg)
	}
	if s.cfg.StructuredOutput == "prompt" {
		s.model = promptSchemaModel{s.model}
	}
	s.bucket = newTokenBucket(s.cfg.RequestsPerMinute, s.now())
	return s, "", nil
}

// Config returns the service configuration. Callers must never expose Config().APIKey.
func (s *Service) Config() config.AIConfig { return s.cfg }
