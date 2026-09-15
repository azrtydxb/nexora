package proposal_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/piwi3910/nexora/mgmt/internal/ai/proposal"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func draft(groupID uuid.UUID, rev int64, description string) proposal.Draft {
	return proposal.Draft{
		Source: "filter_recommendations", Title: "Block ads for guests", Description: "d", Priority: "medium",
		Impact: map[string]any{"queries": 10},
		Actions: []proposal.Action{{
			OperationID: "updatePolicyGroup", PathParams: map[string]string{"id": groupID.String()},
			Body:        json.RawMessage(fmt.Sprintf(`{"name":"guests","cidrs":["10.9.0.0/16"],"description":%q,"revision":%d}`, description, rev)),
			Explanation: fmt.Sprintf("explanation %d", rev),
		}},
	}
}

// TestUpsertDedupeAndDismiss catches duplicate rows for one fingerprint, re-proposing a recently
// dismissed suggestion, and two applies claiming the same proposal.
func TestUpsertDedupeAndDismiss(t *testing.T) {
	ctx := testCtx(t)
	st := storetest.New(t)
	group := uuid.New()

	id, created, err := proposal.Upsert(ctx, st, draft(group, 1, "a"))
	if err != nil || !created || id == uuid.Nil {
		t.Fatalf("first upsert: %v %v %v", id, created, err)
	}
	first, err := proposal.Get(ctx, st.Pool, id)
	if err != nil {
		t.Fatal(err)
	}
	// Revision and explanation are not part of the fingerprint.
	again, created, err := proposal.Upsert(ctx, st, draft(group, 2, "a"))
	if err != nil || created || again != id {
		t.Fatalf("second upsert: %v %v %v", again, created, err)
	}
	refreshed, _ := proposal.Get(ctx, st.Pool, id)
	if !refreshed.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("updated_at not advanced: %v -> %v", first.UpdatedAt, refreshed.UpdatedAt)
	}
	other, created, err := proposal.Upsert(ctx, st, draft(group, 1, "b"))
	if err != nil || !created || other == id {
		t.Fatalf("different body: %v %v %v", other, created, err)
	}
	all, err := proposal.List(ctx, st, proposal.Filter{Source: "filter_recommendations"})
	if err != nil || len(all) != 2 {
		t.Fatalf("list: %d %v", len(all), err)
	}

	if err := proposal.Dismiss(ctx, st, id, "op", "not now"); err != nil {
		t.Fatal(err)
	}
	if err := proposal.Dismiss(ctx, st, id, "op", ""); !errors.Is(err, proposal.ErrNotOpen) {
		t.Fatalf("second dismiss: %v", err)
	}
	if got, created, err := proposal.Upsert(ctx, st, draft(group, 1, "a")); err != nil || created || got != uuid.Nil {
		t.Fatalf("upsert of dismissed: %v %v %v", got, created, err)
	}
	if _, err := st.Pool.Exec(ctx, "update ai_proposals set reviewed_at = now() - interval '8 days' where id = $1", id); err != nil {
		t.Fatal(err)
	}
	reproposed, created, err := proposal.Upsert(ctx, st, draft(group, 1, "a"))
	if err != nil || !created || reproposed == uuid.Nil || reproposed == id {
		t.Fatalf("upsert after 8 days: %v %v %v", reproposed, created, err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var finishes []func(string, any, string) error
	notOpen := 0
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, finish, err := proposal.Claim(ctx, st, reproposed)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case errors.Is(err, proposal.ErrNotOpen):
				notOpen++
			case err != nil:
				t.Error(err)
			default:
				finishes = append(finishes, finish)
			}
		}()
	}
	wg.Wait()
	if notOpen != 1 || len(finishes) != 1 {
		t.Fatalf("concurrent claims: %d not open, %d claimed", notOpen, len(finishes))
	}
	if err := finishes[0]("applied", map[string]any{"actions": []any{}}, "op"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := proposal.Claim(ctx, st, reproposed); !errors.Is(err, proposal.ErrNotOpen) {
		t.Fatalf("claim after apply: %v", err)
	}
	p, _ := proposal.Get(ctx, st.Pool, reproposed)
	if p.Status != "applied" || p.ReviewedBy != "op" || p.ReviewedAt == nil || len(p.Result) == 0 {
		t.Fatalf("applied proposal: %+v", p)
	}
}
