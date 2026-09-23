package e2e

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/piwi3910/nexora/e2e/harness"
)

const mgmtHARecoveryBudget = 10 * time.Second

// Bind every request, including response body reads, to the same end-to-end deadline.
// Copy the clients so the setup/cleanup client retains its usual timeout and transport.
func acceptanceAPI(ctx context.Context, api *harness.API) *harness.API {
	copyAPI := *api
	client := *api.HC
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = acceptanceTransport{ctx, transport}
	copyAPI.HC = &client
	return &copyAPI
}

type acceptanceTransport struct {
	ctx  context.Context
	base http.RoundTripper
}

func (t acceptanceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.ctx.Err(); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req.Clone(t.ctx))
}

// Success returned by a slow observation is not success within the deadline.
func acceptancePoll(ctx context.Context, interval time.Duration, observe func() error) error {
	var last error
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("acceptance deadline: %w (last observation: %v)", err, last)
		}
		last = observe()
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("acceptance deadline: %w (last observation: %v)", err, last)
		}
		if last == nil {
			return nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

// Process teardown can block too; it consumes the same recovery budget.
func acceptanceDisrupt(ctx context.Context, disrupt func()) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() { disrupt(); close(done) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return ctx.Err()
	}
}
