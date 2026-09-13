package dnssec

import (
	"slices"
	"time"
)

// KeyState is one key as the rollover state machine sees it. Role is ksk|zsk; State is
// published|active|retired|removed; DSState (KSKs) is none|pending|seen.
type KeyState struct {
	ID, Role, State, DSState          string
	PublishedAt                       time.Time
	ActivatedAt, RetiredAt, RemovedAt *time.Time
	DSSeenAt                          *time.Time
}

// Policy holds the timings of a zone's rollovers. ZSKLifetime 0 means manual ZSK rollovers only.
type Policy struct {
	DNSKEYTTL, MaxZoneTTL, Propagation, ParentDSTTL time.Duration
	ZSKLifetime                                     time.Duration
}

// Actions are the key operations Advance asks the caller to perform.
type Actions struct{ CreateZSK bool }

// Advance applies every rollover transition due at now to a copy of keys and returns it, the
// actions due, and the earliest future instant a transition is pending (zero when none is):
//   - ZSK pre-publish: a published ZSK becomes active once DNSKEY TTL + propagation passed since it
//     was published while another ZSK is active, which retires that ZSK; a retired ZSK is removed
//     once max zone TTL + propagation passed since it retired.
//   - KSK double signature: with two active KSKs, once the newer one's DS was seen at the parent,
//     the older is removed after the parent DS TTL + propagation.
//   - automatic ZSK rollover: with one active ZSK, none published and a lifetime set, CreateZSK once
//     the ZSK is ZSKLifetime old.
func Advance(keys []KeyState, p Policy, now time.Time) (out []KeyState, act Actions, next time.Time) {
	out = slices.Clone(keys)
	pending := func(at time.Time) bool {
		if !now.Before(at) {
			return true
		}
		if next.IsZero() || at.Before(next) {
			next = at
		}
		return false
	}
	at := func(t time.Time) *time.Time { return &t }
	idx := func(role, state string) []int {
		var r []int
		for i, k := range out {
			if k.Role == role && k.State == state {
				r = append(r, i)
			}
		}
		return r
	}

	activeZSK := idx("zsk", "active")
	for _, i := range idx("zsk", "published") {
		if len(activeZSK) == 0 || !pending(out[i].PublishedAt.Add(p.DNSKEYTTL+p.Propagation)) {
			continue
		}
		out[i].State, out[i].ActivatedAt = "active", at(now)
		for _, j := range activeZSK {
			out[j].State, out[j].RetiredAt = "retired", at(now)
		}
		activeZSK = []int{i}
	}
	for _, i := range idx("zsk", "retired") {
		if out[i].RetiredAt != nil && pending(out[i].RetiredAt.Add(p.MaxZoneTTL+p.Propagation)) {
			out[i].State, out[i].RemovedAt = "removed", at(now)
		}
	}

	if ksks := idx("ksk", "active"); len(ksks) == 2 {
		older, newer := ksks[0], ksks[1]
		if activatedAt(out[newer]).Before(activatedAt(out[older])) {
			older, newer = newer, older
		}
		if n := out[newer]; n.DSState == "seen" && n.DSSeenAt != nil && pending(n.DSSeenAt.Add(p.ParentDSTTL+p.Propagation)) {
			out[older].State, out[older].RemovedAt = "removed", at(now)
		}
	}

	if p.ZSKLifetime > 0 && len(activeZSK) == 1 && len(idx("zsk", "published")) == 0 {
		if z := out[activeZSK[0]]; z.ActivatedAt != nil && pending(z.ActivatedAt.Add(p.ZSKLifetime)) {
			act.CreateZSK = true
		}
	}
	return out, act, next
}

func activatedAt(k KeyState) time.Time {
	if k.ActivatedAt != nil {
		return *k.ActivatedAt
	}
	return k.PublishedAt
}
