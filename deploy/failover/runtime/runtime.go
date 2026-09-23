// Package runtime coordinates detached, explicitly enabled lab candidates only.
// It exposes no serving activation or withdrawal/eligibility proof.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/piwi3910/nexora/deploy/failover/lease"
)

// Config deliberately has no production/activation switch or boolean proofs.
// Stage uses fresh unreachable namespaces, independently of the leased gate.
// The gate namespace and loopback API server must be externally provisioned.
type Config struct {
	Mode      string
	Driver    lease.DriverOptions
	Authority lease.HTTPSConfig
	Stage     StageOptions
	Margin    time.Duration
	Interval  time.Duration
	Duration  time.Duration
}

func (c Config) validate() error {
	if c.Mode != "detached-lab-v1" || !c.Driver.Enabled || c.Driver.LabBackendInterface != "" || c.Driver.LabBackendAlias != "" {
		return errors.New("only explicitly enabled detached-lab-v1 is supported; active DR provisioning, all-path fencing and clock/drain proof are missing")
	}
	if c.Driver.IOTimeout <= 0 || c.Driver.IOTimeout > 5*time.Second || c.Driver.ReapTimeout <= 0 || c.Driver.ReapTimeout > 5*time.Second || c.Margin <= 0 || c.Margin > time.Second || c.Interval < 10*time.Millisecond || c.Interval > time.Second || c.Duration < lease.MaxKernelWindow+c.Margin+c.Interval || c.Duration > 2*time.Minute {
		return errors.New("invalid bounded lab schedule")
	}
	return nil
}

// Report contains historical lab acknowledgements, never health or inactivity.
type Report struct {
	State       string
	Steps, Arms int
}

type stageSession interface{ Close(context.Context) error }
type controller interface {
	Step(context.Context) (lease.Result, error)
}
type closer interface{ Close(context.Context) error }

// Execute uses the real HTTPS authority, driver, exact paired BootClock and
// trusted detached stage subprocess. No injected Gate or proof flags are public.
func Execute(ctx context.Context, cfg Config) (report Report, err error) {
	report.State = "refused"
	if err = cfg.validate(); err != nil {
		return
	}
	if err = labInventory(ctx, cfg); err != nil {
		return
	}
	authority, err := lease.NewHTTPS(cfg.Authority)
	if err != nil {
		return report, err
	}
	stage, err := launchStage(ctx, cfg.Stage)
	if err != nil {
		return Report{State: "failed"}, err
	}
	// Even a failed driver startup must clean the independently fenced stage.
	gate, clock, err := lease.LaunchDriver(ctx, cfg.Driver)
	if err != nil {
		clean, cancel := context.WithTimeout(context.Background(), cfg.Stage.Timeout)
		defer cancel()
		return Report{State: "failed"}, errors.Join(err, stage.Close(clean))
	}
	holder, err := lease.NewHolderID()
	if err != nil {
		return Report{State: "failed"}, shutdown(stage, gate, cfg.Stage.Timeout, err)
	}
	ctl, err := lease.New(authority, gate, clock, lease.Config{Holder: holder, Margin: cfg.Margin, IOTimeout: cfg.Driver.IOTimeout})
	if err != nil {
		return Report{State: "failed"}, shutdown(stage, gate, cfg.Stage.Timeout, err)
	}
	return run(ctx, cfg, ctl, clock, stage, gate)
}

func shutdown(stage stageSession, gate closer, timeout time.Duration, cause error) error {
	// These operations have separate budgets; a failed/expired DENY must not
	// suppress cleanup of the independently unreachable stage namespaces.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	deny := gate.Close(ctx)
	cancel()
	ctx, cancel = context.WithTimeout(context.Background(), timeout)
	clean := stage.Close(ctx)
	cancel()
	return errors.Join(cause, deny, clean)
}

func run(ctx context.Context, cfg Config, ctl controller, clock lease.Clock, stage stageSession, gate closer) (r Report, err error) {
	r.State = "quarantined"
	defer func() {
		err = shutdown(stage, gate, cfg.Stage.Timeout, err)
		if err != nil {
			r.State = "failed"
		} else {
			r.State = "lab-stopped"
		}
	}()
	duration := time.NewTimer(cfg.Duration)
	defer duration.Stop()
	tick := time.NewTicker(cfg.Interval)
	defer tick.Stop()
	end := time.Now().Add(cfg.Duration)
	var deadline uint64
	for {
		if ctx.Err() != nil {
			return r, ctx.Err()
		}
		if deadline != 0 {
			now := clock.Now()
			if now == 0 || now >= deadline {
				return r, errors.New("previous ticket expired; restart/reacquisition refused")
			}
		}
		if !time.Now().Before(end) {
			if r.Arms == 0 {
				return r, errors.New("lab ended without acknowledged acquisition")
			}
			return r, nil
		}
		stepCtx, cancelStep := context.WithDeadline(ctx, end)
		result, stepErr := ctl.Step(stepCtx)
		if stepErr == nil {
			stepErr = stepCtx.Err()
		}
		cancelStep()
		r.Steps++
		if stepErr != nil {
			return r, fmt.Errorf("ownership step: %w", stepErr)
		}
		if result.TicketArmed != result.CASAcknowledged {
			return r, errors.New("incomplete ownership acknowledgement")
		}
		if result.TicketArmed {
			now := clock.Now()
			if result.Ticket.Captured == 0 || now < result.Ticket.Captured || now >= result.Ticket.Deadline {
				return r, errors.New("stale ARM acknowledgement")
			}
			deadline = result.Ticket.Deadline
			r.Arms++
			r.State = "lab-ticket-acknowledged"
		} else if deadline != 0 {
			// A normal nil-error quarantine result after ARM means ownership loss.
			return r, errors.New("ownership lost; no automatic reacquisition")
		}
		select {
		case <-ctx.Done():
			return r, ctx.Err()
		case <-duration.C:
			if r.Arms == 0 {
				return r, errors.New("lab ended without acknowledged acquisition")
			}
			return r, nil
		case <-tick.C:
		}
	}
}
