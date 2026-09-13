package xfrin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

const (
	dueBatch             = 20
	maxParallelRefreshes = 8
	reconnectDelay       = time.Second
)

// NotifyIgnored counts forwarded NOTIFY messages that named no secondary zone or did not come
// from one of its primaries.
var NotifyIgnored = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "nexora_mgmt_notify_ignored_total",
	Help: "NOTIFY messages forwarded by engines that were ignored (unknown zone or source not a primary).",
})

// Scheduler runs due secondary-zone refreshes. Each refresh holds the session advisory lock
// zone_refresh:<id>, so one management plane instance at a time refreshes a zone.
type Scheduler struct {
	Store     *store.Store
	Refresher *Refresher
	Tick      time.Duration

	once     sync.Once
	slots    chan struct{}
	mu       sync.Mutex
	inflight map[uuid.UUID]bool
	wg       sync.WaitGroup
}

func (s *Scheduler) init() {
	s.once.Do(func() {
		s.slots = make(chan struct{}, maxParallelRefreshes)
		s.inflight = map[uuid.UUID]bool{}
	})
}

// Run refreshes due zones every Tick and on each zone.RefreshChannel notification until ctx ends.
func (s *Scheduler) Run(ctx context.Context) error {
	s.init()
	defer s.wg.Wait()
	for {
		err := s.listen(ctx)
		if ctx.Err() != nil {
			return nil
		}
		slog.Warn("zone refresh listener disconnected", "err", err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(reconnectDelay):
		}
	}
}

func (s *Scheduler) listen(ctx context.Context) error {
	// A dedicated connection outside the pool: LISTEN holds it for the scheduler's lifetime.
	conn, err := pgx.ConnectConfig(ctx, s.Store.Pool.Config().ConnConfig)
	if err != nil {
		return store.MapError(err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, "LISTEN "+zone.RefreshChannel); err != nil {
		return store.MapError(err)
	}
	for {
		if err := s.runDue(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("list due secondary zones", "err", err)
		}
		wctx, cancel := context.WithTimeout(ctx, s.Tick)
		_, err := conn.WaitForNotification(wctx)
		cancel()
		switch {
		case ctx.Err() != nil:
			return nil
		case err == nil, errors.Is(wctx.Err(), context.DeadlineExceeded):
		default:
			return store.MapError(err)
		}
	}
}

func (s *Scheduler) runDue(ctx context.Context) error {
	rows, err := s.Store.Pool.Query(ctx, `SELECT id FROM zones WHERE kind = 'secondary' AND (next_refresh_at IS NULL OR next_refresh_at <= now())
		ORDER BY next_refresh_at NULLS FIRST LIMIT $1`, dueBatch)
	if err != nil {
		return store.MapError(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return store.MapError(err)
	}
	for _, id := range ids {
		s.mu.Lock()
		busy := s.inflight[id]
		s.mu.Unlock()
		if busy {
			continue
		}
		select {
		case s.slots <- struct{}{}:
		default:
			return nil // every slot busy: the rest waits for the next tick
		}
		s.mu.Lock()
		s.inflight[id] = true
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer func() {
				s.mu.Lock()
				delete(s.inflight, id)
				s.mu.Unlock()
				<-s.slots
				s.wg.Done()
			}()
			if err := s.refreshLocked(ctx, id); err != nil && ctx.Err() == nil {
				slog.Warn("secondary zone refresh", "zone", id, "err", err)
			}
		}()
	}
	return nil
}

// refreshLocked refreshes zone id under its advisory lock when it is still due.
func (s *Scheduler) refreshLocked(ctx context.Context, id uuid.UUID) error {
	conn, err := s.Store.Pool.Acquire(ctx)
	if err != nil {
		return store.MapError(err)
	}
	defer conn.Release()
	key := "zone_refresh:" + id.String()
	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtext($1))", key).Scan(&locked); err != nil {
		return store.MapError(err)
	}
	if !locked {
		return nil
	}
	// If the connection broke, the session (and with it the lock) is already gone.
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock(hashtext($1))", key) }()
	var due bool
	var trigger string
	err = conn.QueryRow(ctx, `SELECT next_refresh_at IS NULL OR next_refresh_at <= now(), refresh_trigger FROM zones
		WHERE id = $1 AND kind = 'secondary'`, id).Scan(&due, &trigger)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !due) {
		return nil // deleted, or refreshed by another instance meanwhile
	}
	if err != nil {
		return store.MapError(err)
	}
	if trigger == "" {
		trigger = "timer"
	}
	return s.Refresher.Refresh(ctx, id, trigger)
}

// Notify handles a NOTIFY for zoneName forwarded by an engine from source ("ip:port"): when source
// is one of the secondary zone's primaries, the zone is refreshed now.
func (s *Scheduler) Notify(ctx context.Context, zoneName, source string) error {
	host, _, err := net.SplitHostPort(source)
	if err != nil {
		NotifyIgnored.Inc()
		return fmt.Errorf("NOTIFY source %q is not ip:port", source)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		NotifyIgnored.Inc()
		return fmt.Errorf("NOTIFY source %q is not ip:port", source)
	}
	var id uuid.UUID
	var raw []byte
	err = s.Store.Pool.QueryRow(ctx, "SELECT id, primaries FROM zones WHERE name = $1 AND kind = 'secondary'", dns.CanonicalName(zoneName)).Scan(&id, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		NotifyIgnored.Inc()
		return fmt.Errorf("NOTIFY for %q: no such secondary zone", zoneName)
	}
	if err != nil {
		return store.MapError(err)
	}
	var primaries []zone.Endpoint
	if err := json.Unmarshal(raw, &primaries); err != nil {
		return fmt.Errorf("zone %s primaries: %w", zoneName, err)
	}
	for _, p := range primaries {
		if ap, err := netip.ParseAddrPort(p.Address); err == nil && ap.Addr().Unmap() == ip.Unmap() {
			return zone.RequestRefresh(ctx, s.Store.Pool, id, "notify")
		}
	}
	NotifyIgnored.Inc()
	return fmt.Errorf("NOTIFY for %s from %s: source is not a primary", zoneName, source)
}
