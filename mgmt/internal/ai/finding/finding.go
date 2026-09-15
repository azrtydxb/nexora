// Package finding stores the anomaly and insight findings of the AI agents (M11 S-4, S-5).
//
// Detectors produce candidates; Sync keeps one active (open or acknowledged) finding per candidate
// id, and the model's explanation is attached afterwards with Explain. Findings are useful without
// the model: an unexplained finding carries the detector's title and description.
package finding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// SeverityRank orders severities; a dismissed candidate is raised again only above its rank.
var SeverityRank = map[string]int{"info": 1, "warning": 2, "critical": 3}

const (
	resolveAfter  = 30 * time.Minute
	dismissWindow = 24 * time.Hour
	// detectorSeverityKey keeps the detector's severity in detail: the model may re-grade a finding,
	// and "severity changed" (a new model call, a re-raise after dismissal) is judged on the detector's.
	detectorSeverityKey = "detector_severity"
	detectorSeverity    = "coalesce(detail->>'detector_severity', severity)"
)

// Candidate is one detector result.
type Candidate struct {
	ID                 string // "<type>:<subject>"
	Kind               string // "anomaly" | "insight"
	Type               string // e.g. "dns_tunneling", "servfail_spike"
	Severity           string // info|warning|critical
	Title, Description string // detector defaults
	Detail             map[string]any
}

// Explanation is the model's validated answer for one candidate.
type Explanation struct {
	CandidateID        string
	Severity           string
	Confidence         float64
	Title, Description string
	Detail             map[string]any // merged over the candidate detail
}

// Finding is a stored finding.
type Finding struct {
	ID                                      uuid.UUID
	Kind, CandidateID, Type, Status         string
	Severity, Title, Description, UpdatedBy string
	Confidence                              float64
	Explained                               bool
	Detail                                  json.RawMessage
	FirstSeen, LastSeen, UpdatedAt          time.Time
}

// Filter selects findings for List; empty Kind or Status matches every value.
type Filter struct {
	Kind, Status string
	Limit        int
}

var errSeverity = errors.New("finding: invalid severity")

// Sync upserts open/acknowledged findings for cands (last_seen = now), marks active ones not in cands
// and unseen for 30 min resolved, and skips candidates dismissed in the last 24 h unless the severity
// rose. changed is true when a candidate id is new or its severity changed.
func Sync(ctx context.Context, st *store.Store, kind string, cands []Candidate, now time.Time) (changed bool, err error) {
	err = st.InTx(ctx, func(tx pgx.Tx) error {
		changed = false
		for _, c := range cands {
			if SeverityRank[c.Severity] == 0 {
				return fmt.Errorf("%w %q for candidate %s", errSeverity, c.Severity, c.ID)
			}
			ch, err := syncOne(ctx, tx, kind, c, now)
			if err != nil {
				return err
			}
			changed = changed || ch
		}
		_, err := tx.Exec(ctx, `update ai_findings set status = 'resolved', updated_by = 'system', updated_at = $2
			where kind = $1 and status in ('open', 'acknowledged') and last_seen < $3`, kind, now, now.Add(-resolveAfter))
		return err
	})
	return changed, err
}

func syncOne(ctx context.Context, tx pgx.Tx, kind string, c Candidate, now time.Time) (bool, error) {
	d := maps.Clone(c.Detail)
	if d == nil {
		d = map[string]any{}
	}
	d[detectorSeverityKey] = c.Severity
	detail, err := json.Marshal(d)
	if err != nil {
		return false, fmt.Errorf("finding detail: %w", err)
	}
	var id uuid.UUID
	var severity string
	var explained bool
	err = tx.QueryRow(ctx, `select id, `+detectorSeverity+`, explained from ai_findings
		where kind = $1 and candidate_id = $2 and status in ('open', 'acknowledged') for update`, kind, c.ID).Scan(&id, &severity, &explained)
	switch {
	case err == nil && severity != c.Severity:
		// The severity moved: the old explanation no longer fits, so the detector text returns.
		_, err = tx.Exec(ctx, `update ai_findings set severity = $2, title = $3, description = $4, detail = $5,
			confidence = 0, explained = false, last_seen = $6 where id = $1`, id, c.Severity, c.Title, c.Description, detail, now)
		return true, err
	case err == nil && !explained:
		_, err = tx.Exec(ctx, `update ai_findings set title = $2, description = $3, detail = $4, last_seen = $5 where id = $1`,
			id, c.Title, c.Description, detail, now)
		return false, err
	case err == nil:
		_, err = tx.Exec(ctx, "update ai_findings set last_seen = $2 where id = $1", id, now)
		return false, err
	case !errors.Is(err, pgx.ErrNoRows):
		return false, err
	}
	var dismissed string
	err = tx.QueryRow(ctx, `select `+detectorSeverity+` from ai_findings where kind = $1 and candidate_id = $2 and status = 'dismissed'
		and updated_at > $3 order by updated_at desc limit 1`, kind, c.ID, now.Add(-dismissWindow)).Scan(&dismissed)
	switch {
	case err == nil && SeverityRank[c.Severity] <= SeverityRank[dismissed]:
		return false, nil
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return false, err
	}
	_, err = tx.Exec(ctx, `insert into ai_findings(kind, candidate_id, type, severity, title, description, detail, first_seen, last_seen, updated_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $8, $8)`, kind, c.ID, c.Type, c.Severity, c.Title, c.Description, detail, now)
	return true, err
}

// Explain attaches the model's explanations to the active findings of kind and sets explained=true.
// Explanations for candidate ids without an active finding are ignored.
func Explain(ctx context.Context, st *store.Store, kind string, ex []Explanation) error {
	return st.InTx(ctx, func(tx pgx.Tx) error {
		for _, e := range ex {
			if SeverityRank[e.Severity] == 0 {
				return fmt.Errorf("%w %q for candidate %s", errSeverity, e.Severity, e.CandidateID)
			}
			var raw []byte
			err := tx.QueryRow(ctx, `select detail from ai_findings where kind = $1 and candidate_id = $2
				and status in ('open', 'acknowledged') for update`, kind, e.CandidateID).Scan(&raw)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			merged := map[string]any{}
			if err := json.Unmarshal(raw, &merged); err != nil {
				return fmt.Errorf("finding detail: %w", err)
			}
			detector := merged[detectorSeverityKey]
			maps.Copy(merged, e.Detail)
			merged[detectorSeverityKey] = detector
			detail, err := json.Marshal(merged)
			if err != nil {
				return fmt.Errorf("finding detail: %w", err)
			}
			if _, err := tx.Exec(ctx, `update ai_findings set severity = $3, confidence = $4, title = $5, description = $6,
				detail = $7, explained = true where kind = $1 and candidate_id = $2 and status in ('open', 'acknowledged')`,
				kind, e.CandidateID, e.Severity, e.Confidence, e.Title, e.Description, detail); err != nil {
				return err
			}
		}
		return nil
	})
}

const selectFindings = `select id, kind, candidate_id, type, status, severity, title, description, updated_by, confidence,
	explained, detail, first_seen, last_seen, updated_at from ai_findings`

// List returns findings newest first (by last_seen).
func List(ctx context.Context, q store.PolicyQuerier, f Filter) ([]Finding, error) {
	rows, err := q.Query(ctx, selectFindings+` where ($1 = '' or kind = $1) and ($2 = '' or status = $2)
		order by last_seen desc, id limit $3`, f.Kind, f.Status, f.Limit)
	if err != nil {
		return nil, store.MapError(err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Finding, error) { return scan(row) })
	return out, store.MapError(err)
}

// Update sets an active finding's status to acknowledged or dismissed and audits updateAiFinding.
// A missing finding is store.ErrNotFound; a resolved or dismissed one is store.ErrConflict.
func Update(ctx context.Context, st *store.Store, id uuid.UUID, status string, actor auth.Actor) (Finding, error) {
	if status != "acknowledged" && status != "dismissed" {
		return Finding{}, fmt.Errorf("finding: status %q is not acknowledged or dismissed", status)
	}
	var out Finding
	err := st.InTx(ctx, func(tx pgx.Tx) error {
		before, err := scan(tx.QueryRow(ctx, selectFindings+" where id = $1 for update", id))
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		if before.Status != "open" && before.Status != "acknowledged" {
			return fmt.Errorf("finding is %s: %w", before.Status, store.ErrConflict)
		}
		out, err = scan(tx.QueryRow(ctx, `update ai_findings set status = $2, updated_by = $3, updated_at = now() where id = $1
			returning id, kind, candidate_id, type, status, severity, title, description, updated_by, confidence,
			explained, detail, first_seen, last_seen, updated_at`, id, status, actor.Name))
		if err != nil {
			return err
		}
		return auth.WriteAudit(ctx, tx, actor, auth.Change{Action: "updateAiFinding", TargetType: "ai_finding", TargetID: id.String(),
			Before: map[string]string{"status": before.Status}, After: map[string]string{"status": status}}, nil)
	})
	return out, err
}

func scan(row pgx.Row) (Finding, error) {
	var f Finding
	var confidence float32
	err := row.Scan(&f.ID, &f.Kind, &f.CandidateID, &f.Type, &f.Status, &f.Severity, &f.Title, &f.Description, &f.UpdatedBy,
		&confidence, &f.Explained, &f.Detail, &f.FirstSeen, &f.LastSeen, &f.UpdatedAt)
	f.Confidence = float64(confidence)
	return f, err
}
