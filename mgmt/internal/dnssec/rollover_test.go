package dnssec

import (
	"testing"
	"time"
)

func ptr(t time.Time) *time.Time { return &t }

func stateOf(keys []KeyState, id string) string {
	for _, k := range keys {
		if k.ID == id {
			return k.State
		}
	}
	return "missing"
}

var policy = Policy{DNSKEYTTL: time.Hour, MaxZoneTTL: 24 * time.Hour, Propagation: time.Hour, ParentDSTTL: 24 * time.Hour, ZSKLifetime: 90 * 24 * time.Hour}

func TestZSKPrePublishTimeline(t *testing.T) {
	start := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	keys := []KeyState{
		{ID: "ksk1", Role: "ksk", State: "active", DSState: "seen", ActivatedAt: ptr(start.Add(-400 * 24 * time.Hour))},
		{ID: "zsk1", Role: "zsk", State: "active", ActivatedAt: ptr(start.Add(-10 * 24 * time.Hour))},
		{ID: "zsk2", Role: "zsk", State: "published", PublishedAt: start},
	}
	out, _, next := Advance(keys, policy, start.Add(119*time.Minute))
	if stateOf(out, "zsk2") != "published" || stateOf(out, "zsk1") != "active" || !next.Equal(start.Add(2*time.Hour)) {
		t.Fatalf("before DNSKEY TTL + propagation: %v next=%v", out, next)
	}
	out, _, next = Advance(keys, policy, start.Add(2*time.Hour))
	if stateOf(out, "zsk2") != "active" || stateOf(out, "zsk1") != "retired" || !next.Equal(start.Add(2*time.Hour+25*time.Hour)) {
		t.Fatalf("activation: %v next=%v", out, next)
	}
	out, _, _ = Advance(out, policy, start.Add(27*time.Hour-time.Second))
	if stateOf(out, "zsk1") != "retired" {
		t.Fatal("old ZSK removed before max zone TTL + propagation")
	}
	out, _, _ = Advance(out, policy, start.Add(27*time.Hour))
	if stateOf(out, "zsk1") != "removed" || stateOf(out, "ksk1") != "active" {
		t.Fatalf("removal: %v", out)
	}
}

func TestKSKDoubleSignatureTimeline(t *testing.T) {
	start := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	keys := []KeyState{
		{ID: "ksk1", Role: "ksk", State: "active", DSState: "seen", ActivatedAt: ptr(start.Add(-400 * 24 * time.Hour))},
		{ID: "ksk2", Role: "ksk", State: "active", DSState: "pending", ActivatedAt: ptr(start)},
		{ID: "zsk1", Role: "zsk", State: "active", ActivatedAt: ptr(start.Add(-time.Hour))},
	}
	out, _, _ := Advance(keys, policy, start.Add(30*24*time.Hour))
	if stateOf(out, "ksk1") != "active" || stateOf(out, "ksk2") != "active" {
		t.Fatal("both KSKs sign until the parent DS is confirmed")
	}
	seen := start.Add(31 * 24 * time.Hour)
	keys[1].DSState, keys[1].DSSeenAt = "seen", ptr(seen)
	out, _, next := Advance(keys, policy, seen.Add(time.Hour))
	if stateOf(out, "ksk1") != "active" || !next.Equal(seen.Add(25*time.Hour)) {
		t.Fatalf("waiting for parent DS TTL + propagation: %v next=%v", out, next)
	}
	out, _, _ = Advance(keys, policy, seen.Add(25*time.Hour))
	if stateOf(out, "ksk1") != "removed" || stateOf(out, "ksk2") != "active" {
		t.Fatalf("completion: %v", out)
	}
}

func TestAutomaticZSKRolloverAtLifetime(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	keys := []KeyState{
		{ID: "ksk1", Role: "ksk", State: "active", DSState: "seen", ActivatedAt: ptr(now.Add(-400 * 24 * time.Hour))},
		{ID: "zsk1", Role: "zsk", State: "active", ActivatedAt: ptr(now.Add(-89 * 24 * time.Hour))},
	}
	_, act, next := Advance(keys, policy, now)
	if act.CreateZSK || !next.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("day 89: create=%v next=%v", act.CreateZSK, next)
	}
	_, act, _ = Advance(keys, policy, now.Add(24*time.Hour))
	if !act.CreateZSK {
		t.Fatal("ZSK lifetime reached but no rollover requested")
	}
	manual := policy
	manual.ZSKLifetime = 0
	if _, act, _ = Advance(keys, manual, now.Add(365*24*time.Hour)); act.CreateZSK {
		t.Fatal("lifetime 0 means manual rollovers only")
	}
}
