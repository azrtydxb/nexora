package snapshot

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

const ntaExpiryInterval = 60 * time.Second

// errNothingToExpire rolls the expiry transaction back without publishing a version.
var errNothingToExpire = errors.New("no expired negative trust anchors")

// RunNTAExpiry deletes expired negative trust anchors every 60 s until ctx ends, publishing a
// config version when it deleted any. Instances serialise on an advisory lock.
func RunNTAExpiry(ctx context.Context, st *store.Store, cfg BuildConfig) {
	tick := time.NewTicker(ntaExpiryInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if err := expireNTAs(ctx, st, cfg); err != nil && !errors.Is(err, errNothingToExpire) && ctx.Err() == nil {
			slog.Warn("expire negative trust anchors", "err", err)
		}
	}
}

func expireNTAs(ctx context.Context, st *store.Store, cfg BuildConfig) error {
	_, err := Mutate(ctx, st, cfg, auth.Actor{Type: "system", ID: "nta-expiry", Name: "system"}, func(tx pgx.Tx) (auth.Change, error) {
		var locked bool
		if err := tx.QueryRow(ctx, "select pg_try_advisory_xact_lock(hashtext('nexora:nta_expiry'))").Scan(&locked); err != nil {
			return auth.Change{}, err
		}
		if !locked {
			return auth.Change{}, errNothingToExpire
		}
		n, err := store.DeleteExpiredNegativeTrustAnchors(ctx, tx, time.Now())
		if err != nil {
			return auth.Change{}, err
		}
		if n == 0 {
			return auth.Change{}, errNothingToExpire
		}
		return auth.Change{Action: "expireNegativeTrustAnchors", TargetType: "negative_trust_anchor", TargetID: "expired",
			After: map[string]int64{"deleted": n}}, nil
	})
	return err
}
