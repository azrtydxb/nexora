package rollout

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

// allowed lists the only states Step may move each state to. Halted and the terminal states
// never move through Step (rolled_back and superseded are set by Create).
var allowed = map[State][]State{
	Pending:    {Canary, Rolling},
	Canary:     {Verifying, Halted},
	Verifying:  {Rolling, Halted},
	Rolling:    {Completed, Halted},
	Completed:  nil,
	Halted:     nil,
	RolledBack: nil,
	Superseded: nil,
}

// engineVariant is one point of the per-engine observation space.
type engineVariant struct {
	connected, applied, rejected bool
	health                       Health
}

func variants() []engineVariant {
	var out []engineVariant
	healths := []Health{
		{}, // stopped reporting
		{Samples: 3, Queries: 1000, Servfail: 10},  // healthy
		{Samples: 3, Queries: 1000, Servfail: 900}, // failing
		{Samples: 3, Queries: 5, Servfail: 5},      // below min_health_queries
	}
	for _, c := range []bool{true, false} {
		for _, a := range []bool{true, false} {
			for _, r := range []bool{false, true} {
				if a && r {
					continue // an engine that applied the version cannot also have rejected it
				}
				for _, h := range healths {
					out = append(out, engineVariant{connected: c, applied: a, rejected: r, health: h})
				}
			}
		}
	}
	return out
}

func buildEngines(vs []engineVariant, version uint64) []Engine {
	es := make([]Engine, len(vs))
	for i, v := range vs {
		es[i] = Engine{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte{byte(i)}), Name: fmt.Sprintf("e%d", i),
			Labels: map[string]string{}, Connected: v.connected, AppliedVersion: version - 1, Health: v.health}
		if v.applied {
			es[i].AppliedVersion = version
		}
		if v.rejected {
			es[i].RejectedVersion, es[i].RejectedReason = version, "bad"
		}
	}
	return es
}

// checkStep asserts every invariant of one Step call.
func checkStep(t *testing.T, r Rollout, obs Observation) Rollout {
	t.Helper()
	next, changed := Step(r, obs)
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("%s\n from %+v\n obs %+v\n to %+v (changed=%v)", fmt.Sprintf(format, args...), r, obs, next, changed)
	}
	if next.ID != r.ID || next.EngineGroupID != r.EngineGroupID || next.Version != r.Version || next.Kind != r.Kind ||
		next.Params != r.Params {
		fail("identity, version, kind or params changed")
	}
	if !changed {
		if !reflect.DeepEqual(next, r) {
			fail("unchanged step modified the rollout")
		}
		return next
	}
	if next.State == r.State {
		fail("changed without a state transition")
	}
	if !slices.Contains(allowed[r.State], next.State) {
		fail("illegal transition %s -> %s", r.State, next.State)
	}
	if next.State != Completed && !next.PhaseStartedAt.Equal(obs.Now) {
		fail("entering %s must start the phase at now", next.State)
	}
	if next.State == Halted && next.HaltReason == "" || next.State != Halted && next.HaltReason != r.HaltReason {
		fail("halt reason must be set exactly when halting")
	}
	connected := map[uuid.UUID]bool{}
	for _, e := range obs.Engines {
		connected[e.ID] = e.Connected
	}
	switch next.State {
	case Canary:
		if r.Kind != KindChange || r.Params.Strategy != CanaryStrategy || obs.GroupPaused {
			fail("only an unpaused canary change enters canary")
		}
		n := 0
		for _, c := range connected {
			if c {
				n++
			}
		}
		ids := next.CanaryEngineIDs
		if len(ids) == 0 && n > 0 || n >= 2 && len(ids) >= n {
			fail("canary set size %d for %d connected engines", len(ids), n)
		}
		seen := map[uuid.UUID]bool{}
		for _, id := range ids {
			if !connected[id] || seen[id] {
				fail("canary %s not connected or duplicated", id)
			}
			seen[id] = true
		}
	case Rolling:
		if r.State == Pending && r.Kind == KindChange && (obs.GroupPaused || r.Params.Strategy != AllAtOnce) {
			fail("a paused or canary change skipped the canary phase")
		}
		if r.State == Verifying {
			if obs.Now.Sub(r.PhaseStartedAt) < time.Duration(r.Params.HealthWindowSeconds)*time.Second {
				fail("left verifying before the health window")
			}
			for _, e := range pick(obs.Engines, r.CanaryEngineIDs) {
				h := e.Health
				if h.Samples < 2 || e.RejectedVersion == r.Version ||
					h.Queries >= r.Params.MinHealthQueries && h.Queries > 0 &&
						float64(h.Servfail)/float64(h.Queries) > r.Params.MaxServfailRatio {
					fail("unhealthy canary %s passed the gate", e.Name)
				}
			}
		}
	case Verifying:
		for _, e := range pick(obs.Engines, r.CanaryEngineIDs) {
			if e.AppliedVersion < r.Version || e.RejectedVersion == r.Version {
				fail("verifying with canary %s not applied", e.Name)
			}
		}
	case Completed:
		for _, e := range obs.Engines {
			if e.RejectedVersion == r.Version || e.Connected && e.AppliedVersion < r.Version {
				fail("completed while engine %s had not applied", e.Name)
			}
		}
	case Halted:
		if r.State != Rolling && r.State != Canary && r.State != Verifying {
			fail("halted from %s", r.State)
		}
	}
	return next
}

// TestStepTransitionTable enumerates every state, kind, strategy, pause flag, phase age and
// observation of up to three engines (with and without canary membership) and checks the
// transition invariants for each.
func TestStepTransitionTable(t *testing.T) {
	vs := variants()
	var observations [][]engineVariant
	observations = append(observations, nil)
	for _, a := range vs {
		observations = append(observations, []engineVariant{a})
		for _, b := range vs {
			observations = append(observations, []engineVariant{a, b})
		}
	}
	// A three-engine sample keeps the table exhaustive for one and two engines and broad for three.
	rng := rand.New(rand.NewPCG(1, 2))
	for range 200 {
		observations = append(observations, []engineVariant{vs[rng.IntN(len(vs))], vs[rng.IntN(len(vs))], vs[rng.IntN(len(vs))]})
	}
	const version = 7
	rolloutID := uuid.New()
	ages := []time.Duration{0, 29 * time.Second, 30 * time.Second, 60 * time.Second, 61 * time.Second}
	states := []State{Pending, Canary, Verifying, Rolling, Completed, Halted, RolledBack, Superseded}
	steps := 0
	for _, state := range states {
		for _, kind := range []Kind{KindChange, KindRollback, KindRepublish} {
			for _, strategy := range []Strategy{AllAtOnce, CanaryStrategy} {
				params := Params{Strategy: strategy, CanaryCount: 1, AckTimeoutSeconds: 60, HealthWindowSeconds: 30,
					MaxServfailRatio: 0.05, MinHealthQueries: 100}
				for _, paused := range []bool{false, true} {
					for _, age := range ages {
						for _, ov := range observations {
							es := buildEngines(ov, version)
							canarySets := [][]uuid.UUID{nil}
							if len(es) > 0 {
								canarySets = append(canarySets, []uuid.UUID{es[0].ID})
							}
							for _, canaries := range canarySets {
								r := Rollout{ID: rolloutID, Version: version, Kind: kind, State: state, Params: params,
									PhaseStartedAt: t0, CanaryEngineIDs: canaries}
								if state == Halted {
									r.HaltReason = "earlier"
								}
								checkStep(t, r, Observation{Now: t0.Add(age), GroupPaused: paused, Engines: es})
								steps++
							}
						}
					}
				}
			}
		}
	}
	t.Logf("%d steps checked", steps)
}

// TestStepRandomWalk drives rollouts through random observation sequences: every path follows
// the transition graph, a terminal or halted rollout never moves again, and each state is
// entered at most once (no double advance, no cycles).
func TestStepRandomWalk(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	vs := variants()
	for walk := range 2000 {
		strategy := []Strategy{AllAtOnce, CanaryStrategy}[rng.IntN(2)]
		kind := []Kind{KindChange, KindRollback, KindRepublish}[rng.IntN(3)]
		r := Rollout{ID: uuid.New(), Version: 9, Kind: kind, State: Pending, Params: Params{Strategy: strategy,
			CanaryPercent: 50, AckTimeoutSeconds: 60, HealthWindowSeconds: 30, MaxServfailRatio: 0.05, MinHealthQueries: 100}}
		now := t0
		visited := map[State]bool{Pending: true}
		n := 1 + rng.IntN(4)
		for step := range 40 {
			ov := make([]engineVariant, n)
			for i := range ov {
				ov[i] = vs[rng.IntN(len(vs))]
			}
			now = now.Add(time.Duration(rng.IntN(40)) * time.Second)
			before := r
			r = checkStep(t, r, Observation{Now: now, GroupPaused: rng.IntN(4) == 0, Engines: buildEngines(ov, 9)})
			if r.State != before.State {
				if visited[r.State] {
					t.Fatalf("walk %d step %d: state %s entered twice", walk, step, r.State)
				}
				visited[r.State] = true
			}
		}
	}
}

// TestTargetTable checks Target over every rollout state: the result is the stable version or the
// rollout's version, and the new version reaches a non-canary only once rolling.
func TestTargetTable(t *testing.T) {
	canary, other := eng("canary", true, 6), eng("other", true, 6)
	onNew := eng("on-new", true, 7)
	for _, s := range []State{Pending, Canary, Verifying, Rolling, Completed, Halted, RolledBack, Superseded} {
		r := &Rollout{Version: 7, State: s, CanaryEngineIDs: []uuid.UUID{canary.ID}}
		for _, e := range []Engine{canary, other, onNew} {
			got := Target(e, 6, r)
			if got != 6 && got != 7 {
				t.Fatalf("%s/%s: target %d is neither stable nor the rollout version", s, e.Name, got)
			}
			want := uint64(6)
			switch {
			case s == Rolling || s == Completed || s == RolledBack:
				want = 7
			case (s == Canary || s == Verifying) && e.ID == canary.ID:
				want = 7
			case s == Halted && e.AppliedVersion == 7:
				want = 7
			}
			if got != want {
				t.Errorf("%s/%s: target %d, want %d", s, e.Name, got, want)
			}
		}
	}
}
