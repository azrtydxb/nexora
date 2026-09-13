// Package rollout is the staged-rollout state machine. Step is pure: the controller loads a
// rollout and an observation of its engine group under an advisory lock, calls Step, and
// persists the result.
package rollout

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"
)

// State is a rollout state (rollouts.state).
type State string

const (
	Pending    State = "pending"
	Canary     State = "canary"
	Verifying  State = "verifying"
	Rolling    State = "rolling"
	Completed  State = "completed"
	Halted     State = "halted"
	RolledBack State = "rolled_back"
	Superseded State = "superseded"
)

// Terminal states are never stepped again.
func (s State) Terminal() bool { return s == Completed || s == RolledBack || s == Superseded }

// Strategy is how a change reaches an engine group (engine_groups.rollout_strategy).
type Strategy string

const (
	// AllAtOnce offers the version to every engine of the group at once.
	AllAtOnce Strategy = "all_at_once"
	// CanaryStrategy verifies the version on canaries before the rest of the group.
	CanaryStrategy Strategy = "canary"
)

// Kind is why a rollout exists (rollouts.kind).
type Kind string

const (
	// KindChange rolls out a configuration mutation.
	KindChange Kind = "change"
	// KindRollback rolls out a copy of an older group snapshot.
	KindRollback Kind = "rollback"
	// KindRepublish rolls out a copy of the group's snapshot to an engine moved into the group.
	KindRepublish Kind = "republish"
)

// CanaryLabel on an engine makes it preferred for canary selection.
const CanaryLabel = "nexora.io/canary"

// Params are the engine group's rollout parameters, copied into rollouts.params at creation.
type Params struct {
	Strategy            Strategy `json:"strategy"`
	CanaryCount         int      `json:"canary_count"`
	CanaryPercent       int      `json:"canary_percent"`
	AckTimeoutSeconds   int      `json:"ack_timeout_seconds"`
	HealthWindowSeconds int      `json:"health_window_seconds"`
	MaxServfailRatio    float64  `json:"max_servfail_ratio"`
	MinHealthQueries    uint64   `json:"min_health_queries"`
}

// Rollout is one group snapshot's rollout as the state machine sees it.
type Rollout struct {
	ID              uuid.UUID
	EngineGroupID   uuid.UUID
	Version         uint64
	Kind            Kind
	State           State
	CanaryEngineIDs []uuid.UUID
	PhaseStartedAt  time.Time
	HaltReason      string
	Params          Params
}

// Health is measured from the newest engine_stats sample taken at most 60 s before the phase
// start (the baseline) to the newest sample; Samples counts both ends.
type Health struct {
	Samples  int
	Queries  uint64
	Servfail uint64
}

// Engine is one non-revoked, non-deleted engine of the rollout's engine group.
type Engine struct {
	ID              uuid.UUID
	Name            string
	Labels          map[string]string
	Connected       bool
	AppliedVersion  uint64
	RejectedVersion uint64
	RejectedReason  string
	Health          Health
}

// Observation is the state of the engine group a step decides on.
type Observation struct {
	Now         time.Time
	GroupPaused bool
	Engines     []Engine // non-revoked, non-deleted engines of the rollout's engine group
}

func enter(r Rollout, s State, now time.Time) Rollout {
	r.State, r.PhaseStartedAt = s, now
	return r
}

func halt(r Rollout, now time.Time, reason string) Rollout {
	r = enter(r, Halted, now)
	r.HaltReason = reason
	return r
}

func pick(es []Engine, ids []uuid.UUID) []Engine {
	var out []Engine
	for _, e := range es {
		if slices.Contains(ids, e.ID) {
			out = append(out, e)
		}
	}
	return out
}

func rejection(r Rollout, es []Engine) (string, bool) {
	for _, e := range es {
		if e.RejectedVersion == r.Version {
			return fmt.Sprintf("engine %s rejected version %d: %s", e.Name, r.Version, e.RejectedReason), true
		}
	}
	return "", false
}

// firstUnapplied returns the first engine that has not applied r.Version. connectedOnly skips
// disconnected engines (rolling phase); canaries must apply even when their stream dropped.
func firstUnapplied(r Rollout, es []Engine, connectedOnly bool) (Engine, bool) {
	for _, e := range es {
		if connectedOnly && !e.Connected {
			continue
		}
		if e.AppliedVersion < r.Version {
			return e, true
		}
	}
	return Engine{}, false
}

// Step advances r by at most one transition and reports whether it changed.
func Step(r Rollout, obs Observation) (Rollout, bool) {
	if r.State.Terminal() || r.State == Halted {
		return r, false
	}
	ackTimeout := time.Duration(r.Params.AckTimeoutSeconds) * time.Second
	switch r.State {
	case Pending:
		if obs.GroupPaused && r.Kind == KindChange {
			return r, false
		}
		if r.Kind != KindChange || r.Params.Strategy == AllAtOnce {
			return enter(r, Rolling, obs.Now), true
		}
		r.CanaryEngineIDs = SelectCanaries(r.Params, obs.Engines)
		return enter(r, Canary, obs.Now), true

	case Canary:
		canaries := pick(obs.Engines, r.CanaryEngineIDs)
		if reason, bad := rejection(r, canaries); bad {
			return halt(r, obs.Now, reason), true
		}
		e, waiting := firstUnapplied(r, canaries, false)
		if !waiting {
			return enter(r, Verifying, obs.Now), true
		}
		if obs.Now.Sub(r.PhaseStartedAt) > ackTimeout {
			return halt(r, obs.Now, fmt.Sprintf("engine %s did not apply version %d within %ds", e.Name, r.Version, r.Params.AckTimeoutSeconds)), true
		}
		return r, false

	case Verifying:
		canaries := pick(obs.Engines, r.CanaryEngineIDs)
		if reason, bad := rejection(r, canaries); bad {
			return halt(r, obs.Now, reason), true
		}
		if obs.Now.Sub(r.PhaseStartedAt) < time.Duration(r.Params.HealthWindowSeconds)*time.Second {
			return r, false
		}
		for _, e := range canaries {
			if e.Health.Samples < 2 {
				return halt(r, obs.Now, fmt.Sprintf("engine %s stopped reporting health during verification", e.Name)), true
			}
			if e.Health.Queries < r.Params.MinHealthQueries || e.Health.Queries == 0 {
				continue
			}
			ratio := float64(e.Health.Servfail) / float64(e.Health.Queries)
			if ratio > r.Params.MaxServfailRatio {
				return halt(r, obs.Now, fmt.Sprintf("engine %s servfail ratio %.3f > %.3f over %d queries",
					e.Name, ratio, r.Params.MaxServfailRatio, e.Health.Queries)), true
			}
		}
		return enter(r, Rolling, obs.Now), true

	case Rolling:
		if reason, bad := rejection(r, obs.Engines); bad {
			return halt(r, obs.Now, reason), true
		}
		e, waiting := firstUnapplied(r, obs.Engines, true)
		if !waiting {
			r.State = Completed
			return r, true
		}
		if obs.Now.Sub(r.PhaseStartedAt) > ackTimeout {
			return halt(r, obs.Now, fmt.Sprintf("engine %s did not apply version %d within %ds", e.Name, r.Version, r.Params.AckTimeoutSeconds)), true
		}
	}
	return r, false
}

// SelectCanaries picks connected engines, labelled canaries first, then by name.
func SelectCanaries(p Params, engines []Engine) []uuid.UUID {
	var connected []Engine
	for _, e := range engines {
		if e.Connected {
			connected = append(connected, e)
		}
	}
	n := len(connected)
	if n == 0 {
		return nil
	}
	want := max(p.CanaryCount, int(math.Ceil(float64(n)*float64(p.CanaryPercent)/100)), 1)
	if n >= 2 {
		want = min(want, n-1)
	}
	want = min(want, n)
	sort.SliceStable(connected, func(i, j int) bool {
		ci, cj := connected[i].Labels[CanaryLabel] == "true", connected[j].Labels[CanaryLabel] == "true"
		if ci != cj {
			return ci
		}
		return connected[i].Name < connected[j].Name
	})
	ids := make([]uuid.UUID, want)
	for i := range ids {
		ids[i] = connected[i].ID
	}
	return ids
}

// Target is the version engine e should run; latest is its engine group's newest
// non-superseded rollout.
func Target(e Engine, stable uint64, latest *Rollout) uint64 {
	if latest == nil {
		return stable
	}
	switch latest.State {
	case Rolling, Completed, RolledBack:
		return latest.Version
	case Canary, Verifying:
		if slices.Contains(latest.CanaryEngineIDs, e.ID) {
			return latest.Version
		}
	case Halted:
		if e.AppliedVersion == latest.Version {
			return latest.Version
		}
	}
	return stable
}
