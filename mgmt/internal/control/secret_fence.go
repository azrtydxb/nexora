package control

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Delivery hides prepared material until the database positively acknowledges COMMIT.
// Receivers must call Await outside all queue/database locks before transport Send.
// A nil barrier is reserved for ordinary, nonsecret control messages.
type Delivery[T any] struct {
	value   T
	barrier *commitBarrier
}

type commitBarrier struct {
	done      chan struct{}
	once      sync.Once
	committed bool
}

func (b *commitBarrier) resolve(ok bool) {
	b.once.Do(func() { b.committed = ok; close(b.done) })
}

func (d Delivery[T]) Await(ctx context.Context) (T, bool) {
	var zero T
	if ctx.Err() != nil {
		return zero, false
	}
	if d.barrier != nil {
		select {
		case <-ctx.Done():
			return zero, false
		case <-d.barrier.done:
		}
		if !d.barrier.committed || ctx.Err() != nil {
			return zero, false
		}
	}
	return d.value, true
}

// enqueueSecret prepares a bounded, nonblocking local offer under persisted
// authorization locks. Only an acknowledged successful commit opens its barrier.
// Callers hold their queue mutex; enqueue does only bounded local work, never
// network Send, pool acquisition, or a blocking channel operation. Loading and
// decryption precede this call. Successful offers were queued/inflight before the
// locks were released; their delivery may still follow a later takeover.
// Never retry this transaction: callers may freshly authorize a later reoffer.
func enqueueSecret(ctx context.Context, st *store.Store, engineID, sessionID uuid.UUID, serial string, enqueue func(*commitBarrier)) error {
	if st == nil || st.Pool == nil {
		return errors.New("secret fence requires database")
	}
	b := &commitBarrier{done: make(chan struct{})}
	defer b.resolve(false)
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	// Do not retry a transaction containing an in-memory side effect.
	return func() error {
		tx, err := st.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			return err
		}
		defer func() {
			rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}()
		var id string
		if err := tx.QueryRow(ctx, `select id::text from engines
   where id=$1 and connection_session=$2 and revoked_at is null and deleted_at is null
   for no key update`, engineID, sessionID).Scan(&id); err != nil {
			return err
		}
		// Lock separately, in engine-then-certificate order like connection claims
		// and RevokeEngine. Also covers direct certificate revocation.
		if err := tx.QueryRow(ctx, `select serial from engine_certificates
   where engine_id=$1 and serial=lower($2) and revoked_at is null
   for share`, engineID, serial).Scan(&id); err != nil {
			return err
		}
		enqueue(b)
		return commitSecret(ctx, tx, b)
	}()
}

// Treat every commit error, including an unknown outcome, as a discard.
func commitSecret(ctx context.Context, tx pgx.Tx, b *commitBarrier) error {
	err := tx.Commit(ctx)
	b.resolve(err == nil)
	return err
}
