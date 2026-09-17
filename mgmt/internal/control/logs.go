package control

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Engine log routing channels. ChannelEngineLogs carries a logNote (JSON); ChannelEngineLogsDone
// the id of a request whose reply is in engine_log_replies.
const (
	ChannelEngineLogs     = "nexora_engine_logs"
	ChannelEngineLogsDone = "nexora_engine_logs_done"
)

var (
	ErrEngineDisconnected = errors.New("engine is not connected")
	ErrEngineTimeout      = errors.New("engine did not answer in time")
)

// logWait bounds how long Read waits for an engine's reply.
const logWait = 5 * time.Second

// logNote is the ChannelEngineLogs payload.
type logNote struct {
	RequestID string    `json:"request_id"`
	EngineID  uuid.UUID `json:"engine_id"`
	AfterSeq  uint64    `json:"after_seq"`
	MinLevel  int32     `json:"min_level"`
	Limit     uint32    `json:"limit"`
	Contains  string    `json:"contains"`
}

// LogBroker reads engine log ring buffers through whichever instance holds the engine's stream:
// requests go out as notifications; replies use engine_log_replies and a
// post-commit local wake or ChannelEngineLogsDone.
type LogBroker struct {
	st *store.Store

	mu sync.Mutex
	// waiters holds this instance's pending requests; a nil batch means "the reply is in the table".
	waiters map[string]chan *controlv1.LogBatch
}

// NewLogBroker creates the broker of one instance.
func NewLogBroker(st *store.Store) *LogBroker {
	return &LogBroker{st: st, waiters: map[string]chan *controlv1.LogBatch{}}
}

// Read asks the engine for the log lines matching req (its request id is assigned here) and waits
// up to 5 s for the reply. A missing or deleted engine gives store.ErrNotFound, one without a live
// stream ErrEngineDisconnected, one that does not answer ErrEngineTimeout.
func (b *LogBroker) Read(ctx context.Context, engineID uuid.UUID, req *controlv1.LogRequest) (*controlv1.LogBatch, error) {
	var live bool
	if err := b.st.Pool.QueryRow(ctx, `select e.connected_instance is not null and coalesce(i.heartbeat_at > now() - interval '15 seconds', false)
		from engines e left join instances i on i.id = e.connected_instance
		where e.id = $1 and e.deleted_at is null`, engineID).Scan(&live); err != nil {
		return nil, store.MapError(err)
	}
	if !live {
		return nil, ErrEngineDisconnected
	}
	id := uuid.NewString()
	ch := make(chan *controlv1.LogBatch, 1)
	b.mu.Lock()
	b.waiters[id] = ch
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.waiters, id)
		b.mu.Unlock()
	}()
	payload, err := json.Marshal(logNote{RequestID: id, EngineID: engineID, AfterSeq: req.AfterSeq, MinLevel: int32(req.MinLevel), Limit: req.Limit, Contains: req.Contains})
	if err != nil {
		return nil, err
	}
	if _, err := b.st.Pool.Exec(ctx, "select pg_notify($1, $2)", ChannelEngineLogs, string(payload)); err != nil {
		return nil, store.MapError(err)
	}
	timer := time.NewTimer(logWait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, ErrEngineTimeout
	case batch := <-ch:
		if batch != nil {
			return batch, nil
		}
	}
	var raw []byte
	err = b.st.Pool.QueryRow(ctx, "delete from engine_log_replies where request_id = $1 returning batch", id).Scan(&raw)
	if err != nil {
		if errors.Is(store.MapError(err), store.ErrNotFound) {
			return nil, ErrEngineTimeout
		}
		return nil, store.MapError(err)
	}
	batch := &controlv1.LogBatch{}
	if err := proto.Unmarshal(raw, batch); err != nil {
		return nil, err
	}
	return batch, nil
}

// DeliverTx persists a reply and its notification atomically in the stream's
// ownership transaction. Even local waiters read committed data from the table.
func (b *LogBroker) DeliverTx(ctx context.Context, tx pgx.Tx, batch *controlv1.LogBatch) error {
	if _, err := uuid.Parse(batch.RequestId); err != nil {
		return nil
	}
	raw, err := proto.Marshal(batch)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "insert into engine_log_replies(request_id, batch) values ($1, $2) on conflict do nothing", batch.RequestId, raw); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "delete from engine_log_replies where created_at < now() - interval '60 seconds'"); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "select pg_notify($1, $2)", ChannelEngineLogsDone, batch.RequestId)
	return err
}

// Done wakes a request of this instance whose reply was stored in engine_log_replies.
func (b *LogBroker) Done(requestID string) { b.wake(requestID, nil) }

// wake passes batch to the waiter of requestID and reports whether this instance has one.
func (b *LogBroker) wake(requestID string, batch *controlv1.LogBatch) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch, ok := b.waiters[requestID]
	if ok {
		select {
		case ch <- batch:
		default: // a reply already arrived
		}
	}
	return ok
}

// maxPendingLogRequests bounds the log request ids a subscriber remembers for matching replies.
const maxPendingLogRequests = 64

// offerLog queues a LogRequest for the engine without blocking and remembers its id, so only a
// reply to a request actually sent is accepted (takeLogRequest).
func (s *subscriber) offerLog(req *controlv1.LogRequest, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, at := range s.logRequests {
		if now.Sub(at) > 2*logWait {
			delete(s.logRequests, id)
		}
	}
	if len(s.logRequests) >= maxPendingLogRequests {
		return
	}
	select {
	case s.logs <- req:
		s.logRequests[req.RequestId] = now
	default: // the queue is full; the requester times out
	}
}

// takeLogRequest reports whether id names a pending request of this subscriber and forgets it.
func (s *subscriber) takeLogRequest(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.logRequests[id]
	delete(s.logRequests, id)
	return ok
}

// offerLogNote offers the request in a ChannelEngineLogs payload to this instance's streams of
// the engine it names.
func (h *Hub) offerLogNote(payload string) {
	var n logNote
	if err := json.Unmarshal([]byte(payload), &n); err != nil {
		slog.Warn("engine log request payload is not valid", "err", err)
		return
	}
	req := &controlv1.LogRequest{RequestId: n.RequestID, AfterSeq: n.AfterSeq, MinLevel: controlv1.LogLevel(n.MinLevel), Limit: n.Limit, Contains: n.Contains}
	now := time.Now()
	for _, s := range h.subscribers(func(s *subscriber) bool { return s.id == n.EngineID }) {
		s.offerLog(req, now)
	}
}

// SetLogBroker sets the broker woken when a reply stored by another instance is ready.
func (h *Hub) SetLogBroker(b *LogBroker) { h.logs = b }

// SetLogBroker sets the broker that receives the engines' LogBatch replies (nil: they are dropped).
func (s *Server) SetLogBroker(b *LogBroker) { s.logs = b }
