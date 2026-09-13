package rollout

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var t0 = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

func eng(name string, connected bool, applied uint64) Engine {
	return Engine{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)), Name: name, Connected: connected,
		AppliedVersion: applied, Labels: map[string]string{}}
}

func canaryParams() Params {
	return Params{Strategy: CanaryStrategy, CanaryCount: 1, AckTimeoutSeconds: 60, HealthWindowSeconds: 30,
		MaxServfailRatio: 0.05, MinHealthQueries: 100}
}

func TestAllAtOnceCompletesWhenConnectedEnginesApply(t *testing.T) {
	r := Rollout{Version: 7, Kind: KindChange, State: Pending, Params: Params{Strategy: AllAtOnce, AckTimeoutSeconds: 60}}
	es := []Engine{eng("a", true, 6), eng("b", true, 6), eng("c", false, 3)}
	r, ch := Step(r, Observation{Now: t0, Engines: es})
	if !ch || r.State != Rolling || !r.PhaseStartedAt.Equal(t0) {
		t.Fatalf("pending -> %s (changed=%v)", r.State, ch)
	}
	if r, ch = Step(r, Observation{Now: t0.Add(5 * time.Second), Engines: es}); ch {
		t.Fatalf("advanced to %s before any ack", r.State)
	}
	es[0].AppliedVersion, es[1].AppliedVersion = 7, 7
	if r, ch = Step(r, Observation{Now: t0.Add(6 * time.Second), Engines: es}); !ch || r.State != Completed {
		t.Fatalf("got %s, want completed; a disconnected engine must not block", r.State)
	}
}

func TestCanaryHappyPath(t *testing.T) {
	es := []Engine{eng("a", true, 6), eng("b", true, 6), eng("c", true, 6)}
	es[2].Labels[CanaryLabel] = "true"
	r := Rollout{Version: 7, Kind: KindChange, State: Pending, Params: canaryParams()}
	r, _ = Step(r, Observation{Now: t0, Engines: es})
	if r.State != Canary || len(r.CanaryEngineIDs) != 1 || r.CanaryEngineIDs[0] != es[2].ID {
		t.Fatalf("state %s canaries %v, want canary [c]", r.State, r.CanaryEngineIDs)
	}
	es[2].AppliedVersion = 7
	r, _ = Step(r, Observation{Now: t0.Add(3 * time.Second), Engines: es})
	if r.State != Verifying {
		t.Fatalf("state %s, want verifying", r.State)
	}
	es[2].Health = Health{Samples: 3, Queries: 1000, Servfail: 10}
	if r, _ = Step(r, Observation{Now: t0.Add(20 * time.Second), Engines: es}); r.State != Verifying {
		t.Fatalf("left verifying before the health window: %s", r.State)
	}
	if r, _ = Step(r, Observation{Now: t0.Add(34 * time.Second), Engines: es}); r.State != Rolling {
		t.Fatalf("state %s, want rolling (1%% servfail is under 5%%)", r.State)
	}
	es[0].AppliedVersion, es[1].AppliedVersion = 7, 7
	if r, _ = Step(r, Observation{Now: t0.Add(40 * time.Second), Engines: es}); r.State != Completed {
		t.Fatalf("state %s, want completed", r.State)
	}
}

func verifyingWith(h Health) (Rollout, []Engine) {
	es := []Engine{eng("a", true, 7), eng("b", true, 6)}
	es[0].Health = h
	return Rollout{Version: 7, Kind: KindChange, State: Verifying, PhaseStartedAt: t0,
		CanaryEngineIDs: []uuid.UUID{es[0].ID}, Params: canaryParams()}, es
}

func TestHaltsOnServfailRatio(t *testing.T) {
	r, es := verifyingWith(Health{Samples: 3, Queries: 400, Servfail: 380})
	r, ch := Step(r, Observation{Now: t0.Add(31 * time.Second), Engines: es})
	if !ch || r.State != Halted || !strings.Contains(r.HaltReason, "engine a servfail ratio 0.950 > 0.050") {
		t.Fatalf("state %s reason %q", r.State, r.HaltReason)
	}
	if r, ch = Step(r, Observation{Now: t0.Add(60 * time.Second), Engines: es}); ch || r.State != Halted {
		t.Fatalf("halted rollout moved on its own to %s", r.State)
	}
}

func TestLowTrafficPassesGate(t *testing.T) {
	r, es := verifyingWith(Health{Samples: 3, Queries: 20, Servfail: 20})
	if r, _ = Step(r, Observation{Now: t0.Add(31 * time.Second), Engines: es}); r.State != Rolling {
		t.Fatalf("state %s, want rolling below min_health_queries", r.State)
	}
}

func TestHaltsWhenCanaryStopsReporting(t *testing.T) {
	r, es := verifyingWith(Health{Samples: 1, Queries: 5000})
	if r, _ = Step(r, Observation{Now: t0.Add(31 * time.Second), Engines: es}); r.State != Halted ||
		!strings.Contains(r.HaltReason, "stopped reporting") {
		t.Fatalf("state %s reason %q", r.State, r.HaltReason)
	}
}

func TestHaltsOnCanaryRejectAndAckTimeout(t *testing.T) {
	es := []Engine{eng("a", true, 6), eng("b", true, 6)}
	base := Rollout{Version: 7, Kind: KindChange, State: Canary, PhaseStartedAt: t0,
		CanaryEngineIDs: []uuid.UUID{es[0].ID}, Params: canaryParams()}

	rej := append([]Engine(nil), es...)
	rej[0].RejectedVersion, rej[0].RejectedReason = 7, "cache max_bytes below 1048576"
	if r, _ := Step(base, Observation{Now: t0.Add(time.Second), Engines: rej}); r.State != Halted ||
		r.HaltReason != "engine a rejected version 7: cache max_bytes below 1048576" {
		t.Fatalf("reject: state %s reason %q", r.State, r.HaltReason)
	}
	if r, ch := Step(base, Observation{Now: t0.Add(59 * time.Second), Engines: es}); ch {
		t.Fatalf("halted before the ack timeout: %s", r.State)
	}
	if r, _ := Step(base, Observation{Now: t0.Add(61 * time.Second), Engines: es}); r.State != Halted ||
		r.HaltReason != "engine a did not apply version 7 within 60s" {
		t.Fatalf("timeout: state %s reason %q", r.State, r.HaltReason)
	}
}

func TestPausedGroupHoldsChangesOnly(t *testing.T) {
	es := []Engine{eng("a", true, 6)}
	change := Rollout{Version: 8, Kind: KindChange, State: Pending, Params: canaryParams()}
	if r, ch := Step(change, Observation{Now: t0, GroupPaused: true, Engines: es}); ch || r.State != Pending {
		t.Fatalf("paused change moved to %s", r.State)
	}
	rb := Rollout{Version: 9, Kind: KindRollback, State: Pending, Params: canaryParams()}
	if r, _ := Step(rb, Observation{Now: t0, GroupPaused: true, Engines: es}); r.State != Rolling {
		t.Fatalf("rollback under pause went to %s, want rolling (all at once)", r.State)
	}
}

func TestSelectCanaries(t *testing.T) {
	es := []Engine{eng("d", true, 0), eng("b", true, 0), eng("a", false, 0), eng("c", true, 0), eng("e", true, 0)}
	es[3].Labels[CanaryLabel] = "true" // c
	got := SelectCanaries(Params{Strategy: CanaryStrategy, CanaryCount: 1, CanaryPercent: 50}, es)
	// 4 connected, 50% -> 2; c first by label, then b by name
	if len(got) != 2 || got[0] != es[3].ID || got[1] != es[1].ID {
		t.Fatalf("got %v", got)
	}
	if got := SelectCanaries(Params{Strategy: CanaryStrategy, CanaryCount: 10}, es); len(got) != 3 {
		t.Fatalf("canary set must leave one connected engine out, got %d", len(got))
	}
	if got := SelectCanaries(Params{Strategy: CanaryStrategy, CanaryPercent: 1}, es[:1]); len(got) != 1 {
		t.Fatalf("single engine group: got %d canaries, want 1", len(got))
	}
	if got := SelectCanaries(Params{Strategy: CanaryStrategy, CanaryCount: 1}, []Engine{eng("x", false, 0)}); len(got) != 0 {
		t.Fatalf("no connected engines must yield no canaries, got %v", got)
	}
}

func TestTarget(t *testing.T) {
	a, b := eng("a", true, 7), eng("b", true, 6)
	canary := &Rollout{Version: 7, State: Verifying, CanaryEngineIDs: []uuid.UUID{a.ID}}
	if Target(a, 6, canary) != 7 || Target(b, 6, canary) != 6 {
		t.Fatal("canary phase: only canaries target the new version")
	}
	halted := &Rollout{Version: 7, State: Halted, CanaryEngineIDs: []uuid.UUID{a.ID}}
	if Target(a, 6, halted) != 7 || Target(b, 6, halted) != 6 {
		t.Fatal("halted: engines already on the version keep it, others stay stable")
	}
	for _, s := range []State{Rolling, Completed, RolledBack} {
		if Target(b, 6, &Rollout{Version: 7, State: s}) != 7 {
			t.Fatalf("%s: everyone targets the version", s)
		}
	}
	if Target(b, 6, &Rollout{Version: 8, State: Pending}) != 6 || Target(b, 6, nil) != 6 {
		t.Fatal("pending or no rollout: stable")
	}
}
