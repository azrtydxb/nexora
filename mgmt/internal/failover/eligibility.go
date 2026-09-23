package failover

import (
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// EligibilityReason is a stable denial code, not frontend health or ownership.
type EligibilityReason string

const (
	ReasonLifecycle       EligibilityReason = "lifecycle_not_active"
	ReasonInvalidGroup    EligibilityReason = "invalid_group"
	ReasonInvalidClock    EligibilityReason = "invalid_clock"
	ReasonExpectedMembers EligibilityReason = "expected_members"
	ReasonInvalidBinding  EligibilityReason = "invalid_binding"
	ReasonBindingTime     EligibilityReason = "binding_time"
	ReasonFailureDomain   EligibilityReason = "failure_domain_unproven"
	ReasonInventoryTime   EligibilityReason = "inventory_time"
	ReasonRevoked         EligibilityReason = "revoked"
	ReasonDeleted         EligibilityReason = "deleted"
	ReasonPolicyMismatch  EligibilityReason = "policy_mismatch"
	ReasonTargetMismatch  EligibilityReason = "target_mismatch"
	ReasonNotReady        EligibilityReason = "not_ready"
	ReasonManagement      EligibilityReason = "management_unavailable"
	ReasonDirectDNS       EligibilityReason = "direct_dns_unavailable"
	ReasonSnapshot        EligibilityReason = "snapshot_not_applied"
	ReasonEmptyPool       EligibilityReason = "empty_pool"
)

// TrustedPlacement must come from a trusted platform lookup linking the persistent
// engine identity to its current pod and actual node. Never populate NodeName from
// engines.node_name, a pod label, or another engine-supplied placement claim.
// The initial Kubernetes contract requires canonical nonzero UUID pod/node UIDs;
// a canonical DNS node name may substitute for NodeUID. Names must identify real
// failure domains within one platform, not virtual aliases for a shared host.
// This value is evidence supplied by an adapter, not an authentication mechanism.
type TrustedPlacement struct {
	EngineID          uuid.UUID
	PodUID            string
	NodeUID, NodeName string
	ObservedAt        time.Time
}

// EligibilityCheck describes an independently timestamped check of the bound pod.
// DirectDNS must probe that backend directly, never a VIP or cached group result.
type EligibilityCheck struct {
	OK         bool
	ObservedAt time.Time
}

// EligibilitySnapshot uses snapshot.ContentDigest's lowercase SHA-256 encoding.
// The digest must cover effective policy AND authoritative configuration.
type EligibilitySnapshot struct {
	Version uint64
	Digest  string
}

// MemberEvidence is a coherent observation for one current pod incarnation.
// ObservedAt dates the authoritative inventory read (including revocation,
// deletion, policy membership and target). All checks and the applied snapshot
// must belong to PodUID; adapters must discard cached evidence after pod changes.
// An applied digest must be resolved from an acknowledged version and trusted
// snapshot store, never inferred from the desired version or Ready alone.
type MemberEvidence struct {
	EngineID, PolicyGroupID      uuid.UUID
	ConnectionSession            uuid.UUID
	ContainerID                  string
	PodUID                       string
	Placement                    TrustedPlacement
	ObservedAt                   time.Time
	Revoked, Deleted             bool
	Ready, Management, DirectDNS EligibilityCheck
	Applied, Target              EligibilitySnapshot
	SnapshotObservedAt           time.Time
	// InventoryValidUntil is overwritten by database validation, never authority
	// from a collector. It bounds certificate/session validity across DB waits.
	InventoryValidUntil time.Time
}

// MemberEligibility contains all applicable denials in deterministic order.
type MemberEligibility struct {
	EngineID uuid.UUID
	Eligible bool
	Reasons  []EligibilityReason
}

// Eligibility is a pure decision in desired member order. Eligible is always an
// explicit pool (empty, never a fallback); Reasons contains group-wide denials.
// A nonempty pool does not assert VIP ownership, datapath transparency or HA.
type Eligibility struct {
	Members  [2]MemberEligibility
	Eligible []uuid.UUID
	Reasons  []EligibilityReason
}

var digestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// EvaluateEligibility performs no IO and does not grant frontend ownership.
// maxAge must be positive; observations at the inclusive age boundary are fresh,
// zero or future timestamps are denied without clock-skew tolerance. The caller
// must reevaluate on changes and expire decisions; they are not durable leases.
// Missing/duplicate/extra members, untrusted placement, or incompatible desired
// configurations deny both members. Individual health/applied-state failures
// remove only that member, allowing a proven healthy partner to keep serving.
func EvaluateEligibility(g Group, evidence []MemberEvidence, now time.Time, maxAge time.Duration) Eligibility {
	r := Eligibility{Eligible: make([]uuid.UUID, 0, 2)}
	for i, id := range g.Members {
		r.Members[i].EngineID = id
	}
	deny := func(reason EligibilityReason) { r.Reasons = append(r.Reasons, reason) }
	if g.Lifecycle != "" && g.Lifecycle != "active" {
		deny(ReasonLifecycle)
	}
	if g.Validate() != nil {
		deny(ReasonInvalidGroup)
	}
	if now.IsZero() || maxAge <= 0 {
		deny(ReasonInvalidClock)
	}
	var members [2]MemberEvidence
	var counts [2]int
	unexpected := len(evidence) != 2
	for _, e := range evidence {
		switch e.EngineID {
		case g.Members[0]:
			members[0] = e
			counts[0]++
		case g.Members[1]:
			members[1] = e
			counts[1]++
		default:
			unexpected = true
		}
	}
	if unexpected || counts != [2]int{1, 1} {
		deny(ReasonExpectedMembers)
	}
	if len(r.Reasons) == 0 {
		placementInvalid := false
		for i, e := range members {
			m := &r.Members[i]
			add := func(reason EligibilityReason) { m.Reasons = append(m.Reasons, reason) }
			p := e.Placement
			if p.EngineID != e.EngineID || !canonicalUID(e.PodUID) || p.PodUID != e.PodUID ||
				(p.NodeUID == "" && p.NodeName == "") ||
				(p.NodeUID != "" && !canonicalUID(p.NodeUID)) ||
				(p.NodeName != "" && !canonicalNodeName(p.NodeName)) {
				add(ReasonInvalidBinding)
				placementInvalid = true
			}
			if !freshEligibility(p.ObservedAt, now, maxAge) {
				add(ReasonBindingTime)
				placementInvalid = true
			}
			if !freshEligibility(e.ObservedAt, now, maxAge) || (!e.InventoryValidUntil.IsZero() && !now.Before(e.InventoryValidUntil)) {
				add(ReasonInventoryTime)
			}
			if e.Revoked {
				add(ReasonRevoked)
			}
			if e.Deleted {
				add(ReasonDeleted)
			}
			if !e.Ready.OK || !freshEligibility(e.Ready.ObservedAt, now, maxAge) {
				add(ReasonNotReady)
			}
			if !e.Management.OK || !freshEligibility(e.Management.ObservedAt, now, maxAge) {
				add(ReasonManagement)
			}
			if !e.DirectDNS.OK || !freshEligibility(e.DirectDNS.ObservedAt, now, maxAge) {
				add(ReasonDirectDNS)
			}
			if !validEligibilitySnapshot(e.Applied) || !validEligibilitySnapshot(e.Target) ||
				e.Applied != e.Target || !freshEligibility(e.SnapshotObservedAt, now, maxAge) {
				add(ReasonSnapshot)
			}
		}
		a, b := members[0], members[1]
		// Placement is a pair invariant even if one member is unhealthy. Unknown
		// placement cannot justify two independently failing physical domains.
		if placementInvalid || a.PodUID == b.PodUID || !distinctNodes(a.Placement, b.Placement) {
			deny(ReasonFailureDomain)
		}
		if a.PolicyGroupID == uuid.Nil || a.PolicyGroupID != b.PolicyGroupID {
			deny(ReasonPolicyMismatch)
		}
		// Equivalent content can have distinct publication versions. Each member
		// must still acknowledge its own exact target version above.
		if !validEligibilitySnapshot(a.Target) || !validEligibilitySnapshot(b.Target) || a.Target.Digest != b.Target.Digest {
			deny(ReasonTargetMismatch)
		}
	}
	for i := range r.Members {
		m := &r.Members[i]
		m.Reasons = append(m.Reasons, r.Reasons...)
		m.Eligible = len(m.Reasons) == 0
		if m.Eligible {
			r.Eligible = append(r.Eligible, m.EngineID)
		}
	}
	if len(r.Eligible) == 0 {
		deny(ReasonEmptyPool)
	}
	return r
}

func freshEligibility(at, now time.Time, maxAge time.Duration) bool {
	return !at.IsZero() && !at.After(now) && now.Sub(at) <= maxAge
}

func canonicalUID(s string) bool {
	id, err := uuid.Parse(s)
	return err == nil && id != uuid.Nil && id.String() == s
}

func canonicalNodeName(s string) bool {
	if len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if !nameRE.MatchString(label) {
			return false
		}
	}
	return true
}

func validEligibilitySnapshot(s EligibilitySnapshot) bool {
	return s.Version > 0 && digestRE.MatchString(s.Digest)
}

func distinctNodes(a, b TrustedPlacement) bool {
	// Conflicting aliases must not hide the same node or a recreated Node object.
	if (a.NodeUID != "" && a.NodeUID == b.NodeUID) || (a.NodeName != "" && a.NodeName == b.NodeName) {
		return false
	}
	return (a.NodeUID != "" && b.NodeUID != "") || (a.NodeName != "" && b.NodeName != "")
}
