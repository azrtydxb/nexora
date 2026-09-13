package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/store"
)

const heartbeatInterval = 5 * time.Second

// NewInstanceID returns `<hostname>-<8 hex>`.
func NewInstanceID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "nexora-mgmt"
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return host + "-" + hex.EncodeToString(b)
}

// RunInstanceHeartbeat upserts this instance's row every 5 s until ctx ends. Engines count as
// connected while their connected_instance heartbeat is younger than 15 s.
func RunInstanceHeartbeat(ctx context.Context, st *store.Store, instanceID string) {
	t := time.NewTicker(heartbeatInterval)
	defer t.Stop()
	for {
		if err := upsertInstance(ctx, st, instanceID); err != nil && ctx.Err() == nil {
			slog.Warn("instance heartbeat failed", "instance", instanceID, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func upsertInstance(ctx context.Context, st *store.Store, instanceID string) error {
	_, err := st.Pool.Exec(ctx, `insert into instances(id) values ($1)
		on conflict (id) do update set heartbeat_at = now()`, instanceID)
	return store.MapError(err)
}
