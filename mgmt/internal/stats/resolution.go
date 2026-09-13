package stats

import (
	"context"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// RecordM3 upserts the engine's latest DNSSEC and RPZ status from s. Reports for RPZ zones that
// no longer exist (or ids that are not UUIDs) are skipped.
func RecordM3(ctx context.Context, st *store.Store, engineID string, s *controlv1.Stats) error {
	if s.Dnssec != nil {
		raw, err := protojson.Marshal(s.Dnssec)
		if err != nil {
			return err
		}
		if _, err := st.Pool.Exec(ctx, `insert into engine_dnssec_status(engine_id, stats, reported_at) values ($1, $2, now())
			on conflict (engine_id) do update set stats = excluded.stats, reported_at = excluded.reported_at`, engineID, raw); err != nil {
			return store.MapError(err)
		}
	}
	for _, z := range s.RpzZones {
		id, err := uuid.Parse(z.Id)
		if err != nil {
			continue
		}
		var lastSuccess *time.Time
		if z.LastSuccessUnix != 0 {
			t := time.Unix(z.LastSuccessUnix, 0).UTC()
			lastSuccess = &t
		}
		if _, err := st.Pool.Exec(ctx, `insert into engine_rpz_status(engine_id, rpz_zone_id, serial, records, skipped, hits,
			last_success_at, last_error, stale, reported_at)
			select $1, $2, $3, $4, $5, $6, $7, $8, $9, now() where exists (select 1 from rpz_zones where id = $2)
			on conflict (engine_id, rpz_zone_id) do update set serial = excluded.serial, records = excluded.records,
			skipped = excluded.skipped, hits = excluded.hits, last_success_at = excluded.last_success_at,
			last_error = excluded.last_error, stale = excluded.stale, reported_at = excluded.reported_at`,
			engineID, id, int64(z.Serial), clampInt64(z.Records), clampInt64(z.Skipped), clampInt64(z.Hits),
			lastSuccess, z.LastError, z.Stale); err != nil {
			return store.MapError(err)
		}
	}
	return nil
}

func clampInt64(v uint64) int64 {
	return int64(min(v, 1<<63-1))
}
