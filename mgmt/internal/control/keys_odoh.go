package control

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
)

// ODoHKeyLoader loads the complete ODoH key set with its digest ("" for an empty set). odoh.Keys
// implements it.
type ODoHKeyLoader interface {
	Load(ctx context.Context) (*controlv1.OdohKeys, string, error)
}

// offerOdohKeys serializes authorization and nonblocking enqueue with persisted
// ownership claims and revocation. Load/decryption runs before this transaction;
// transport Send runs independently afterward. Already queued material is not
// retracted: this fences new ODoH offers, not arbitrary control-stream sends.
func (h *Hub) offerOdohKeys(ctx context.Context, s *subscriber, k *controlv1.OdohKeys, digest string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if digest == s.odohKeysDigest {
		return
	}
	queued := false
	err := enqueueSecret(ctx, h.st, s.id, s.sessionID, s.certificateSerial, func(barrier *commitBarrier) {
		select {
		case s.control <- Delivery[*controlv1.ServerMessage]{barrier: barrier, value: &controlv1.ServerMessage{Msg: &controlv1.ServerMessage_OdohKeys{OdohKeys: k}}}:
			queued = true
		default:
		}
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("fence odoh keys", "engine", s.engineID, "err", err)
		}
		return
	}
	if queued {
		s.odohKeysDigest = digest
	} else {
		slog.Warn("odoh key set not queued: control queue full", "engine", s.engineID)
	}
}

// offerOdohKeysToAll loads the ODoH key set once and offers it to every subscriber.
func (h *Hub) offerOdohKeysToAll(ctx context.Context) {
	subs := h.subscribers(func(*subscriber) bool { return true })
	if len(subs) == 0 {
		return
	}
	keys, digest, ok := h.loadOdohKeys(ctx)
	if !ok {
		return
	}
	for _, s := range subs {
		h.offerOdohKeys(ctx, s, keys, digest)
	}
}

// loadOdohKeys loads the ODoH key set; ok is false without a loader or on error (logged, never the seeds).
func (h *Hub) loadOdohKeys(ctx context.Context) (*controlv1.OdohKeys, string, bool) {
	if h.ODoH == nil {
		return nil, "", false
	}
	keys, digest, err := h.ODoH.Load(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("load odoh keys", "err", err)
		}
		return nil, "", false
	}
	return keys, digest, true
}
