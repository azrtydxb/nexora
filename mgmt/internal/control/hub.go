package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/fleet"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

const (
	hubSafetyInterval = 30 * time.Second
	hubReconnectDelay = time.Second
	// hubBatchWindow and hubBatchMax bound how long and how many notifications are coalesced into
	// one push round (every applied version notifies nexora_rollout).
	hubBatchWindow = 10 * time.Millisecond
	hubBatchMax    = 256
)

// Notification channels the hub listens on. Rollout carries an engine group id; the others an
// engine id.
const (
	ChannelRollout       = "nexora_rollout"
	ChannelEngineUpdated = "nexora_engine_updated"
	ChannelEngineRevoked = "nexora_engine_revoked"
	ChannelEngineRotate  = "nexora_engine_rotate"
)

// Hub tracks the engine streams connected to this instance and pushes each engine its target
// version (fleet.TargetFor) whenever a rollout of its engine group changes.
type Hub struct {
	st         *store.Store
	instanceID string

	// RPZTsig, when set, supplies the RPZ TSIG keys pushed with every broadcast (nil: none are sent).
	RPZTsig *RPZTsig
	// TSIGKeys, when set, supplies the hosted-zone TSIG keys (KeyMaterial) pushed with every broadcast.
	TSIGKeys *TSIGKeys

	mu   sync.Mutex
	subs map[*subscriber]struct{}
	// latest is each engine's most recently registered stream on this instance: a restarted engine
	// can reconnect before the server notices that its previous stream is gone.
	latest map[string]*subscriber
}

// subscriber is one connected engine stream. out holds at most one pending message; a newer
// snapshot replaces an unsent older one.
type subscriber struct {
	engineID string
	id       uuid.UUID
	out      chan *controlv1.ServerMessage
	// control carries certificate messages (RenewCertificate); nothing replaces a queued one.
	control chan *controlv1.ServerMessage
	// revoked is closed once when the engine is revoked; Connect then ends the stream.
	revoked    chan struct{}
	revokeOnce sync.Once
	keys       chan *controlv1.RpzTsigKeys // at most one pending key set; a newer set replaces it
	// keyMaterial holds at most one pending KeyMaterial, owned by this subscriber: the send loop
	// clears its secrets once sent and a replaced pending set is cleared at once.
	keyMaterial chan *controlv1.KeyMaterial
	// results carries UpdateResult replies. Unlike out, nothing replaces a queued result; one that
	// does not fit is dropped and the engine answers SERVFAIL after its own timeout.
	results chan *controlv1.ServerMessage
	// updateSlots bounds the updates of this engine being applied at once.
	updateSlots chan struct{}

	mu                sync.Mutex
	engineGroupID     uuid.UUID // from the last target loaded
	version           uint64    // highest version sent, applied or rejected
	keysDigest        string    // digest of the last key set queued ("" = none)
	keyMaterialDigest string    // digest of the last KeyMaterial queued ("" = none)
	updateTokens      float64
	updateRefilled    time.Time
}

const (
	resultsQueue       = 64
	maxInflightUpdates = 16
	updateRate         = 50.0 // per second, per engine
	updateBurst        = 100.0
)

func newSubscriber(engineID string, applied uint64) *subscriber {
	return &subscriber{engineID: engineID, id: uuid.MustParse(engineID), version: applied, out: make(chan *controlv1.ServerMessage, 1),
		control: make(chan *controlv1.ServerMessage, 4), revoked: make(chan struct{}),
		keys: make(chan *controlv1.RpzTsigKeys, 1), keyMaterial: make(chan *controlv1.KeyMaterial, 1),
		results: make(chan *controlv1.ServerMessage, resultsQueue), updateSlots: make(chan struct{}, maxInflightUpdates),
		updateTokens: updateBurst}
}

// allowUpdate takes one token from the engine's update bucket (updateRate per second, updateBurst deep).
func (s *subscriber) allowUpdate(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.updateRefilled.IsZero() {
		s.updateTokens = min(updateBurst, s.updateTokens+now.Sub(s.updateRefilled).Seconds()*updateRate)
	}
	s.updateRefilled = now
	if s.updateTokens < 1 {
		return false
	}
	s.updateTokens--
	return true
}

// result queues an UpdateResult without blocking.
func (s *subscriber) result(r *controlv1.UpdateResult) {
	select {
	case s.results <- &controlv1.ServerMessage{Msg: &controlv1.ServerMessage_UpdateResult{UpdateResult: r}}:
	default:
		slog.Warn("update result dropped: queue full", "engine", s.engineID, "request", r.RequestId)
	}
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

// renew queues RenewCertificate without blocking (a full queue already holds requests).
func (s *subscriber) renew() {
	select {
	case s.control <- &controlv1.ServerMessage{Msg: &controlv1.ServerMessage_RenewCertificate{RenewCertificate: &controlv1.RenewCertificate{
		Reason: controlv1.CertificateRequest_REASON_ROTATE}}}:
	default:
	}
}

func (s *subscriber) revoke() { s.revokeOnce.Do(func() { close(s.revoked) }) }

func (s *subscriber) group() uuid.UUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.engineGroupID
}

// observe records a version the engine reported as applied or rejected.
func (s *subscriber) observe(version uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version = max(s.version, version)
}

// NewHub creates the hub for one management plane instance.
func NewHub(st *store.Store, instanceID string) *Hub {
	return &Hub{st: st, instanceID: instanceID, subs: map[*subscriber]struct{}{}, latest: map[string]*subscriber{}}
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
	h.latest[s.engineID] = s
}

// unregister removes s and reports whether it was its engine's latest stream on this instance.
func (h *Hub) unregister(s *subscriber) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, s)
	if h.latest[s.engineID] != s {
		return false
	}
	delete(h.latest, s.engineID)
	return true
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
	for _, ch := range []string{ChannelRollout, ChannelEngineUpdated, ChannelEngineRevoked, ChannelEngineRotate} {
		if _, err := conn.Exec(ctx, "listen "+ch); err != nil {
			return store.MapError(err)
		}
	}
	// Anything published while disconnected is picked up here.
	h.pushAll(ctx)
	for {
		wctx, cancel := context.WithTimeout(ctx, hubSafetyInterval)
		n, err := conn.WaitForNotification(wctx)
		cancel()
		switch {
		case err == nil:
			batch := []*pgconn.Notification{n}
			for len(batch) < hubBatchMax {
				bctx, cancel := context.WithTimeout(ctx, hubBatchWindow)
				more, err := conn.WaitForNotification(bctx)
				cancel()
				if err != nil {
					if ctx.Err() == nil && !errors.Is(err, context.DeadlineExceeded) {
						return store.MapError(err)
					}
					break
				}
				batch = append(batch, more)
			}
			h.handle(ctx, batch)
		case ctx.Err() == nil && errors.Is(wctx.Err(), context.DeadlineExceeded):
			h.pushAll(ctx)
		case ctx.Err() != nil:
			return nil
		default:
			return store.MapError(err)
		}
	}
}

// handle acts on a batch of notifications: revocations and rotations at once, then one push per
// engine group or engine named.
func (h *Hub) handle(ctx context.Context, batch []*pgconn.Notification) {
	groups, engines := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
	for _, n := range batch {
		id, err := uuid.Parse(n.Payload)
		if err != nil {
			slog.Warn("notification payload is not a uuid", "channel", n.Channel)
			continue
		}
		switch n.Channel {
		case ChannelRollout:
			groups[id] = true
		case ChannelEngineUpdated:
			engines[id] = true
		case ChannelEngineRevoked:
			for _, s := range h.subscribers(func(s *subscriber) bool { return s.id == id }) {
				s.revoke()
			}
		case ChannelEngineRotate:
			for _, s := range h.subscribers(func(s *subscriber) bool { return s.id == id }) {
				s.renew()
			}
		}
	}
	for g := range groups {
		h.push(ctx, fleet.EngineFilter{EngineGroupID: &g}, func(s *subscriber) bool { return s.group() == g })
	}
	for e := range engines {
		h.push(ctx, fleet.EngineFilter{EngineID: &e}, func(s *subscriber) bool { return s.id == e })
	}
}

func (h *Hub) subscribers(match func(*subscriber) bool) []*subscriber {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []*subscriber
	for s := range h.subs {
		if match(s) {
			out = append(out, s)
		}
	}
	return out
}

// pushAll offers every subscriber its target (the initial and the periodic safety resync).
func (h *Hub) pushAll(ctx context.Context) {
	h.push(ctx, fleet.EngineFilter{}, func(*subscriber) bool { return true })
}

// push loads the targets of the engines matching f and offers them to the matching subscribers.
func (h *Hub) push(ctx context.Context, f fleet.EngineFilter, match func(*subscriber) bool) {
	subs := h.subscribers(match)
	if len(subs) == 0 {
		return
	}
	targets, err := fleet.Targets(ctx, h.st.Pool, f)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("load engine targets", "err", err)
		}
		return
	}
	ks := h.loadKeySets(ctx)
	defer clearKeyMaterial(ks.km)
	for _, s := range subs {
		t, ok := targets[s.id]
		switch {
		case ok && t.Revoked, !ok && f.EngineGroupID == nil:
			// A revoked or deleted engine whose notification this instance missed (listener
			// reconnect): end its stream now rather than never.
			s.revoke()
		case ok:
			offerTarget(s, t, ks)
		}
	}
}

// keySets are the complete key sets filtered per engine; an ok flag is false when the set could
// not be loaded (nothing of it is offered then). km must be cleared by the loader's caller.
type keySets struct {
	rpz   *controlv1.RpzTsigKeys
	rpzOK bool
	km    *controlv1.KeyMaterial
	zoned map[string]bool
	kmOK  bool
}

func (h *Hub) loadKeySets(ctx context.Context) keySets {
	var ks keySets
	ks.rpz, _, ks.rpzOK = h.loadKeys(ctx)
	ks.km, ks.zoned, ks.kmOK = h.loadKeyMaterial(ctx)
	return ks
}

// offerTarget offers s its target: the RPZ and hosted-zone TSIG keys it may hold (FilterRPZKeys,
// FilterKeyMaterial), then the snapshot.
func offerTarget(s *subscriber, t fleet.Target, ks keySets) {
	s.mu.Lock()
	s.engineGroupID = t.EngineGroupID
	s.mu.Unlock()
	if t.Snapshot == nil {
		return
	}
	if ks.rpzOK {
		k := FilterRPZKeys(t.Snapshot, ks.rpz)
		s.offerKeys(k, keySetDigest(k, len(k.Keys)))
	}
	if ks.kmOK {
		m := FilterKeyMaterial(t.Snapshot, ks.km, ks.zoned)
		s.offerKeyMaterial(m, keySetDigest(m, len(m.TsigKeys)))
	}
	s.offer(t.Version, t.Snapshot)
}

// keySetDigest is the lowercase hex SHA-256 of the deterministic encoding of a key set of n keys
// ("" for an empty set, so an engine that never had keys is not sent an empty set).
func keySetDigest(m proto.Message, n int) string {
	if n == 0 {
		return ""
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		return "" // key messages always encode; "" would only suppress a resend
	}
	sum := sha256.Sum256(raw)
	clear(raw)
	return hex.EncodeToString(sum[:])
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

// loadKeyMaterial loads the hosted-zone TSIG key set and the names zones use; ok is false without
// a loader or on error (logged, never the keys). The caller clears the returned set.
func (h *Hub) loadKeyMaterial(ctx context.Context) (*controlv1.KeyMaterial, map[string]bool, bool) {
	if h.TSIGKeys == nil {
		return nil, nil, false
	}
	km, _, err := h.TSIGKeys.Load(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("load tsig key material", "err", err)
		}
		return nil, nil, false
	}
	zoned, err := h.TSIGKeys.ZoneKeyNames(ctx)
	if err != nil {
		clearKeyMaterial(km)
		if ctx.Err() == nil {
			slog.Warn("load zone tsig key names", "err", err)
		}
		return nil, nil, false
	}
	return km, zoned, true
}
