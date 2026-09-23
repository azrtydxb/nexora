package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The unmerged M8 branch assigned 1300-1303 to different migrations. Goose
// records versions, not filenames or checksums: renaming those files cannot
// upgrade an already-applied branch database. Refuse that history before Up
// changes anything; an operator must inspect it and plan a separate transition.
// Called while holding the migration advisory lock, on that same connection.
func checkM8MigrationHistory(ctx context.Context, conn *pgxpool.Conn) error {
	var history bool
	if err := conn.QueryRow(ctx, `select to_regclass('goose_db_version') is not null`).Scan(&history); err != nil {
		return fmt.Errorf("inspect migration history: %w", err)
	}
	rows, err := conn.Query(ctx, `select version, present from (values
		(1300, to_regclass('failover_groups') is not null),
		(1301, exists(select 1 from information_schema.columns where table_schema = current_schema() and table_name = 'engines' and column_name = 'connection_session')),
		(1302, exists(select 1 from information_schema.columns where table_schema = current_schema() and table_name = 'zones' and column_name = 'zonemd_generate')),
		(1303, to_regclass('catalog_zones') is not null),
		(1304, to_regclass('odoh_settings') is not null),
		(1305, exists(select 1 from information_schema.columns where table_schema = current_schema() and table_name = 'engine_groups' and column_name = 'mdns_enabled'))
	) as markers(version, present) order by version`)
	if err != nil {
		return fmt.Errorf("inspect M8 schema: %w", err)
	}
	type marker struct {
		version int
		present bool
	}
	var markers []marker
	for rows.Next() {
		var m marker
		if err := rows.Scan(&m.version, &m.present); err != nil {
			rows.Close()
			return err
		}
		markers = append(markers, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, m := range markers {
		var applied bool
		if history {
			if err := conn.QueryRow(ctx, `select coalesce((select is_applied from goose_db_version where version_id = $1 order by id desc limit 1), false)`, m.version).Scan(&applied); err != nil {
				return fmt.Errorf("inspect migration %d: %w", m.version, err)
			}
		}
		if applied != m.present {
			return fmt.Errorf("unsupported legacy M8 or inconsistent migration history at version %d: applied=%t schema_present=%t; inspect history before upgrading; no automatic renumbering is safe", m.version, applied, m.present)
		}
	}
	return nil
}
