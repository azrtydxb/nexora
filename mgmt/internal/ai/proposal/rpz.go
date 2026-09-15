package proposal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// MaxRPZRules bounds the rules of one appendAiRpzRules action.
const MaxRPZRules = 50

// maxReasonRunes bounds the reason written into the zone file comment.
const maxReasonRunes = 200

// rpzTargets maps a rule policy to the CNAME target that encodes it in RPZ.
var rpzTargets = map[string]string{"nxdomain": ".", "nodata": "*.", "drop": "rpz-drop.", "passthru": "rpz-passthru."}

// RPZRule is one suggested RPZ record. ProposalID is set for applied rules and names the proposal in the
// zone file comment; it is not part of an action body.
type RPZRule struct {
	Record     string    `json:"record"`
	Policy     string    `json:"policy"` // nxdomain|nodata|drop|passthru
	Category   string    `json:"category"`
	Reason     string    `json:"reason"`
	Confidence float64   `json:"confidence"`
	ProposalID uuid.UUID `json:"-"`
}

// DecodeRules parses the body {"rules": [...]} of an appendAiRpzRules action, rejecting unknown fields.
// Records are lower-cased without a trailing dot.
func DecodeRules(a Action) ([]RPZRule, error) {
	if a.OperationID != OpAppendAiRpzRules {
		return nil, fmt.Errorf("operation %s is not %s", a.OperationID, OpAppendAiRpzRules)
	}
	var body struct {
		Rules []RPZRule `json:"rules"`
	}
	dec := json.NewDecoder(bytes.NewReader(a.Body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		return nil, fmt.Errorf("body: %w", err)
	}
	if len(body.Rules) == 0 || len(body.Rules) > MaxRPZRules {
		return nil, fmt.Errorf("body: rules needs 1 to %d entries, got %d", MaxRPZRules, len(body.Rules))
	}
	for i := range body.Rules {
		r := &body.Rules[i]
		r.Record = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(r.Record)), ".")
		if _, ok := rpzTargets[r.Policy]; !ok {
			return nil, fmt.Errorf("rules[%d]: policy %q must be nxdomain, nodata, drop or passthru", i, r.Policy)
		}
		if r.Category == "" || len(r.Category) > 64 {
			return nil, fmt.Errorf("rules[%d]: category needs 1 to 64 characters", i)
		}
		if r.Confidence < 0 || r.Confidence > 1 {
			return nil, fmt.Errorf("rules[%d]: confidence must be between 0 and 1", i)
		}
	}
	return body.Rules, nil
}

// commentText makes s safe for one zone file comment line, truncated to n runes.
func commentText(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	if r := []rune(s); len(r) > n {
		s = string(r[:n])
	}
	return s
}

// RenderZone renders rules as the BIND RPZ zone RPZZoneName with one comment line per rule. Records are
// relative to the zone origin; the serial is the Unix time of now.
func RenderZone(rules []RPZRule, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "$TTL 300\n@ SOA localhost. hostmaster.localhost. %d 3600 600 86400 300\n@ NS localhost.\n", uint32(now.Unix()))
	for _, r := range rules {
		fmt.Fprintf(&b, "; ai proposal %s category=%s reason=%s\n", r.ProposalID, commentText(r.Category, 64), commentText(r.Reason, maxReasonRunes))
		fmt.Fprintf(&b, "%s CNAME %s\n", r.Record, rpzTargets[r.Policy])
	}
	return b.String()
}

// AppliedRules returns every rule recorded by a successful apply, ordered by record.
func AppliedRules(ctx context.Context, q store.PolicyQuerier) ([]RPZRule, error) {
	rows, err := q.Query(ctx, "select record, policy, category, reason, proposal_id from ai_rpz_rules order by record")
	if err != nil {
		return nil, store.MapError(err)
	}
	defer rows.Close()
	out := []RPZRule{}
	for rows.Next() {
		var r RPZRule
		if err := rows.Scan(&r.Record, &r.Policy, &r.Category, &r.Reason, &r.ProposalID); err != nil {
			return nil, store.MapError(err)
		}
		out = append(out, r)
	}
	return out, store.MapError(rows.Err())
}

// RecordApplied records rules uploaded for proposalID; a record already present is replaced.
func RecordApplied(ctx context.Context, st *store.Store, rules []RPZRule, proposalID uuid.UUID, by string) error {
	return st.InTx(ctx, func(tx pgx.Tx) error {
		for _, r := range rules {
			if _, err := tx.Exec(ctx, `insert into ai_rpz_rules(record, policy, category, reason, proposal_id, applied_by) values ($1, $2, $3, $4, $5, $6)
				on conflict (record) do update set policy = excluded.policy, category = excluded.category, reason = excluded.reason,
					proposal_id = excluded.proposal_id, applied_by = excluded.applied_by, applied_at = now()`,
				r.Record, r.Policy, r.Category, commentText(r.Reason, maxReasonRunes), proposalID, by); err != nil {
				return err
			}
		}
		return nil
	})
}
