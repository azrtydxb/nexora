package control

import (
	"testing"

	"github.com/google/uuid"
)

// Persistence must hold the same lifecycle lock as registration: otherwise an
// old stream can pass unregister's latest check, pause, then clear its replacement.
func TestConnectionPersistenceHoldsLifecycleLock(t *testing.T) {
	h := NewHub(nil, "instance")
	old := newSubscriber(uuid.NewString(), 0)
	current := newSubscriber(old.engineID, 0)
	assertLocked := func() {
		t.Helper()
		if h.connectionMu.TryLock() {
			h.connectionMu.Unlock()
			t.Error("database write is outside the connection lifecycle lock")
		}
		// Database I/O must not prevent broadcasting to existing streams.
		if !h.mu.TryLock() {
			t.Fatal("database write holds the broadcast lock")
		}
		h.mu.Unlock()
	}
	connected := false
	persist := func() error {
		assertLocked()
		connected = true
		return nil
	}
	if err := h.registerConnection(old, persist); err != nil {
		t.Fatal(err)
	}
	if err := h.registerConnection(current, persist); err != nil {
		t.Fatal(err)
	}
	h.unregisterConnection(old, func() {
		t.Error("superseded stream tried to clear its replacement")
		connected = false
	})
	if !connected || h.Connected() != 1 {
		t.Fatal("replacement was disconnected")
	}
	h.unregisterConnection(current, func() {
		assertLocked()
		connected = false
	})
	if connected || h.Connected() != 0 {
		t.Fatal("final stream did not clear connection")
	}
}
