package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/piwi3910/nexora/mgmt/migrations"
)

// DefaultEngineGroupID is the engine group every engine and join token falls back to.
var DefaultEngineGroupID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// EngineScopedTables are the configuration tables whose rows apply to every engine group
// (engine_group_id IS NULL) or to one engine group.
var EngineScopedTables = []string{
	"upstreams", "filter_lists", "policy_groups", "rewrites", "forward_zones", "zones", "rpz_zones",
}

// MigrateDownTo rolls the schema back to version (tests and emergency operations only).
func (s *Store) MigrateDownTo(ctx context.Context, version int64) error {
	db := stdlib.OpenDBFromPool(s.Pool)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return err
	}
	if _, err := provider.DownTo(ctx, version); err != nil {
		return fmt.Errorf("migrate down to %d: %w", version, MapError(err))
	}
	return nil
}
