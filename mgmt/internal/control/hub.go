package control

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

const (
	hubSafetyInterval = 30 * time.Second
	hubReconnectDelay = time.Second
)

// Hub tracks the engine streams connected to this instance and pushes every new config version
// (announced by pg_notify on snapshot.NotifyChannel) to them.
type Hub struct {
	st         *store.Store
	instanceID string

	// RPZTsig, when set, supplies the RPZ TSIG keys pushed with every broadcast (nil: none are sent).
	RPZTsig *RPZTsig
	// TSIGKeys, when set, supplies the hosted-zone TSIG keys (KeyMaterial) pushed with every broadcast.
	TSIGKeys *TSIGKeys

	mu   sync.Mutex
	subs map[*subscriber]struct{}
}

// subscriber is one connected engine stream. out holds at most one pending message; a newer
// snapshot replaces an unsent older one.
type subscriber struct {
	engineID string
	out      chan *controlv1.ServerMessage
	keys     chan *controlv1.RpzTsigKeys // at most one pending key set; a newer set replaces it
	// keyMaterial holds at most one pending KeyMaterial, owned by this subscriber: the send loop
	// clears its secrets once sent and a replaced pending set is cleared at once.
	keyMaterial chan *controlv1.KeyMaterial

	mu                sync.Mutex
	version           uint64 // highest version sent, applied or rejected
	keysDigest        string // digest of the last key set queued ("" = none)
	keyMaterialDigest string // digest of the last KeyMaterial queued ("" = none)
}

func newSubscriber(engineID string, applied uint64) *subscriber {
	return &subscriber{engineID: engineID, version: applied, out: make(chan *controlv1.ServerMessage, 1),
		keys: make(chan *controlv1.RpzTsigKeys, 1), keyMaterial: make(chan *controlv1.KeyMaterial, 1)}
}

// offerKeyMaterial queues a private copy of km unless this engine was already given the set with
// digest. The caller keeps ownership of km.
func (s *subscriber) offerKeyMaterial(km *controlv1.KeyMaterial, digest string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if digest == s.keyMaterialDigest {
		return
	}
	s.keyMaterialDigest = digest
	select {
	case old := <-s.keyMaterial:
		clearKeyMaterial(old)
	default:
	}
	s.keyMaterial <- proto.Clone(km).(*controlv1.KeyMaterial)
}

// offerKeys queues k unless this engine was already given the key set with digest.
func (s *subscriber) offerKeys(k *controlv1.RpzTsigKeys, digest string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if digest == s.keysDigest {
		return
	}
	s.keysDigest = digest
	select {
	case <-s.keys:
	default:
	}
	s.keys <- k
}

// offer queues snap when it is newer than anything this engine has seen.
func (s *subscriber) offer(version uint64, snap *controlv1.ConfigSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if version <= s.version {
		return
	}
	s.version = version
	s.replace(&controlv1.ServerMessage{Msg: &controlv1.ServerMessage_Snapshot{Snapshot: snap}})
}

// send queues msg unconditionally (latest wins).
func (s *subscriber) send(msg *controlv1.ServerMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replace(msg)
}

func (s *subscriber) replace(msg *controlv1.ServerMessage) {
	select {
	case <-s.out:
	default:
	}
	s.out <- msg
}

// observe records a version the engine reported as applied or rejected.
func (s *subscriber) observe(version uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version = max(s.version, version)
}

// NewHub creates the hub for one management plane instance.
func NewHub(st *store.Store, instanceID string) *Hub {
	return &Hub{st: st, instanceID: instanceID, subs: map[*subscriber]struct{}{}}
}

// Connected is the number of engine streams on this instance.
func (h *Hub) Connected() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func (h *Hub) register(s *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subs[s] = struct{}{}
}

func (h *Hub) unregister(s *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, s)
}

// Run listens for new versions until ctx ends, reconnecting after 1 s on connection loss.
func (h *Hub) Run(ctx context.Context) error {
	for {
		err := h.listen(ctx)
		if ctx.Err() != nil {
			return nil
		}
		slog.Warn("config listener disconnected", "instance", h.instanceID, "err", err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(hubReconnectDelay):
		}
	}
}

func (h *Hub) listen(ctx context.Context) error {
	// A dedicated connection outside the pool: LISTEN holds it for the hub's lifetime, which must
	// not block closing the pool.
	conn, err := pgx.ConnectConfig(ctx, h.st.Pool.Config().ConnConfig)
	if err != nil {
		return store.MapError(err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, "listen "+snapshot.NotifyChannel); err != nil {
		return store.MapError(err)
	}
	// Anything published while disconnected is picked up here.
	h.broadcast(ctx)
	for {
		wctx, cancel := context.WithTimeout(ctx, hubSafetyInterval)
		_, err := conn.WaitForNotification(wctx)
		cancel()
		switch {
		case err == nil, ctx.Err() == nil && errors.Is(wctx.Err(), context.DeadlineExceeded):
			h.broadcast(ctx)
		case ctx.Err() != nil:
			return nil
		default:
			return store.MapError(err)
		}
	}
}

// broadcast loads the latest snapshot and offers it to every subscriber.
func (h *Hub) broadcast(ctx context.Context) {
	h.mu.Lock()
	subs := make([]*subscriber, 0, len(h.subs))
	for s := range h.subs {
		subs = append(subs, s)
	}
	h.mu.Unlock()
	if len(subs) == 0 {
		return
	}
	version, snap, err := snapshot.Latest(ctx, h.st.Pool)
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, store.ErrNotFound) {
			slog.Warn("load latest config version", "err", err)
		}
		return
	}
	for _, s := range subs {
		s.offer(version, snap)
	}
	if keys, digest, ok := h.loadKeys(ctx); ok {
		for _, s := range subs {
			s.offerKeys(keys, digest)
		}
	}
	if km, digest, ok := h.loadKeyMaterial(ctx); ok {
		for _, s := range subs {
			s.offerKeyMaterial(km, digest)
		}
		clearKeyMaterial(km)
	}
}

// loadKeys loads the RPZ TSIG key set; ok is false without a loader or on error (logged, never the keys).
func (h *Hub) loadKeys(ctx context.Context) (*controlv1.RpzTsigKeys, string, bool) {
	if h.RPZTsig == nil {
		return nil, "", false
	}
	keys, digest, err := h.RPZTsig.Load(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("load rpz tsig keys", "err", err)
		}
		return nil, "", false
	}
	return keys, digest, true
}

// loadKeyMaterial loads the hosted-zone TSIG key set; ok is false without a loader or on error
// (logged, never the keys). The caller clears the returned set.
func (h *Hub) loadKeyMaterial(ctx context.Context) (*controlv1.KeyMaterial, string, bool) {
	if h.TSIGKeys == nil {
		return nil, "", false
	}
	km, digest, err := h.TSIGKeys.Load(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("load tsig key material", "err", err)
		}
		return nil, "", false
	}
	return km, digest, true
}
