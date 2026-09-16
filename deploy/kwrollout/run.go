// Package kwrollout implements the kw DNS-pair sequencing core.
// Runtime rollout adapters and integration into kw-deploy.sh are not yet implemented.
package kwrollout

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Member identifies one independently upgradable, node-pinned engine workload.
type Member struct {
	Workload string
	Node     string
}

// Pair is one DNS VIP's two disjoint serving members, primary first for migration.
type Pair struct {
	Name    string
	Address string
	Members [2]Member
}

// Stage changes the chart's single permitted rolling workload and VIP selectors.
type Stage struct {
	Name            string
	RollingWorkload string
	LegacySelectors bool
}

// Gate names the members whose runtime health and desired templates must be verified.
// Runtime health includes unique identities, fresh management connection, current
// configuration, direct DNS, and both client VIPs. Ready alone is insufficient.
type Gate struct {
	Name          string
	Required      []string
	Desired       []string
	PairSelectors bool
}

// Function boundaries keep orchestration tests independent of external commands.
// A failure deliberately retains the deployment lock, including cancellation: a
// remote Helm request may still complete after its local process was interrupted.
type steps struct {
	lock      func(context.Context) error
	unlock    func(context.Context) error
	ownership func(context.Context) error
	inspect   func(context.Context) (bool, error)
	// checkDNS performs one complete configured VIP probe round without retries.
	// It must honor cancellation; run joins it before returning or unlocking.
	checkDNS        func(context.Context) error
	monitorInterval time.Duration
	// enroll labels existing pods without restarting them, using UID/resourceVersion
	// preconditions, and verifies all pair-labelled endpoints before selector cutover.
	enroll func(context.Context, []string) error
	health func(context.Context, Gate) error
	apply  func(context.Context, Stage) error
	// Supporting resources and bootstrap are part of the same retained lock and
	// DNS monitor lifetime, not unguarded work before/after the rollout.
	prepare  func(context.Context) error
	finalize func(context.Context) error
}

func run(ctx context.Context, pairs []Pair, s steps) error {
	if err := validatePairs(pairs); err != nil {
		return err
	}
	if s.checkDNS == nil || s.monitorInterval <= 0 {
		return fmt.Errorf("configured DNS monitoring is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.lock(ctx); err != nil {
		return fmt.Errorf("acquire deployment lock: %w", err)
	}
	// No mutation until a full baseline round has passed. Expectations must
	// come from configuration, never from this round's observed answers.
	if err := s.checkDNS(ctx); err != nil {
		return fmt.Errorf("baseline DNS (lock retained): %w", err)
	}
	rolloutCtx, cancelRollout := context.WithCancel(ctx)
	defer cancelRollout()
	monitorCtx, stopMonitor := context.WithCancel(ctx)
	defer stopMonitor()
	stopRequested := make(chan struct{})
	monitorDone := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(s.monitorInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopRequested:
				monitorDone <- nil
				return
			default:
			}
			select {
			case <-stopRequested:
				monitorDone <- nil
				return
			case <-monitorCtx.Done():
				monitorDone <- nil
				return
			case <-ticker.C:
				if err := s.checkDNS(monitorCtx); err != nil {
					// Only our cancellation error is normal shutdown. A real
					// failure racing shutdown must still retain the lock.
					if !(monitorCtx.Err() != nil && errors.Is(err, monitorCtx.Err())) {
						cancelRollout()
						monitorDone <- fmt.Errorf("monitored DNS (lock retained): %w", err)
						return
					}
				}
			}
		}
	}()
	err := runStages(rolloutCtx, pairs, s)
	close(stopRequested)
	if err != nil {
		stopMonitor()
	}
	// On success let an in-flight bounded round finish instead of cancelling
	// kubectl exec and losing the remote probe's final evidence. Failed stages
	// cancel promptly; they already retain the lock regardless of probe outcome.
	monitorErr := <-monitorDone
	if err = errors.Join(err, monitorErr, ctx.Err()); err != nil {
		return err
	}
	// The monitor is fully joined. Finish with an uncancelled complete round
	// so cancelling an in-flight probe cannot manufacture final success.
	if err := s.checkDNS(ctx); err != nil {
		return fmt.Errorf("final DNS (lock retained): %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.ownership(ctx); err != nil {
		return fmt.Errorf("ownership before unlock: %w", err)
	}
	return s.unlock(ctx)
}

func runStages(ctx context.Context, pairs []Pair, s steps) error {
	legacy, err := s.inspect(ctx)
	if err != nil {
		return fmt.Errorf("inspect topology (lock retained): %w", err)
	}
	var anchors, added, all []string
	for _, p := range pairs {
		anchors = append(anchors, p.Members[0].Workload)
		added = append(added, p.Members[1].Workload)
	}
	all = append(append([]string{}, anchors...), added...)
	existing := all
	if legacy {
		existing = anchors
	}
	health := func(g Gate) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.health(ctx, g); err != nil {
			return fmt.Errorf("health gate %s (lock retained): %w", g.Name, err)
		}
		return ctx.Err()
	}
	apply := func(stage Stage) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.ownership(ctx); err != nil {
			return fmt.Errorf("ownership before stage %s: %w", stage.Name, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.apply(ctx, stage); err != nil {
			return fmt.Errorf("stage %s (lock retained): %w", stage.Name, err)
		}
		return ctx.Err()
	}
	if err := health(Gate{Name: "existing", Required: existing}); err != nil {
		return err
	}
	if s.prepare != nil {
		if err := s.ownership(ctx); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.prepare(ctx); err != nil {
			return fmt.Errorf("supporting resources (lock retained): %w", err)
		}
		if err := health(Gate{Name: "prepared", Required: existing}); err != nil {
			return err
		}
	}
	if err := apply(Stage{Name: "frozen", LegacySelectors: legacy}); err != nil {
		return err
	}
	if legacy {
		// Resumed partners may have an earlier image. Keep them frozen through
		// cutover; serial replacement gates below verify every target image.
		if err := health(Gate{Name: "new", Required: all}); err != nil {
			return err
		}
		// A healthy partner is not protection while the VIP still excludes it.
		// Enrol the existing pods without replacement, then expose both members
		// before permitting the first disruptive workload update.
		if err := s.ownership(ctx); err != nil {
			return fmt.Errorf("ownership before enrolment: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.enroll(ctx, anchors); err != nil {
			return fmt.Errorf("enrol existing endpoints (lock retained): %w", err)
		}
		if err := apply(Stage{Name: "paired"}); err != nil {
			return err
		}
		if err := health(Gate{Name: "paired", Required: all, PairSelectors: true}); err != nil {
			return err
		}
	}
	for _, workload := range all {
		if err := health(Gate{Name: "all", Required: all, PairSelectors: true}); err != nil {
			return err
		}
		if err := apply(Stage{Name: workload, RollingWorkload: workload}); err != nil {
			return err
		}
		if err := health(Gate{Name: workload, Required: all, Desired: []string{workload}, PairSelectors: true}); err != nil {
			return err
		}
	}
	if err := health(Gate{Name: "desired", Required: all, Desired: all, PairSelectors: true}); err != nil {
		return err
	}
	if err := apply(Stage{Name: "final"}); err != nil {
		return err
	}
	if err := health(Gate{Name: "final", Required: all, Desired: all, PairSelectors: true}); err != nil {
		return err
	}
	if s.finalize != nil {
		if err := s.ownership(ctx); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.finalize(ctx); err != nil {
			return fmt.Errorf("bootstrap (lock retained): %w", err)
		}
		if err := health(Gate{Name: "bootstrapped", Required: all, Desired: all, PairSelectors: true}); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func validatePairs(pairs []Pair) error {
	if len(pairs) != 2 {
		return fmt.Errorf("kw requires exactly two DNS failover pairs")
	}
	names, addresses, members := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, p := range pairs {
		if p.Name == "" || names[p.Name] {
			return fmt.Errorf("empty or duplicate pair name %q", p.Name)
		}
		names[p.Name] = true
		if (p.Address != "192.168.10.136" && p.Address != "192.168.10.139") || addresses[p.Address] {
			return fmt.Errorf("invalid or duplicate pair address %q", p.Address)
		}
		addresses[p.Address] = true
		if p.Members[0].Node == p.Members[1].Node {
			return fmt.Errorf("pair %s members must be on different nodes", p.Name)
		}
		for _, m := range p.Members {
			if m.Workload == "" || m.Node == "" || members[m.Workload] {
				return fmt.Errorf("empty or overlapping member in pair %s", p.Name)
			}
			members[m.Workload] = true
		}
	}
	return nil
}
