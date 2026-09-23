package zone

import (
	"context"

	"github.com/google/uuid"

	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// SetZonemdStatus records the outcome of the last ZONEMD verification of zone zoneID: status is
// "off", "absent", "verified" or "failed", errText the failure reason ("" otherwise).
func SetZonemdStatus(ctx context.Context, q Execer, zoneID uuid.UUID, status, errText string) error {
	_, err := q.Exec(ctx, `UPDATE zones SET zonemd_status = $2, zonemd_error = $3 WHERE id = $1`, zoneID, status, errText)
	return store.MapError(err)
}
