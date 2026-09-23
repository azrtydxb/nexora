package failover_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/piwi3910/nexora/mgmt/internal/failover"
)

func TestPublicationBackgroundStageBounds(t *testing.T) {
	for _, stage := range []string{"invalidation acquisition", "publication acquisition", "invalidation lock", "publication lock"} {
		t.Run(stage, func(t *testing.T) {
			f := newLifecycleFixture(t)
			f.prepareEvidence()
			cfg := f.st.Pool.Config()
			cfg.MaxConns = 1
			pool, err := pgxpool.NewWithConfig(f.ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			var held *pgxpool.Conn
			var locked pgx.Tx
			hold := func() {
				if stage == "invalidation acquisition" || stage == "publication acquisition" {
					held, err = pool.Acquire(f.ctx)
				} else {
					locked, err = f.st.Pool.Begin(f.ctx)
					if err == nil {
						_, err = locked.Exec(f.ctx, `select id from public.failover_groups where id=$1 for update`, f.g.ID)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			release := func() {
				if held != nil {
					held.Release()
					held = nil
				}
				if locked != nil {
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					_ = locked.Rollback(ctx)
					locked = nil
				}
			}
			defer release()
			if stage == "invalidation acquisition" || stage == "invalidation lock" {
				hold()
			}
			calls := 0
			before := time.Now()
			result, err := failover.CollectAndPublish(context.Background(), pool, f.token, 1, 150*time.Millisecond, collectFunc(func(context.Context, failover.Group, time.Duration) ([]failover.MemberEvidence, error) {
				calls++
				hold()
				return f.evidence, nil
			}))
			if err == nil || len(result.Eligible) != 0 || time.Since(before) > 2*time.Second {
				t.Fatalf("stage escaped bound: %+v %v elapsed=%s", result, err, time.Since(before))
			}
			want := 1
			if stage == "invalidation acquisition" || stage == "invalidation lock" {
				want = 0
			}
			if calls != want {
				t.Fatalf("collection calls=%d want %d", calls, want)
			}
			release()
			reuse, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err = pool.Ping(reuse); err != nil {
				t.Fatalf("single slot not reusable: %v", err)
			}
		})
	}
}

// Delays delivery to the caller after PostgreSQL positively acknowledges COMMIT.
// This catches success paths whose context watcher has already been disarmed.
type publicationCommitDelay struct {
	commits atomic.Int32
	delay   time.Duration
	armed   atomic.Bool
}

func (t *publicationCommitDelay) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, publicationSQLKey{}, d.SQL)
}

type publicationSQLKey struct{}

func (t *publicationCommitDelay) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryEndData) {
	if ctx.Value(publicationSQLKey{}) == "commit" && d.Err == nil && t.armed.Load() && t.commits.Add(1) == 2 {
		time.Sleep(t.delay)
	}
}
func TestPublicationNoLateCommitEligibility(t *testing.T) {
	for _, kind := range []string{"stage deadline", "certificate freshness"} {
		t.Run(kind, func(t *testing.T) {
			f := newLifecycleFixture(t)
			f.prepareEvidence()
			tracer := &publicationCommitDelay{delay: 300 * time.Millisecond}
			cfg := f.st.Pool.Config()
			cfg.MaxConns = 1
			cfg.ConnConfig.Tracer = tracer
			pool, err := pgxpool.NewWithConfig(f.ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			if err = pool.Ping(f.ctx); err != nil {
				t.Fatal(err)
			}
			age := 150 * time.Millisecond
			if kind == "certificate freshness" {
				age = 5 * time.Second
				f.exec(`update engine_certificates set not_after=clock_timestamp()+interval '200 milliseconds' where engine_id in ($1,$2)`, f.g.Members[0], f.g.Members[1])
			}
			tracer.armed.Store(true)
			result, err := failover.CollectAndPublish(context.Background(), pool, f.token, 1, age, collectFunc(func(context.Context, failover.Group, time.Duration) ([]failover.MemberEvidence, error) {
				return f.evidence, nil
			}))
			if tracer.commits.Load() != 2 {
				t.Fatalf("did not reach second commit: %v", err)
			}
			if len(result.Eligible) != 0 {
				t.Fatalf("late eligibility: %+v", result)
			}
			if kind == "stage deadline" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("late acknowledgement: %v", err)
			}
			if kind == "certificate freshness" && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPublicationLateCollectorSuccessFailsClosed(t *testing.T) {
	f := newLifecycleFixture(t)
	f.prepareEvidence()
	result, err := failover.CollectAndPublish(context.Background(), f.st.Pool, f.token, 1, 100*time.Millisecond, collectFunc(func(ctx context.Context, _ failover.Group, _ time.Duration) ([]failover.MemberEvidence, error) {
		<-ctx.Done()
		return f.evidence, nil
	}))
	if !errors.Is(err, context.DeadlineExceeded) || len(result.Eligible) != 0 || len(f.read().Eligible) != 0 {
		t.Fatalf("late collector success: %+v %v", result, err)
	}
}
