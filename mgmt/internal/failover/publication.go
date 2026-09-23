package failover

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/piwi3910/nexora/mgmt/internal/fleet"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// EvidenceCollector is a trusted dependency, never a desired-state request body.
// ObservationReader implements it. Its input publisher must bind authenticated
// connectionSession to the exact persisted identity/pod/container; UUID-only Hello
// cannot establish that relationship. There is no implicit collector/authorizer.
type EvidenceCollector interface {
	Collect(context.Context, Group, time.Duration) ([]MemberEvidence, error)
}

// CollectAndPublish collects once and attempts each of its two transactions once
// through a dedicated collector
// LOGIN pool. sequence is allocated by the worker before collection and increases
// within the PublisherToken session. Failed collection commits an empty pool; a
// failed/ambiguous commit returns an empty result and never retries the side effect.
// The returned eligibility is telemetry, not a durable serving/ownership grant.
func CollectAndPublish(ctx context.Context, pool *pgxpool.Pool, t PublisherToken, sequence int64, maxAge time.Duration, c EvidenceCollector) (Eligibility, error) {
	empty := EvaluateEligibility(Group{}, nil, time.Time{}, 0)
	if pool == nil || missingDependency(c) || sequence <= 0 || maxAge < time.Millisecond || maxAge > 30*time.Second {
		return empty, ErrInvalid
	}
	st := &store.Store{Pool: pool}
	// Each database stage includes acquisition, BEGIN, locks, writes and COMMIT.
	// Background callers receive the same finite budget. Cleanup is separately
	// bounded by Store and retires uncertain connections, including failed BEGIN.
	budget := min(maxAge, 5*time.Second)
	stageCtx, cancel := context.WithTimeout(ctx, budget)
	var source uuid.UUID
	var g Group
	var start time.Time
	err := st.InTxOnce(stageCtx, func(tx pgx.Tx) error {
		var err error
		source, err = sourceIncarnation(stageCtx, tx)
		if err != nil {
			return err
		}
		g, err = lockPublisher(stageCtx, tx, t)
		if err != nil {
			return err
		}
		if err = tx.QueryRow(stageCtx, `select clock_timestamp()`).Scan(&start); err != nil {
			return err
		}
		tag, err := tx.Exec(stageCtx, `update public.failover_publishers set sequence=$2 where group_id=$1 and sequence<$2`, g.ID, sequence)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return store.ErrConflict
		}
		_, err = tx.Exec(stageCtx, `delete from public.failover_publications where group_id=$1`, g.ID)
		return err
	})
	if err == nil {
		err = publicationContextError(stageCtx)
	}
	cancel()
	if err != nil {
		return empty, err
	}
	collectCtx, cancel := context.WithTimeout(ctx, maxAge)
	evidence, collectErr := c.Collect(collectCtx, g, maxAge)
	if collectErr == nil {
		collectErr = publicationContextError(collectCtx)
	}
	cancel()
	if collectErr != nil {
		evidence = nil
	}
	stageCtx, cancel = context.WithTimeout(ctx, budget)
	defer cancel()
	var decision publicationDecision
	err = st.InTxOnce(stageCtx, func(tx pgx.Tx) error {
		var err error
		decision, err = publish(stageCtx, tx, t, sequence, source, start, maxAge, evidence)
		return err
	})
	if err != nil {
		return empty, err
	}
	if err = publicationContextError(stageCtx); err != nil {
		return empty, err
	}
	// Only acknowledged commits reach here. Count final writes and commit latency
	// against every evidence/certificate deadline using a conservative DB-clock
	// estimate anchored before the final clock query (including its round trip).
	now := decision.now.Add(time.Since(decision.clockStarted))
	if !now.Before(start.Add(maxAge)) || !freshEligibility(start, now, maxAge) {
		decision.evidence = nil
	}
	return EvaluateEligibility(decision.group, decision.evidence, now, maxAge), collectErr
}

func publicationContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

type publicationDecision struct {
	group             Group
	evidence          []MemberEvidence
	now, clockStarted time.Time
}

func sourceIncarnation(ctx context.Context, tx pgx.Tx) (uuid.UUID, error) {
	if err := pinAuthoritySchema(ctx, tx); err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx, `select s.incarnation from public.failover_sources s join pg_catalog.pg_roles r on r.oid=s.role_oid and r.rolname=s.role_name
 where s.role_name=session_user and s.purpose='collector' for share of s`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrUnauthorized
	}
	return id, err
}

func publish(ctx context.Context, tx pgx.Tx, t PublisherToken, sequence int64, source uuid.UUID, start time.Time, maxAge time.Duration, evidence []MemberEvidence) (publicationDecision, error) {
	empty := publicationDecision{}
	authorized, err := sourceIncarnation(ctx, tx)
	if err != nil {
		return empty, err
	}
	if authorized != source {
		return empty, ErrUnauthorized
	}
	g, err := lockPublisher(ctx, tx, t)
	if err != nil {
		return empty, err
	}
	var previous int64
	if err = tx.QueryRow(ctx, `select sequence from public.failover_publishers where group_id=$1`, g.ID).Scan(&previous); err != nil {
		return empty, err
	}
	if sequence != previous {
		return empty, store.ErrConflict
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `select clock_timestamp()`).Scan(&now); err != nil {
		return empty, err
	}
	// Late successful IO cannot extend freshness, and future timestamps are denied.
	if !freshEligibility(start, now, maxAge) {
		evidence = nil
	}
	evidence, err = validateInventory(ctx, tx, g, evidence, now, maxAge)
	if err != nil {
		return empty, err
	}
	clockStarted := time.Now()
	if err = tx.QueryRow(ctx, `select clock_timestamp()`).Scan(&now); err != nil {
		return empty, err
	}
	if !freshEligibility(start, now, maxAge) {
		evidence = nil
	}
	result := publicationDecision{group: g, evidence: evidence, now: now, clockStarted: clockStarted}
	raw, err := json.Marshal(evidence)
	if err != nil {
		return empty, err
	}
	expires := start.Add(maxAge)
	// A slow/failed collection still replaces the previous result with an expired
	// empty record. It does not refresh any of the original measurement timestamps.
	_, err = tx.Exec(ctx, `insert into public.failover_publications(group_id,generation,source_incarnation,max_age_ms,epoch,sequence,evidence,collected_at,expires_at)
 values($1,$2,$3,$4,$5,$6,$7,$8,$9) on conflict(group_id) do update set generation=excluded.generation,source_incarnation=excluded.source_incarnation,
 max_age_ms=excluded.max_age_ms,epoch=excluded.epoch,sequence=excluded.sequence,evidence=excluded.evidence,collected_at=excluded.collected_at,expires_at=excluded.expires_at`, g.ID, g.Generation, source, maxAge.Milliseconds(), t.Epoch, sequence, raw, start, expires)
	if err != nil {
		return empty, err
	}
	_, err = tx.Exec(ctx, `update public.failover_publishers set sequence=$2 where group_id=$1`, g.ID, sequence)
	return result, err
}

// ReadPublished revalidates persisted evidence against current source authority,
// generation, owner incarnation, engine sessions/revocation and snapshot targets.
// Caller supplies a short READ COMMITTED tx and owns its bounded acquisition and
// cleanup. This function bounds its own IO, but locks live until caller cleanup.
// Errors and missing/stale records always return an explicitly empty pool.
func ReadPublished(ctx context.Context, tx pgx.Tx, id uuid.UUID) (Eligibility, error) {
	stageStarted := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	empty := EvaluateEligibility(Group{}, nil, time.Time{}, 0)
	if err := pinAuthoritySchema(ctx, tx); err != nil {
		return empty, err
	}
	g, err := scan(tx.QueryRow(ctx, "select "+columns+" from public.failover_groups where id=$1 for share", id))
	if err != nil {
		return empty, err
	}
	empty = EvaluateEligibility(g, nil, time.Now(), time.Second)
	var raw []byte
	var start, expires, now time.Time
	var age int64
	// SHARE locks serialize source revoke and owner rotation with this read.
	err = tx.QueryRow(ctx, `select p.evidence,p.collected_at,p.expires_at,p.max_age_ms,clock_timestamp()
 from public.failover_publications p join public.failover_publishers o on o.group_id=p.group_id and o.epoch=p.epoch and o.sequence=p.sequence and o.generation=p.generation
 join public.failover_sources s on s.incarnation=p.source_incarnation and s.purpose='collector'
 join pg_catalog.pg_roles r on r.oid=s.role_oid and r.rolname=s.role_name
 where p.group_id=$1 and p.generation=$2 for share of p,o,s`, id, g.Generation).Scan(&raw, &start, &expires, &age, &now)
	if errors.Is(err, pgx.ErrNoRows) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	maxAge := time.Duration(age) * time.Millisecond
	ctx, cancelAge := context.WithDeadline(ctx, stageStarted.Add(maxAge))
	defer cancelAge()
	if !now.Before(expires) || !freshEligibility(start, now, maxAge) {
		return empty, nil
	}
	var evidence []MemberEvidence
	if err = json.Unmarshal(raw, &evidence); err != nil {
		return empty, err
	}
	evidence, err = validateInventory(ctx, tx, g, evidence, now, maxAge)
	if err != nil {
		return empty, err
	}
	// Query processing time counts against freshness too.
	clockStarted := time.Now()
	if err = tx.QueryRow(ctx, `select clock_timestamp()`).Scan(&now); err != nil {
		return empty, err
	}
	if !now.Before(expires) || !freshEligibility(start, now, maxAge) {
		return empty, nil
	}
	if err = publicationContextError(ctx); err != nil {
		return empty, err
	}
	now = now.Add(time.Since(clockStarted))
	if !now.Before(expires) || !freshEligibility(start, now, maxAge) {
		return empty, nil
	}
	return EvaluateEligibility(g, evidence, now, maxAge), nil
}

func validateInventory(ctx context.Context, tx pgx.Tx, g Group, input []MemberEvidence, now time.Time, maxAge time.Duration) ([]MemberEvidence, error) {
	if len(input) != 2 {
		return nil, nil
	}
	evidence := append([]MemberEvidence(nil), input...)
	// Same deterministic engine lock order as desired CRUD. Group and snapshot
	// authority are subsequently locked; any database deadlock fails closed.
	rows, err := tx.Query(ctx, `select id from public.engines where id in ($1,$2) order by id for share`, g.Members[0], g.Members[1])
	if err != nil {
		return nil, err
	}
	_, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, err
	}
	for i := range evidence {
		e := &evidence[i]
		e.InventoryValidUntil = now
		if e.EngineID != g.Members[0] && e.EngineID != g.Members[1] {
			return nil, nil
		}
		if e.ConnectionSession == uuid.Nil || e.ContainerID == "" || !canonicalUID(e.Placement.NodeUID) {
			return nil, nil
		}
		var session *uuid.UUID
		var policy uuid.UUID
		var applied int64
		var revoked, deleted, connected, badApply bool
		var lastSeen *time.Time
		err = tx.QueryRow(ctx, `select connection_session,engine_group_id,applied_version,revoked_at is not null,deleted_at is not null,
   connected_instance is not null,persist_error<>'' or version_ahead or rejected_version is not null,last_seen_at from public.engines where id=$1`, e.EngineID).
			Scan(&session, &policy, &applied, &revoked, &deleted, &connected, &badApply, &lastSeen)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if session == nil || *session != e.ConnectionSession {
			return nil, nil
		}
		// Never overwrite stale inventory timestamps with database receipt time.
		e.Revoked = e.Revoked || revoked
		e.Deleted = e.Deleted || deleted
		if policy != e.PolicyGroupID {
			return nil, nil
		}
		if lastSeen != nil {
			e.InventoryValidUntil = lastSeen.Add(maxAge)
		}
		if !connected || lastSeen == nil || !freshEligibility(*lastSeen, now, maxAge) {
			e.Management.OK = false
		}
		if badApply || applied <= 0 || uint64(applied) != e.Applied.Version {
			e.Applied = EligibilitySnapshot{}
		}
		var groupID uuid.UUID
		if err = tx.QueryRow(ctx, `select id from public.engine_groups where id=$1 for share`, policy).Scan(&groupID); err != nil {
			return nil, err
		}
		// Fleet.TargetFor uses the existing rollout target algorithm. Its writers
		// lock engine_groups; this SHARE lock excludes a concurrent target change.
		target, err := fleet.TargetFor(ctx, tx, e.EngineID)
		if errors.Is(err, store.ErrNotFound) {
			e.Deleted = true
			continue
		}
		if err != nil {
			return nil, err
		}
		if target.Snapshot == nil || target.Version == 0 {
			e.Target = EligibilitySnapshot{}
			continue
		}
		digest, err := snapshot.ContentDigest(target.Snapshot)
		if err != nil {
			return nil, err
		}
		if e.Target.Version != target.Version || e.Target.Digest != digest {
			e.Target = EligibilitySnapshot{}
		}
		var appliedDigest string
		err = tx.QueryRow(ctx, `select content_sha256 from public.group_snapshots where engine_group_id=$1 and version=$2 for share`, policy, applied).Scan(&appliedDigest)
		if errors.Is(err, pgx.ErrNoRows) {
			e.Applied = EligibilitySnapshot{}
		} else if err != nil {
			return nil, err
		} else if e.Applied.Digest != appliedDigest {
			e.Applied = EligibilitySnapshot{}
		}
		var certOK bool
		var certExpiry time.Time
		err = tx.QueryRow(ctx, `select c.revoked_at is null and c.not_before <= $2 and c.not_after > $2,c.not_after
   from public.engine_certificates c join public.engines e on e.certificate_serial=c.serial and e.id=c.engine_id where e.id=$1 for share of c`, e.EngineID, now).Scan(&certOK, &certExpiry)
		if errors.Is(err, pgx.ErrNoRows) {
			e.Revoked = true
		} else if err != nil {
			return nil, err
		} else if !certOK {
			e.Revoked = true
		}
		if !certExpiry.IsZero() && certExpiry.Before(e.InventoryValidUntil) {
			e.InventoryValidUntil = certExpiry
		}
	}
	return evidence, nil
}

var _ EvidenceCollector = (*ObservationReader)(nil)
