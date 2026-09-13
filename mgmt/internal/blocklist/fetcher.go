package blocklist

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

const (
	pollInterval  = 30 * time.Second
	gcInterval    = time.Hour
	fetchTimeout  = 60 * time.Second
	keptVersions  = 20
	maxErrorBytes = 1024
	// Each attempt holds one pooled connection for its lock and needs a second to publish; bounding
	// concurrent attempts keeps them from exhausting the pool (16 connections) and deadlocking.
	maxConcurrentRefreshes = 4
)

var systemActor = auth.Actor{Type: "system", ID: "blocklist-fetcher", Name: "system"}

// Fetcher downloads filter lists and publishes a new config version when a list's content changes.
type Fetcher struct {
	st    *store.Store
	build snapshot.BuildConfig
	hc    *http.Client
	slots chan struct{}
}

// NewFetcher returns a fetcher that publishes through st with build and downloads with hc.
func NewFetcher(st *store.Store, build snapshot.BuildConfig, hc *http.Client) *Fetcher {
	return &Fetcher{st: st, build: build, hc: hc, slots: make(chan struct{}, maxConcurrentRefreshes)}
}

// Run refreshes every enabled list whose refresh interval has elapsed, every 30 s, and deletes
// unreferenced blobs hourly, until ctx is done.
func (f *Fetcher) Run(ctx context.Context) {
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	var lastGC time.Time
	for {
		f.refreshDue(ctx)
		if time.Since(lastGC) >= gcInterval {
			if err := f.collectBlobs(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("blob garbage collection", "err", err)
			}
			lastGC = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// RefreshNow fetches list id on behalf of p. It waits for a fetch of the same list that another
// instance has in progress, then fetches again, so the row reflects an attempt started after the
// call. A download or parse failure is recorded on the row (last_error) and is not returned.
func (f *Fetcher) RefreshNow(ctx context.Context, p auth.Principal, id string) error {
	return f.refresh(ctx, id, p.Actor(), true)
}

func (f *Fetcher) refreshDue(ctx context.Context) {
	rows, err := f.st.Pool.Query(ctx, `select id::text from filter_lists where enabled and (last_attempt_at is null
		or last_attempt_at < now() - refresh_interval_seconds * interval '1 second') order by last_attempt_at nulls first`)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("select due filter lists", "err", err)
		}
		return
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		slog.Warn("select due filter lists", "err", err)
		return
	}
	for _, id := range ids {
		if err := f.refresh(ctx, id, systemActor, false); err != nil && ctx.Err() == nil {
			slog.Warn("refresh filter list", "id", id, "err", err)
		}
	}
}

// refresh runs one attempt for list id under the list's session advisory lock. Without wait, a
// lock held elsewhere means another instance is fetching and refresh returns nil.
func (f *Fetcher) refresh(ctx context.Context, id string, actor auth.Actor, wait bool) error {
	select {
	case f.slots <- struct{}{}:
		defer func() { <-f.slots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	conn, err := f.st.Pool.Acquire(ctx)
	if err != nil {
		return store.MapError(err)
	}
	defer conn.Release()
	unlock, err := lockList(ctx, conn, "filter_list:"+id, wait)
	if err != nil || unlock == nil {
		return err
	}
	defer unlock()

	var url string
	var currentSHA *string
	if err := f.st.Pool.QueryRow(ctx, "select url, current_blob_sha256 from filter_lists where id = $1", id).Scan(&url, &currentSHA); err != nil {
		return store.MapError(err)
	}
	text, stats, err := f.download(ctx, url)
	if err != nil {
		msg := err.Error()
		if len(msg) > maxErrorBytes {
			msg = strings.ToValidUTF8(msg[:maxErrorBytes], "")
		}
		_, dbErr := f.st.Pool.Exec(ctx, "update filter_lists set last_attempt_at = now(), last_error = $2 where id = $1", id, msg)
		return store.MapError(dbErr)
	}
	data, sha, err := Compress(text)
	if err != nil {
		return err
	}
	entries := bytes.Count(text, []byte{'\n'})
	if currentSHA != nil && *currentSHA == sha {
		_, err := f.st.Pool.Exec(ctx, `update filter_lists set last_attempt_at = now(), last_success_at = now(), entry_count = $2,
			invalid_line_count = $3, last_error = '' where id = $1`, id, entries, stats.Invalid)
		return store.MapError(err)
	}
	_, err = snapshot.Mutate(ctx, f.st, f.build, actor, func(tx pgx.Tx) (auth.Change, error) {
		// Taken before the blob insert so blob garbage collection (which holds the same lock)
		// cannot delete a reused blob between this insert and the reference to it.
		if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtext('nexora:config_version'))"); err != nil {
			return auth.Change{}, err
		}
		var beforeSHA *string
		var beforeCount int
		if err := tx.QueryRow(ctx, "select current_blob_sha256, entry_count from filter_lists where id = $1 for update", id).Scan(&beforeSHA, &beforeCount); err != nil {
			return auth.Change{}, err
		}
		if _, err := tx.Exec(ctx, "insert into blobs(sha256, size, data) values ($1, $2, $3) on conflict do nothing", sha, len(data), data); err != nil {
			return auth.Change{}, err
		}
		_, err := tx.Exec(ctx, `update filter_lists set current_blob_sha256 = $2, entry_count = $3, invalid_line_count = $4,
			last_success_at = now(), last_attempt_at = now(), last_error = '', revision = revision + 1 where id = $1`,
			id, sha, entries, stats.Invalid)
		return auth.Change{Action: "refreshFilterList", TargetType: "filter_list", TargetID: id,
			Before: map[string]any{"sha256": beforeSHA, "entry_count": beforeCount},
			After:  map[string]any{"sha256": sha, "entry_count": entries}}, err
	})
	return err
}

// lockList takes the session advisory lock key on conn. It returns a nil unlock function when
// wait is false and the lock is held elsewhere.
func lockList(ctx context.Context, conn *pgxpool.Conn, key string, wait bool) (func(), error) {
	if wait {
		if _, err := conn.Exec(ctx, "select pg_advisory_lock(hashtext($1))", key); err != nil {
			return nil, store.MapError(err)
		}
	} else {
		var ok bool
		if err := conn.QueryRow(ctx, "select pg_try_advisory_lock(hashtext($1))", key).Scan(&ok); err != nil {
			return nil, store.MapError(err)
		}
		if !ok {
			return nil, nil
		}
	}
	return func() {
		// If the connection broke, the session (and with it the lock) is already gone.
		_, _ = conn.Exec(context.WithoutCancel(ctx), "select pg_advisory_unlock(hashtext($1))", key)
	}, nil
}

// download fetches url and returns the normalised list text and the parse statistics.
func (f *Fetcher) download(ctx context.Context, url string) ([]byte, ParseStats, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, ParseStats{}, err
	}
	req.Header.Set("User-Agent", "nexora-mgmt")
	resp, err := f.hc.Do(req)
	if err != nil {
		return nil, ParseStats{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, ParseStats{}, fmt.Errorf("http status %d", resp.StatusCode)
	}
	body := &io.LimitedReader{R: resp.Body, N: MaxListBytes + 1}
	domains, stats, err := Parse(body)
	if body.N == 0 {
		return nil, ParseStats{}, errors.New("list exceeds 256 MiB")
	}
	if err != nil {
		return nil, ParseStats{}, fmt.Errorf("read list: %w", err)
	}
	return Normalize(domains), stats, nil
}

// collectBlobs deletes blobs referenced neither by a filter list nor by the newest config versions.
func (f *Fetcher) collectBlobs(ctx context.Context) error {
	return f.st.InTx(ctx, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, "select pg_try_advisory_xact_lock(hashtext('nexora:blob-gc'))").Scan(&ok); err != nil || !ok {
			return err
		}
		// Blocks publishing, so no version referencing a blob can commit between the read and the delete.
		if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtext('nexora:config_version'))"); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "select version, snapshot from config_versions order by version desc limit $1", keptVersions)
		if err != nil {
			return err
		}
		keep := []string{}
		var version int64
		var raw []byte
		_, err = pgx.ForEachRow(rows, []any{&version, &raw}, func() error {
			snap := &controlv1.ConfigSnapshot{}
			if err := proto.Unmarshal(raw, snap); err != nil {
				return fmt.Errorf("config version %d: %w", version, err)
			}
			for _, ref := range append(snap.GetFilter().GetBlocklists(), snap.GetFilter().GetAllowlists()...) {
				keep = append(keep, ref.GetSha256())
			}
			return nil
		})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `delete from blobs where sha256 <> all($1) and sha256 not in
			(select current_blob_sha256 from filter_lists where current_blob_sha256 is not null)`, keep)
		return err
	})
}
