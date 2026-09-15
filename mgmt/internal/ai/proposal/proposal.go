// Package proposal stores AI proposals, validates their actions against the embedded OpenAPI document
// and renders the RPZ rules they suggest. Nothing here applies a proposal: the API replays its actions
// with the reviewing operator's credentials.
package proposal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// RPZZoneName is the file RPZ zone that applied RPZ rule proposals compile into.
const RPZZoneName = "ai-suggested.rpz"

// OpAppendAiRpzRules is the RPZ action: it is not an OpenAPI operation, apply compiles it into
// createRpzZone and uploadRpzZoneFile replays.
const OpAppendAiRpzRules = "appendAiRpzRules"

// MaxActions bounds the actions of one proposal.
const MaxActions = 8

// AllowedOperations are the only operations a proposal action may name.
var AllowedOperations = map[string]bool{"updateFilterCategory": true, "createPolicyGroup": true, "updatePolicyGroup": true,
	"updateGlobalSafeSearch": true, "updateAllowlist": true, "updateResolverSettings": true, "updateUpstream": true,
	"updateEngineGroup": true, OpAppendAiRpzRules: true}

// Action is one API call a proposal suggests.
type Action struct {
	OperationID string            `json:"operation_id"`
	PathParams  map[string]string `json:"path_params"`
	Body        json.RawMessage   `json:"body,omitempty"`
	Explanation string            `json:"explanation,omitempty"`
}

// Draft is a proposal as an agent produces it.
type Draft struct {
	Source, Title, Description, Priority string
	Impact, Evidence, Risk               any
	Actions                              []Action
	SessionID                            *uuid.UUID
}

// Proposal is a stored proposal.
type Proposal struct {
	ID                                       uuid.UUID
	Source, Fingerprint, Status              string
	Title, Description, Priority, ReviewedBy string
	DismissReason                            string
	Impact, Evidence, Risk, Result           json.RawMessage
	Actions                                  []Action
	SessionID                                *uuid.UUID
	CreatedAt, UpdatedAt                     time.Time
	ReviewedAt                               *time.Time
}

// ErrNotOpen is returned for a proposal that is not open, missing, or locked by a concurrent apply
// or dismiss.
var ErrNotOpen = errors.New("proposal is not open")

// dismissQuiet is how long a dismissed fingerprint is not re-proposed.
const dismissQuiet = 7 * 24 * time.Hour

// Fingerprint is source + ":" + the first 16 hex digits of the SHA-256 of the canonical JSON of the
// actions without their body revision and explanation.
func Fingerprint(source string, actions []Action) string {
	canon := make([]map[string]any, len(actions))
	for i, a := range actions {
		var body any
		if len(a.Body) > 0 {
			dec := json.NewDecoder(bytes.NewReader(a.Body))
			dec.UseNumber()
			if dec.Decode(&body) != nil {
				body = string(a.Body)
			}
		}
		if m, ok := body.(map[string]any); ok {
			delete(m, "revision")
		}
		params := a.PathParams
		if params == nil {
			params = map[string]string{}
		}
		canon[i] = map[string]any{"operation_id": a.OperationID, "path_params": params, "body": body}
	}
	raw, _ := json.Marshal(canon) // maps marshal with sorted keys
	sum := sha256.Sum256(raw)
	return source + ":" + hex.EncodeToString(sum[:])[:16]
}

func jsonOr(v any, empty string) ([]byte, error) {
	if v == nil {
		if empty == "" {
			return nil, nil
		}
		return []byte(empty), nil
	}
	return json.Marshal(v)
}

// Upsert refreshes an open proposal with the same fingerprint, returns (uuid.Nil, false, nil) when that
// fingerprint was dismissed in the last 7 days, and inserts otherwise.
func Upsert(ctx context.Context, st *store.Store, d Draft) (uuid.UUID, bool, error) {
	if len(d.Actions) == 0 || len(d.Actions) > MaxActions {
		return uuid.Nil, false, fmt.Errorf("a proposal needs 1 to %d actions, got %d", MaxActions, len(d.Actions))
	}
	fp := Fingerprint(d.Source, d.Actions)
	impact, err := jsonOr(d.Impact, "{}")
	if err != nil {
		return uuid.Nil, false, err
	}
	evidence, err := jsonOr(d.Evidence, "{}")
	if err != nil {
		return uuid.Nil, false, err
	}
	risk, err := jsonOr(d.Risk, "")
	if err != nil {
		return uuid.Nil, false, err
	}
	actions, err := json.Marshal(d.Actions)
	if err != nil {
		return uuid.Nil, false, err
	}
	var id uuid.UUID
	var created bool
	err = st.InTx(ctx, func(tx pgx.Tx) error {
		var dismissed bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from ai_proposals where fingerprint = $1 and status = 'dismissed'
			and reviewed_at > now() - $2 * interval '1 second')`, fp, int64(dismissQuiet.Seconds())).Scan(&dismissed); err != nil {
			return err
		}
		if dismissed {
			id = uuid.Nil
			return nil
		}
		return tx.QueryRow(ctx, `insert into ai_proposals(source, fingerprint, title, description, priority, impact, evidence, actions, risk, session_id)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			on conflict (fingerprint) where status = 'open' do update set title = excluded.title, description = excluded.description,
				priority = excluded.priority, impact = excluded.impact, evidence = excluded.evidence, actions = excluded.actions,
				risk = excluded.risk, session_id = excluded.session_id, updated_at = now()
			returning id, xmax = 0`,
			d.Source, fp, d.Title, d.Description, d.Priority, impact, evidence, actions, risk, d.SessionID).Scan(&id, &created)
	})
	if err != nil {
		return uuid.Nil, false, err
	}
	return id, created, nil
}

const proposalColumns = `id, source, fingerprint, status, title, description, priority, impact, evidence, actions, risk, session_id,
	created_at, updated_at, reviewed_by, reviewed_at, result, dismiss_reason`

func scan(row pgx.Row) (Proposal, error) {
	var p Proposal
	var actions []byte
	err := row.Scan(&p.ID, &p.Source, &p.Fingerprint, &p.Status, &p.Title, &p.Description, &p.Priority, &p.Impact, &p.Evidence,
		&actions, &p.Risk, &p.SessionID, &p.CreatedAt, &p.UpdatedAt, &p.ReviewedBy, &p.ReviewedAt, &p.Result, &p.DismissReason)
	if err != nil {
		return Proposal{}, store.MapError(err)
	}
	if err := json.Unmarshal(actions, &p.Actions); err != nil {
		return Proposal{}, fmt.Errorf("proposal %s actions: %w", p.ID, err)
	}
	return p, nil
}

// Filter selects proposals; empty fields match everything and Limit defaults to 100 (at most 500).
type Filter struct {
	Source, Status string
	Limit          int
}

// List returns proposals newest first.
func List(ctx context.Context, st *store.Store, f Filter) ([]Proposal, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	f.Limit = min(f.Limit, 500)
	rows, err := st.Pool.Query(ctx, `select `+proposalColumns+` from ai_proposals
		where ($1 = '' or source = $1) and ($2 = '' or status = $2) order by created_at desc, id limit $3`, f.Source, f.Status, f.Limit)
	if err != nil {
		return nil, store.MapError(err)
	}
	defer rows.Close()
	out := []Proposal{}
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, store.MapError(rows.Err())
}

// Get returns one proposal or store.ErrNotFound.
func Get(ctx context.Context, q store.PolicyQuerier, id uuid.UUID) (Proposal, error) {
	return scan(q.QueryRow(ctx, `select `+proposalColumns+` from ai_proposals where id = $1`, id))
}

// Claim locks an open proposal (FOR UPDATE SKIP LOCKED) in a transaction that the returned finish
// function commits: it stores the status, result and reviewer. A skipped or non-open row gives
// ErrNotOpen. The caller must call finish exactly once; status "open" records the result and keeps the
// proposal open.
func Claim(ctx context.Context, st *store.Store, id uuid.UUID) (Proposal, func(status string, result any, by string) error, error) {
	tx, err := st.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Proposal{}, nil, store.MapError(err)
	}
	p, err := scan(tx.QueryRow(ctx, `select `+proposalColumns+` from ai_proposals where id = $1 and status = 'open' for update skip locked`, id))
	if err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		if errors.Is(err, store.ErrNotFound) {
			return Proposal{}, nil, ErrNotOpen
		}
		return Proposal{}, nil, err
	}
	finish := func(status string, result any, by string) error {
		// The apply outlives a client disconnect: the replayed actions already happened.
		fctx := context.WithoutCancel(ctx)
		defer func() { _ = tx.Rollback(fctx) }()
		raw, err := jsonOr(result, "")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(fctx, `update ai_proposals set status = $2, result = $3, updated_at = now(),
			reviewed_by = case when $2 = 'open' then reviewed_by else $4 end,
			reviewed_at = case when $2 = 'open' then reviewed_at else now() end where id = $1`, id, status, raw, by); err != nil {
			return store.MapError(err)
		}
		return store.MapError(tx.Commit(fctx))
	}
	return p, finish, nil
}

// Dismiss marks an open proposal dismissed by by with an optional reason; a non-open or locked
// proposal gives ErrNotOpen.
func Dismiss(ctx context.Context, st *store.Store, id uuid.UUID, by, reason string) error {
	tag, err := st.Pool.Exec(ctx, `with c as (select id from ai_proposals where id = $1 and status = 'open' for update skip locked)
		update ai_proposals p set status = 'dismissed', reviewed_by = $2, reviewed_at = now(), dismiss_reason = $3, updated_at = now()
		from c where p.id = c.id`, id, by, reason)
	if err != nil {
		return store.MapError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotOpen
	}
	return nil
}

// SupersedeSession marks every other open proposal of an assistant session superseded.
func SupersedeSession(ctx context.Context, st *store.Store, sessionID, keep uuid.UUID) error {
	_, err := st.Pool.Exec(ctx, `update ai_proposals set status = 'superseded', updated_at = now()
		where session_id = $1 and id <> $2 and status = 'open'`, sessionID, keep)
	return store.MapError(err)
}
