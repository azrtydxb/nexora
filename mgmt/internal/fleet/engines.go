// Package fleet reads the engine fleet: engine views with their target versions and status, the
// snapshot each engine should run, and the fleet gauges.
package fleet

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/rollout"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Engine status values (first match wins, in this order).
const (
	StatusRevoked      = "revoked"
	StatusAhead        = "ahead"
	StatusDisconnected = "disconnected"
	StatusRejected     = "rejected"
	StatusCurrent      = "current"
	StatusBehind       = "behind"
)

// EngineView is one non-deleted engine with its engine group, target version and status.
type EngineView struct {
	ID, EngineGroupID                                                                                 uuid.UUID
	NodeName, EngineVersion, EngineGroupName, RejectedReason, PersistError, CertificateSerial, Status string
	EnrolledAt                                                                                        time.Time
	LastSeenAt, RevokedAt, CertRotateRequestedAt, CertificateNotAfter                                 *time.Time
	Connected, VersionAhead                                                                           bool
	AppliedVersion, TargetVersion                                                                     uint64
	RejectedVersion                                                                                   *uint64
	Labels                                                                                            map[string]string
	Revision                                                                                          int64
}

// EngineFilter narrows ListEngines; nil fields do not filter.
type EngineFilter struct {
	EngineGroupID, EngineID *uuid.UUID
}

// ListEngines returns the non-deleted engines matching f, ordered by node name.
func ListEngines(ctx context.Context, q store.PolicyQuerier, f EngineFilter) ([]EngineView, error) {
	// An engine counts as connected while the instance holding its stream heartbeats (every 5 s).
	rows, err := q.Query(ctx, `
		select e.id, e.node_name, e.engine_version, e.enrolled_at, e.last_seen_at,
		       (e.connected_instance is not null and coalesce(i.heartbeat_at > now() - interval '15 seconds', false)),
		       e.applied_version, e.rejected_version, e.rejected_reason, e.persist_error, e.version_ahead,
		       e.engine_group_id, g.name, e.labels, e.revision, e.revoked_at, e.cert_rotate_requested_at,
		       e.certificate_serial, c.not_after, coalesce(g.stable_version, 0)
		from engines e
		join engine_groups g on g.id = e.engine_group_id
		left join instances i on i.id = e.connected_instance
		left join engine_certificates c on c.serial = e.certificate_serial
		where e.deleted_at is null and ($1::uuid is null or e.engine_group_id = $1) and ($2::uuid is null or e.id = $2)
		order by e.node_name, e.enrolled_at`, f.EngineGroupID, f.EngineID)
	if err != nil {
		return nil, store.MapError(err)
	}
	type row struct {
		v      EngineView
		stable uint64
	}
	scanned, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var out row
		var applied, stable int64
		var rejected *int64
		err := r.Scan(&out.v.ID, &out.v.NodeName, &out.v.EngineVersion, &out.v.EnrolledAt, &out.v.LastSeenAt, &out.v.Connected,
			&applied, &rejected, &out.v.RejectedReason, &out.v.PersistError, &out.v.VersionAhead,
			&out.v.EngineGroupID, &out.v.EngineGroupName, &out.v.Labels, &out.v.Revision, &out.v.RevokedAt, &out.v.CertRotateRequestedAt,
			&out.v.CertificateSerial, &out.v.CertificateNotAfter, &stable)
		out.v.AppliedVersion, out.stable = uint64(applied), uint64(stable)
		if rejected != nil {
			rv := uint64(*rejected)
			out.v.RejectedVersion = &rv
		}
		return out, err
	})
	if err != nil {
		return nil, store.MapError(err)
	}
	latest, err := latestRollouts(ctx, q)
	if err != nil {
		return nil, err
	}
	views := make([]EngineView, len(scanned))
	for i, r := range scanned {
		v := r.v
		var newest *rollout.Rollout
		if lr, ok := latest[v.EngineGroupID]; ok {
			newest = &lr
		}
		v.TargetVersion = rollout.Target(rollout.Engine{ID: v.ID, AppliedVersion: v.AppliedVersion}, r.stable, newest)
		v.Status = status(v)
		views[i] = v
	}
	return views, nil
}

func status(v EngineView) string {
	switch {
	case v.RevokedAt != nil:
		return StatusRevoked
	case v.VersionAhead || v.TargetVersion > 0 && v.AppliedVersion > v.TargetVersion:
		return StatusAhead
	case !v.Connected:
		return StatusDisconnected
	case v.RejectedVersion != nil && *v.RejectedVersion > v.AppliedVersion:
		return StatusRejected
	case v.AppliedVersion == v.TargetVersion:
		return StatusCurrent
	}
	return StatusBehind
}

// latestRollouts returns each engine group's newest non-superseded rollout.
func latestRollouts(ctx context.Context, q store.PolicyQuerier) (map[uuid.UUID]rollout.Rollout, error) {
	rows, err := q.Query(ctx, `select distinct on (engine_group_id) engine_group_id, version, state, canary_engine_ids
		from rollouts where state <> 'superseded' order by engine_group_id, version desc`)
	if err != nil {
		return nil, store.MapError(err)
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (rollout.Rollout, error) {
		var r rollout.Rollout
		var version int64
		err := row.Scan(&r.EngineGroupID, &version, &r.State, &r.CanaryEngineIDs)
		r.Version = uint64(version)
		return r, err
	})
	if err != nil {
		return nil, store.MapError(err)
	}
	out := make(map[uuid.UUID]rollout.Rollout, len(list))
	for _, r := range list {
		out[r.EngineGroupID] = r
	}
	return out, nil
}

// Target is what one engine should run: its engine group's snapshot of the target version (nil
// when the target is 0, before any rollout of its group completed).
type Target struct {
	EngineGroupID   uuid.UUID
	Version         uint64
	Snapshot        *controlv1.ConfigSnapshot
	RotateRequested bool
	Revoked         bool
}

// TargetFor loads engine engineID's target (store.ErrNotFound for an unknown or deleted engine).
func TargetFor(ctx context.Context, q store.PolicyQuerier, engineID uuid.UUID) (Target, error) {
	targets, err := Targets(ctx, q, EngineFilter{EngineID: &engineID})
	if err != nil {
		return Target{}, err
	}
	t, ok := targets[engineID]
	if !ok {
		return Target{}, store.ErrNotFound
	}
	return t, nil
}

// Targets loads the targets of the non-deleted engines matching f by engine id, decoding each
// group snapshot once however many engines target it.
func Targets(ctx context.Context, q store.PolicyQuerier, f EngineFilter) (map[uuid.UUID]Target, error) {
	views, err := ListEngines(ctx, q, f)
	if err != nil {
		return nil, err
	}
	type key struct {
		group   uuid.UUID
		version uint64
	}
	snaps := map[key]*controlv1.ConfigSnapshot{}
	out := make(map[uuid.UUID]Target, len(views))
	for _, v := range views {
		t := Target{EngineGroupID: v.EngineGroupID, Version: v.TargetVersion, RotateRequested: v.CertRotateRequestedAt != nil,
			Revoked: v.RevokedAt != nil}
		if t.Version > 0 {
			k := key{v.EngineGroupID, t.Version}
			if snaps[k] == nil {
				var raw []byte
				if err := q.QueryRow(ctx, "select snapshot from group_snapshots where engine_group_id = $1 and version = $2",
					v.EngineGroupID, int64(t.Version)).Scan(&raw); err != nil {
					return nil, store.MapError(err)
				}
				snap := &controlv1.ConfigSnapshot{}
				if err := proto.Unmarshal(raw, snap); err != nil {
					return nil, fmt.Errorf("engine group %s version %d: %w", v.EngineGroupID, t.Version, err)
				}
				snaps[k] = snap
			}
			t.Snapshot = snaps[k]
		}
		out[v.ID] = t
	}
	return out, nil
}
