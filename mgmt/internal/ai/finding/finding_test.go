package finding_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/ai/finding"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func TestSyncLifecycle(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	c := finding.Candidate{ID: "dns_tunneling:10.0.1.45", Kind: "anomaly", Type: "dns_tunneling", Severity: "warning", Title: "t", Description: "d"}
	if changed, err := finding.Sync(ctx, st, "anomaly", []finding.Candidate{c}, t0); err != nil || !changed {
		t.Fatalf("new candidate: changed=%v err=%v", changed, err)
	}
	if changed, _ := finding.Sync(ctx, st, "anomaly", []finding.Candidate{c}, t0.Add(30*time.Second)); changed {
		t.Fatal("unchanged candidate reported as changed")
	}
	c.Severity = "critical"
	if changed, _ := finding.Sync(ctx, st, "anomaly", []finding.Candidate{c}, t0.Add(time.Minute)); !changed {
		t.Fatal("severity rise not reported")
	}
	list, _ := finding.List(ctx, st.Pool, finding.Filter{Kind: "anomaly", Status: "open", Limit: 10})
	if len(list) != 1 || list[0].Severity != "critical" || list[0].Explained {
		t.Fatalf("list: %+v", list)
	}
	if _, err := finding.Update(ctx, st, list[0].ID, "dismissed", auth.Actor{Type: "user", ID: "u1", Name: "otto"}); err != nil {
		t.Fatal(err)
	}
	if changed, _ := finding.Sync(ctx, st, "anomaly", []finding.Candidate{c}, t0.Add(2*time.Minute)); changed {
		t.Fatal("dismissed candidate raised again within 24 h at the same severity")
	}
	other := finding.Candidate{ID: "nxdomain_burst:10.0.1.9", Kind: "anomaly", Type: "nxdomain_burst", Severity: "warning", Title: "t", Description: "d"}
	_, _ = finding.Sync(ctx, st, "anomaly", []finding.Candidate{other}, t0.Add(3*time.Minute))
	_, _ = finding.Sync(ctx, st, "anomaly", nil, t0.Add(34*time.Minute))
	resolved, _ := finding.List(ctx, st.Pool, finding.Filter{Kind: "anomaly", Status: "resolved", Limit: 10})
	if len(resolved) != 1 || resolved[0].CandidateID != other.ID {
		t.Fatalf("resolved after 30 min: %+v", resolved)
	}
	var audits int
	_ = st.Pool.QueryRow(ctx, "select count(*) from audit_log where action='updateAiFinding'").Scan(&audits)
	if audits != 1 {
		t.Fatalf("audit rows %d", audits)
	}
}

func TestExplainKeepsDetectorSeverity(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	c := finding.Candidate{ID: "servfail_spike:e1", Kind: "insight", Type: "servfail_spike", Severity: "warning", Title: "t", Description: "d",
		Detail: map[string]any{"engines": []string{"e1"}}}
	if _, err := finding.Sync(ctx, st, "insight", []finding.Candidate{c}, t0); err != nil {
		t.Fatal(err)
	}
	ex := finding.Explanation{CandidateID: c.ID, Severity: "critical", Confidence: 0.75, Title: "SERVFAIL spike on e1", Description: "upstream down",
		Detail: map[string]any{"possible_causes": []string{"upstream"}, "detector_severity": "info"}}
	if err := finding.Explain(ctx, st, "insight", []finding.Explanation{ex}); err != nil {
		t.Fatal(err)
	}
	list, _ := finding.List(ctx, st.Pool, finding.Filter{Kind: "insight", Limit: 10})
	if len(list) != 1 || !list[0].Explained || list[0].Severity != "critical" || list[0].Title != ex.Title || list[0].Confidence != 0.75 {
		t.Fatalf("explained: %+v", list)
	}
	var detail map[string]any
	_ = json.Unmarshal(list[0].Detail, &detail)
	if detail["engines"] == nil || detail["possible_causes"] == nil {
		t.Fatalf("detail not merged: %s", list[0].Detail)
	}
	// The model re-graded the finding; the detector still says warning, so nothing changed.
	if changed, err := finding.Sync(ctx, st, "insight", []finding.Candidate{c}, t0.Add(30*time.Second)); err != nil || changed {
		t.Fatalf("re-graded finding reported as changed=%v err=%v", changed, err)
	}
	list, _ = finding.List(ctx, st.Pool, finding.Filter{Kind: "insight", Limit: 10})
	if !list[0].Explained || list[0].Title != ex.Title {
		t.Fatalf("explanation lost on an unchanged sync: %+v", list[0])
	}
}
