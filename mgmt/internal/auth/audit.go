// Package auth holds identities, authorization and the audit log.
package auth

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Actor is who performed a change.
type Actor struct {
	Type     string // user | api_token | system
	ID, Name string
}

// Change describes one audited mutation.
type Change struct {
	Action, TargetType, TargetID string
	Before, After                any
}

// WriteAudit inserts an audit_log row inside tx with diff = {"before": ..., "after": ...}.
func WriteAudit(ctx context.Context, tx pgx.Tx, a Actor, c Change, configVersion *uint64) error {
	diff, err := json.Marshal(map[string]any{"before": c.Before, "after": c.After})
	if err != nil {
		return fmt.Errorf("audit diff: %w", err)
	}
	var cv *int64
	if configVersion != nil {
		v := int64(*configVersion)
		cv = &v
	}
	_, err = tx.Exec(ctx, `insert into audit_log(actor_type, actor_id, actor_name, action, target_type, target_id, diff, config_version)
		values ($1, $2, $3, $4, $5, $6, $7, $8)`,
		a.Type, a.ID, a.Name, c.Action, c.TargetType, c.TargetID, diff, cv)
	return err
}
