package snapshot

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/odoh"
)

// AddOdoh sets snap.Odoh from the fleet-wide ODoH settings: nil while both roles are off, so an
// engine without M8 and one with ODoH off see the same snapshot. Keys never enter a snapshot.
func AddOdoh(ctx context.Context, tx pgx.Tx, snap *controlv1.ConfigSnapshot) error {
	s, err := odoh.GetSettings(ctx, tx)
	if err != nil {
		return fmt.Errorf("odoh settings: %w", err)
	}
	snap.Odoh = odoh.Config(s)
	return nil
}
