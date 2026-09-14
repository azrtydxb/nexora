package api

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Commit, BuildDate and RepositoryURL are the build stamp and source link reported by getVersion
// (set by nexora-mgmt; empty when the binary was built without them or no repository is configured).
var Commit, BuildDate, RepositoryURL = "", "", ""

// engineVersion is one entry of VersionInfo.Engines.
type engineVersion = struct {
	Count   int    `json:"count"`
	Version string `json:"version"`
}

func (h *handlers) GetVersion(ctx context.Context, _ GetVersionRequestObject) (GetVersionResponseObject, error) {
	rows, err := h.d.Store.Pool.Query(ctx, `select engine_version, count(*) from engines
		where deleted_at is null and engine_version <> '' group by 1 order by 2 desc, 1`)
	if err != nil {
		return nil, store.MapError(err)
	}
	engines, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (engineVersion, error) {
		var e engineVersion
		err := r.Scan(&e.Version, &e.Count)
		return e, err
	})
	if err != nil {
		return nil, store.MapError(err)
	}
	return GetVersion200JSONResponse{Version: Version, Commit: Commit, BuildDate: BuildDate, RepositoryUrl: RepositoryURL, Engines: engines}, nil
}
