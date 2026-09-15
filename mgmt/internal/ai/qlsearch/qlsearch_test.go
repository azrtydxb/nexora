package qlsearch_test

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/aifake"
	"github.com/piwi3910/nexora/mgmt/internal/ai/qlsearch"
	"github.com/piwi3910/nexora/mgmt/internal/catalog"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func insertPolicyGroup(t *testing.T, st *store.Store, name, cidr string) uuid.UUID {
	t.Helper()
	var g store.PolicyGroup
	err := st.InTx(context.Background(), func(tx pgx.Tx) error {
		var err error
		g, err = store.CreatePolicyGroup(context.Background(), tx, store.PolicyGroup{Name: name, CIDRs: []netip.Prefix{netip.MustParsePrefix(cidr)}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return g.ID
}

// lastUserText is the text of the last message of a recorded model call.
func lastUserText(c provider.Call) string {
	if len(c.Messages) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range c.Messages[len(c.Messages)-1].Content {
		if tp, ok := p.(provider.TextPart); ok {
			b.WriteString(tp.Text)
		}
	}
	return b.String()
}

// TestQueryLogSearchTranslation catches unresolved policy group names, a range longer than 7 days that
// is accepted, validation errors that are not fed back to the model, and a summary that is not kept.
func TestQueryLogSearchTranslation(t *testing.T) {
	st := storetest.New(t)
	guest := insertPolicyGroup(t, st, "guest", "10.9.0.0/24")
	now := time.Date(2026, 9, 15, 16, 0, 0, 0, time.UTC)
	ql := querylog.NewBuiltin(100)
	m := aifake.Model(
		aifake.JSON(map[string]any{"policy_group_names": []string{"finance"}, "filter": []string{"blocked"}, "explanation": "x"}),
		aifake.JSON(map[string]any{"from": now.Add(-8 * 24 * time.Hour), "to": now, "policy_group_names": []string{"guest"}, "explanation": "x"}),
		aifake.JSON(map[string]any{"from": now.Add(-2 * time.Hour), "to": now, "qtype": []string{"TXT"}, "filter": []string{"blocked"},
			"policy_group_names": []string{"guest"}, "explanation": "blocked TXT from guest"}),
		aifake.JSON(map[string]any{"summary": "No blocked TXT queries.", "suggestions": []string{}}),
	)
	run := qlsearch.New(st, aifake.Service(t, st, m, nil), ql, &catalog.Catalog{}, func() time.Time { return now })
	in, _ := json.Marshal(qlsearch.Input{Query: "blocked TXT queries from the guest group in the last 2 hours"})
	out, err := run(context.Background(), ai.Task{Kind: ai.TaskQueryLogSearch, Input: in})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(qlsearch.Result)
	if r.Filters.PolicyGroup[0] != guest.String() || r.Filters.QType[0] != "TXT" || r.Filters.Filter[0] != "blocked" ||
		r.Filters.To.Sub(r.Filters.From) != 2*time.Hour || r.Summary != "No blocked TXT queries." {
		t.Fatalf("result %+v", r)
	}
	calls := m.RecordedCalls()
	if !strings.Contains(lastUserText(calls[1]), `unknown policy group "finance"; known: guest`) ||
		!strings.Contains(lastUserText(calls[2]), "range longer than 7 days") {
		t.Fatal("validation errors were not fed back")
	}
}

// TestQueryLogSearchDefaultsToLastHour catches a translation without a range that searches the whole
// log instead of the last hour.
func TestQueryLogSearchDefaultsToLastHour(t *testing.T) {
	st := storetest.New(t)
	now := time.Date(2026, 9, 15, 16, 0, 0, 0, time.UTC)
	m := aifake.Model(
		aifake.JSON(map[string]any{"name": "example", "explanation": "x"}),
		aifake.JSON(map[string]any{"summary": "quiet", "suggestions": []string{"a"}}),
	)
	run := qlsearch.New(st, aifake.Service(t, st, m, nil), querylog.NewBuiltin(10), &catalog.Catalog{}, func() time.Time { return now })
	in, _ := json.Marshal(qlsearch.Input{Query: "example"})
	out, err := run(context.Background(), ai.Task{Kind: ai.TaskQueryLogSearch, Input: in})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(qlsearch.Result)
	if !r.Filters.To.Equal(now) || !r.Filters.From.Equal(now.Add(-time.Hour)) || r.Filters.Name != "example" {
		t.Fatalf("filters %+v", r.Filters)
	}
}
