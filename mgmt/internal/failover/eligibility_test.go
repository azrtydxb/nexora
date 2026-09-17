package failover_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/piwi3910/nexora/mgmt/internal/failover"
)

func eligibilityFixture() (failover.Group, []failover.MemberEvidence, time.Time) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	g := failover.Group{Name: "dns136", FrontendIP: "192.168.10.136", Members: [2]uuid.UUID{uuid.New(), uuid.New()}}
	policy := uuid.New()
	members := make([]failover.MemberEvidence, 2)
	for i, id := range g.Members {
		pod := uuid.NewString()
		snapshot := failover.EligibilitySnapshot{Version: 3, Digest: strings.Repeat("a", 64)}
		check := failover.EligibilityCheck{OK: true, ObservedAt: now}
		members[i] = failover.MemberEvidence{
			EngineID: id, PolicyGroupID: policy, PodUID: pod, ObservedAt: now,
			Placement: failover.TrustedPlacement{EngineID: id, PodUID: pod, NodeUID: uuid.NewString(), NodeName: []string{"worker-a", "worker-b"}[i], ObservedAt: now},
			Ready:     check, Management: check, DirectDNS: check,
			Applied: snapshot, Target: snapshot, SnapshotObservedAt: now,
		}
	}
	return g, members, now
}

func TestEligibilityHealthy(t *testing.T) {
	for _, mode := range []string{"both identifiers", "node UIDs", "node names", "equal content different versions", "reversed evidence"} {
		t.Run(mode, func(t *testing.T) {
			g, members, now := eligibilityFixture()
			switch mode {
			case "node UIDs":
				for i := range members {
					members[i].Placement.NodeName = ""
				}
			case "node names":
				for i := range members {
					members[i].Placement.NodeUID = ""
				}
			case "equal content different versions":
				members[1].Target.Version++
				members[1].Applied.Version++
			case "reversed evidence":
				members[0], members[1] = members[1], members[0]
			}
			before := slices.Clone(members)
			got := failover.EvaluateEligibility(g, members, now, 30*time.Second)
			if !reflect.DeepEqual(got.Eligible, g.Members[:]) || len(got.Reasons) != 0 {
				t.Fatalf("%+v", got)
			}
			for i, m := range got.Members {
				if !m.Eligible || len(m.Reasons) != 0 || m.EngineID != g.Members[i] {
					t.Fatalf("%+v", m)
				}
			}
			if !reflect.DeepEqual(before, members) {
				t.Fatal("mutated caller evidence")
			}
		})
	}
}

func TestEligibilityPairDenials(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*failover.Group, *[]failover.MemberEvidence)
		want   failover.EligibilityReason
	}{
		{"invalid group", func(g *failover.Group, _ *[]failover.MemberEvidence) { g.FrontendIP = "::1" }, failover.ReasonInvalidGroup},
		{"nil desired identity", func(g *failover.Group, _ *[]failover.MemberEvidence) { g.Members[0] = uuid.Nil }, failover.ReasonInvalidGroup},
		{"duplicate desired identity", func(g *failover.Group, _ *[]failover.MemberEvidence) { g.Members[1] = g.Members[0] }, failover.ReasonInvalidGroup},
		{"no evidence", func(_ *failover.Group, m *[]failover.MemberEvidence) { *m = nil }, failover.ReasonExpectedMembers},
		{"missing partner", func(_ *failover.Group, m *[]failover.MemberEvidence) { *m = (*m)[:1] }, failover.ReasonExpectedMembers},
		{"extra member", func(_ *failover.Group, m *[]failover.MemberEvidence) { *m = append(*m, (*m)[0]) }, failover.ReasonExpectedMembers},
		{"duplicate evidence", func(_ *failover.Group, m *[]failover.MemberEvidence) { (*m)[1] = (*m)[0] }, failover.ReasonExpectedMembers},
		{"unknown identity", func(_ *failover.Group, m *[]failover.MemberEvidence) { (*m)[0].EngineID = uuid.New() }, failover.ReasonExpectedMembers},
		{"nil evidence identity", func(_ *failover.Group, m *[]failover.MemberEvidence) { (*m)[0].EngineID = uuid.Nil }, failover.ReasonExpectedMembers},
		{"same node UID different names", func(_ *failover.Group, m *[]failover.MemberEvidence) {
			(*m)[1].Placement.NodeUID = (*m)[0].Placement.NodeUID
		}, failover.ReasonFailureDomain},
		{"same name different UIDs", func(_ *failover.Group, m *[]failover.MemberEvidence) {
			(*m)[1].Placement.NodeName = (*m)[0].Placement.NodeName
		}, failover.ReasonFailureDomain},
		{"incomparable nodes", func(_ *failover.Group, m *[]failover.MemberEvidence) {
			(*m)[0].Placement.NodeName = ""
			(*m)[1].Placement.NodeUID = ""
		}, failover.ReasonFailureDomain},
		{"same pod", func(_ *failover.Group, m *[]failover.MemberEvidence) {
			(*m)[1].PodUID = (*m)[0].PodUID
			(*m)[1].Placement.PodUID = (*m)[0].PodUID
		}, failover.ReasonFailureDomain},
		{"missing policy", func(_ *failover.Group, m *[]failover.MemberEvidence) { (*m)[0].PolicyGroupID = uuid.Nil }, failover.ReasonPolicyMismatch},
		{"different policies same content", func(_ *failover.Group, m *[]failover.MemberEvidence) { (*m)[1].PolicyGroupID = uuid.New() }, failover.ReasonPolicyMismatch},
		{"different target content", func(_ *failover.Group, m *[]failover.MemberEvidence) {
			(*m)[0].Target.Digest = strings.Repeat("b", 64)
			(*m)[0].Applied = (*m)[0].Target
		}, failover.ReasonTargetMismatch},
		{"missing target", func(_ *failover.Group, m *[]failover.MemberEvidence) { (*m)[0].Target = failover.EligibilitySnapshot{} }, failover.ReasonTargetMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, members, now := eligibilityFixture()
			tc.mutate(&g, &members)
			got := failover.EvaluateEligibility(g, members, now, 30*time.Second)
			assertEmptyEligibility(t, got, tc.want)
			for _, m := range got.Members {
				if !slices.Contains(m.Reasons, tc.want) {
					t.Fatalf("missing member denial %s: %+v", tc.want, m)
				}
			}
		})
	}
}

func assertEmptyEligibility(t *testing.T, got failover.Eligibility, reason failover.EligibilityReason) {
	t.Helper()
	if got.Eligible == nil || len(got.Eligible) != 0 || got.Members[0].Eligible || got.Members[1].Eligible ||
		!slices.Contains(got.Reasons, failover.ReasonEmptyPool) || !slices.Contains(got.Reasons, reason) {
		t.Fatalf("expected explicit empty pool with %s: %+v", reason, got)
	}
}

func TestEligibilityMalformedBindings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*failover.MemberEvidence)
	}{
		{"absent binding", func(m *failover.MemberEvidence) { m.Placement = failover.TrustedPlacement{} }},
		{"missing bound ID", func(m *failover.MemberEvidence) { m.Placement.EngineID = uuid.Nil }},
		{"wrong bound ID", func(m *failover.MemberEvidence) { m.Placement.EngineID = uuid.New() }},
		{"missing pod", func(m *failover.MemberEvidence) { m.PodUID = ""; m.Placement.PodUID = "" }},
		{"malformed pod", func(m *failover.MemberEvidence) { m.PodUID = "pod-a"; m.Placement.PodUID = m.PodUID }},
		{"zero pod", func(m *failover.MemberEvidence) { m.PodUID = uuid.Nil.String(); m.Placement.PodUID = m.PodUID }},
		{"noncanonical pod", func(m *failover.MemberEvidence) { m.PodUID = "urn:uuid:" + m.PodUID; m.Placement.PodUID = m.PodUID }},
		{"replaced pod", func(m *failover.MemberEvidence) { m.Placement.PodUID = uuid.NewString() }},
		{"missing node", func(m *failover.MemberEvidence) { m.Placement.NodeUID = ""; m.Placement.NodeName = "" }},
		{"malformed node UID with valid name", func(m *failover.MemberEvidence) { m.Placement.NodeUID = "bad" }},
		{"zero node UID", func(m *failover.MemberEvidence) { m.Placement.NodeUID = uuid.Nil.String() }},
		{"whitespace node", func(m *failover.MemberEvidence) { m.Placement.NodeName = " worker-a" }},
		{"uppercase node", func(m *failover.MemberEvidence) { m.Placement.NodeName = "WORKER-A" }},
		{"bad node label", func(m *failover.MemberEvidence) { m.Placement.NodeName = "worker_a" }},
		{"trailing dot", func(m *failover.MemberEvidence) { m.Placement.NodeName = "worker-a." }},
		{"empty label", func(m *failover.MemberEvidence) { m.Placement.NodeName = "a..b" }},
		{"long node", func(m *failover.MemberEvidence) { m.Placement.NodeName = strings.Repeat("a.", 127) + "a" }},
		{"long label", func(m *failover.MemberEvidence) { m.Placement.NodeName = strings.Repeat("a", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, members, now := eligibilityFixture()
			tc.mutate(&members[0])
			got := failover.EvaluateEligibility(g, members, now, 30*time.Second)
			assertEmptyEligibility(t, got, failover.ReasonFailureDomain)
			if !slices.Contains(got.Members[0].Reasons, failover.ReasonInvalidBinding) {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestEligibilityMemberFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*failover.MemberEvidence)
		want   failover.EligibilityReason
	}{
		{"revoked", func(m *failover.MemberEvidence) { m.Revoked = true }, failover.ReasonRevoked},
		{"deleted", func(m *failover.MemberEvidence) { m.Deleted = true }, failover.ReasonDeleted},
		{"not ready", func(m *failover.MemberEvidence) { m.Ready.OK = false }, failover.ReasonNotReady},
		{"disconnected", func(m *failover.MemberEvidence) { m.Management.OK = false }, failover.ReasonManagement},
		{"DNS failed", func(m *failover.MemberEvidence) { m.DirectDNS.OK = false }, failover.ReasonDirectDNS},
		{"old version same content", func(m *failover.MemberEvidence) { m.Applied.Version-- }, failover.ReasonSnapshot},
		{"ahead version", func(m *failover.MemberEvidence) { m.Applied.Version++ }, failover.ReasonSnapshot},
		{"same version wrong content", func(m *failover.MemberEvidence) { m.Applied.Digest = strings.Repeat("b", 64) }, failover.ReasonSnapshot},
		{"missing digest", func(m *failover.MemberEvidence) { m.Applied.Digest = "" }, failover.ReasonSnapshot},
		{"malformed digest", func(m *failover.MemberEvidence) { m.Applied.Digest = strings.Repeat("z", 64) }, failover.ReasonSnapshot},
		{"uppercase digest", func(m *failover.MemberEvidence) { m.Applied.Digest = strings.Repeat("A", 64) }, failover.ReasonSnapshot},
		{"zero version", func(m *failover.MemberEvidence) { m.Applied.Version = 0 }, failover.ReasonSnapshot},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for failed := range 2 {
				g, members, now := eligibilityFixture()
				tc.mutate(&members[failed])
				got := failover.EvaluateEligibility(g, members, now, 30*time.Second)
				if !reflect.DeepEqual(got.Eligible, []uuid.UUID{g.Members[1-failed]}) || len(got.Reasons) != 0 ||
					!reflect.DeepEqual(got.Members[failed].Reasons, []failover.EligibilityReason{tc.want}) {
					t.Fatalf("%+v", got)
				}
				tc.mutate(&members[1-failed])
				got = failover.EvaluateEligibility(g, members, now, 30*time.Second)
				assertEmptyEligibility(t, got, failover.ReasonEmptyPool)
			}
		})
	}
}

func TestEligibilityObservationTimes(t *testing.T) {
	for _, field := range []struct {
		name   string
		at     func(*failover.MemberEvidence) *time.Time
		reason failover.EligibilityReason
	}{
		{"binding", func(m *failover.MemberEvidence) *time.Time { return &m.Placement.ObservedAt }, failover.ReasonBindingTime},
		{"inventory", func(m *failover.MemberEvidence) *time.Time { return &m.ObservedAt }, failover.ReasonInventoryTime},
		{"ready", func(m *failover.MemberEvidence) *time.Time { return &m.Ready.ObservedAt }, failover.ReasonNotReady},
		{"management", func(m *failover.MemberEvidence) *time.Time { return &m.Management.ObservedAt }, failover.ReasonManagement},
		{"DNS", func(m *failover.MemberEvidence) *time.Time { return &m.DirectDNS.ObservedAt }, failover.ReasonDirectDNS},
		{"snapshot", func(m *failover.MemberEvidence) *time.Time { return &m.SnapshotObservedAt }, failover.ReasonSnapshot},
	} {
		for _, age := range []string{"zero", "future", "stale", "boundary"} {
			t.Run(field.name+"/"+age, func(t *testing.T) {
				g, members, now := eligibilityFixture()
				at := field.at(&members[0])
				switch age {
				case "zero":
					*at = time.Time{}
				case "future":
					*at = now.Add(time.Nanosecond)
				case "stale":
					*at = now.Add(-30*time.Second - time.Nanosecond)
				case "boundary":
					*at = now.Add(-30 * time.Second)
				}
				got := failover.EvaluateEligibility(g, members, now, 30*time.Second)
				if age == "boundary" {
					if len(got.Eligible) != 2 {
						t.Fatalf("%+v", got)
					}
					return
				}
				if !slices.Contains(got.Members[0].Reasons, field.reason) || got.Members[0].Eligible {
					t.Fatalf("%+v", got)
				}
				if field.name == "binding" {
					assertEmptyEligibility(t, got, failover.ReasonFailureDomain)
				} else if !reflect.DeepEqual(got.Eligible, []uuid.UUID{g.Members[1]}) {
					t.Fatalf("%+v", got)
				}
			})
		}
	}
	g, members, now := eligibilityFixture()
	for _, limit := range []time.Duration{0, -time.Second} {
		assertEmptyEligibility(t, failover.EvaluateEligibility(g, members, now, limit), failover.ReasonInvalidClock)
	}
	assertEmptyEligibility(t, failover.EvaluateEligibility(g, members, time.Time{}, time.Second), failover.ReasonInvalidClock)
}

func TestEligibilityAccumulatesDenials(t *testing.T) {
	g, members, now := eligibilityFixture()
	members[0].Revoked, members[0].Deleted = true, true
	members[0].Ready.OK, members[0].Management.OK, members[0].DirectDNS.OK = false, false, false
	members[0].Applied.Version = 0
	got := failover.EvaluateEligibility(g, members, now, time.Second)
	want := []failover.EligibilityReason{failover.ReasonRevoked, failover.ReasonDeleted, failover.ReasonNotReady, failover.ReasonManagement, failover.ReasonDirectDNS, failover.ReasonSnapshot}
	if !reflect.DeepEqual(got.Members[0].Reasons, want) {
		t.Fatalf("%+v", got)
	}
	if again := failover.EvaluateEligibility(g, members, now, time.Second); !reflect.DeepEqual(got, again) {
		t.Fatal("nondeterministic result")
	}
}

func TestEligibilityZeroEvidenceFailsClosed(t *testing.T) {
	g, _, now := eligibilityFixture()
	members := []failover.MemberEvidence{{EngineID: g.Members[0]}, {EngineID: g.Members[1]}}
	got := failover.EvaluateEligibility(g, members, now, time.Second)
	assertEmptyEligibility(t, got, failover.ReasonFailureDomain)
	for _, m := range got.Members {
		for _, reason := range []failover.EligibilityReason{
			failover.ReasonInvalidBinding, failover.ReasonBindingTime, failover.ReasonInventoryTime,
			failover.ReasonNotReady, failover.ReasonManagement, failover.ReasonDirectDNS,
			failover.ReasonSnapshot, failover.ReasonPolicyMismatch, failover.ReasonTargetMismatch,
		} {
			if !slices.Contains(m.Reasons, reason) {
				t.Fatalf("missing %s: %+v", reason, got)
			}
		}
	}
}

func TestEligibilityReevaluationExpiresEvidence(t *testing.T) {
	g, members, now := eligibilityFixture()
	first := failover.EvaluateEligibility(g, members, now, time.Second)
	if len(first.Eligible) != 2 {
		t.Fatalf("%+v", first)
	}
	later := failover.EvaluateEligibility(g, members, now.Add(time.Second+time.Nanosecond), time.Second)
	assertEmptyEligibility(t, later, failover.ReasonFailureDomain)
	// A refreshed health check cannot rehabilitate a stale platform binding.
	for i := range members {
		members[i].Ready.ObservedAt = now.Add(2 * time.Second)
		members[i].Management.ObservedAt = now.Add(2 * time.Second)
		members[i].DirectDNS.ObservedAt = now.Add(2 * time.Second)
	}
	assertEmptyEligibility(t, failover.EvaluateEligibility(g, members, now.Add(2*time.Second), time.Second), failover.ReasonFailureDomain)
}
