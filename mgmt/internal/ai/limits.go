package ai

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// tokenBucket starts at most perMinute calls per minute, refilled continuously.
type tokenBucket struct {
	mu       sync.Mutex
	tokens   float64
	capacity float64
	perSec   float64
	last     time.Time
}

func newTokenBucket(perMinute int, now time.Time) *tokenBucket {
	return &tokenBucket{tokens: float64(perMinute), capacity: float64(perMinute), perSec: float64(perMinute) / 60, last: now}
}

// take consumes a token, or reports how long until one is available.
func (b *tokenBucket) take(now time.Time) (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if now.After(b.last) {
		b.tokens = min(b.capacity, b.tokens+now.Sub(b.last).Seconds()*b.perSec)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / b.perSec * float64(time.Second))
}

// Budget is today's (UTC) fleet-wide token use.
type Budget struct {
	Day                                            time.Time
	LimitTokens, UsedTokens, BackgroundLimitTokens int64
}

// Budget sums today's input and output tokens over every instance sharing the database.
func (s *Service) Budget(ctx context.Context) (Budget, error) {
	day := s.day()
	b := Budget{Day: day, LimitTokens: s.cfg.DailyTokenBudget,
		BackgroundLimitTokens: s.cfg.DailyTokenBudget * int64(s.cfg.BackgroundBudgetPercent) / 100}
	err := s.st.Pool.QueryRow(ctx, `select coalesce(sum(input_tokens+output_tokens),0)::bigint from ai_usage where day = $1`, day).Scan(&b.UsedTokens)
	if err != nil {
		return Budget{}, fmt.Errorf("ai budget: %w", err)
	}
	return b, nil
}

func (s *Service) day() time.Time {
	y, m, d := s.now().UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// acquire checks the budget, then waits for a rate token and a concurrency slot: Interactive at most
// slotWait (ErrBusy), Background until ctx ends. One Generate, including its validation attempts,
// holds one token and one slot.
func (s *Service) acquire(ctx context.Context, p Priority) (release func(), err error) {
	b, err := s.Budget(ctx)
	if err != nil {
		return nil, err
	}
	if limit := b.LimitTokens; b.UsedTokens >= limit || (p == Background && b.UsedTokens >= b.BackgroundLimitTokens) {
		return nil, ErrBudgetExhausted
	}
	wait := ctx
	if p == Interactive {
		var cancel context.CancelFunc
		wait, cancel = context.WithTimeout(ctx, s.slotWait)
		defer cancel()
	}
	start := time.Now()
	defer func() { QueueWait.Observe(time.Since(start).Seconds()) }()
	busy := func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrBusy
	}
	for {
		ok, next := s.bucket.take(s.now())
		if ok {
			break
		}
		t := time.NewTimer(min(next, 50*time.Millisecond))
		select {
		case <-t.C:
		case <-wait.Done():
			t.Stop()
			return nil, busy()
		}
	}
	select {
	case s.sem <- struct{}{}:
	case <-wait.Done():
		return nil, busy()
	}
	InflightRequests.Inc()
	return func() {
		InflightRequests.Dec()
		<-s.sem
	}, nil
}
