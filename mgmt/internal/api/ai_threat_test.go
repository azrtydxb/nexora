package api

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/aifake"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/catalog"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

// isInvalid reports whether err is a rejected request (400 invalid_request).
func isInvalid(err error) bool {
	var verr validationError
	return errors.As(err, &verr)
}

// TestQueryLogRecordThreat catches a query-log read that does not label a name with its cached verdict,
// one that shows an expired verdict, and one that labels records while AI is off.
func TestQueryLogRecordThreat(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	st := storetest.New(t)
	now := time.Now().UTC().Truncate(time.Second)
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}

	for _, v := range []struct {
		name    string
		expires time.Time
	}{{"evil.ait.test", now.Add(6 * 24 * time.Hour)}, {"old.ait.test", now.Add(-time.Hour)}} {
		if _, err := st.Pool.Exec(ctx, `insert into ai_domain_verdicts(name, is_threat, categories, confidence, reasoning,
			checked_at, expires_at) values ($1, true, '{malware}', 0.9, 'r', $2, $3)`, v.name, now.Add(-time.Hour), v.expires); err != nil {
			t.Fatal(err)
		}
	}
	recs := []querylog.Record{
		{Time: now, Client: "10.0.0.1", Name: "EVIL.ait.test.", QType: "A", RCode: "NOERROR"},
		{Time: now, Client: "10.0.0.1", Name: "old.ait.test.", QType: "A", RCode: "NOERROR"},
		{Time: now, Client: "10.0.0.1", Name: "plain.ait.test.", QType: "A", RCode: "NOERROR"},
	}

	out, err := resolveRecordNames(ctx, st.Pool, cat, recs, true)
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case out[0].Threat == nil || !out[0].Threat.IsThreat || len(out[0].Threat.Categories) != 1 || out[0].Threat.Confidence != 0.9:
		t.Fatalf("cached verdict not on the record: %+v", out[0].Threat)
	case !out[0].Threat.CheckedAt.Equal(now.Add(-time.Hour)):
		t.Fatalf("checked_at = %v", out[0].Threat.CheckedAt)
	case out[1].Threat != nil:
		t.Fatalf("expired verdict on the record: %+v", out[1].Threat)
	case out[2].Threat != nil:
		t.Fatalf("verdict for an unchecked name: %+v", out[2].Threat)
	}

	off, err := resolveRecordNames(ctx, st.Pool, cat, recs, false)
	if err != nil {
		t.Fatal(err)
	}
	if off[0].Threat != nil {
		t.Fatalf("records are labelled while AI is off: %+v", off[0].Threat)
	}
}

// TestStartAiThreatCheckLimits catches a check that accepts more domains than the model is asked about in
// one task, or rejects the documented maximum.
func TestStartAiThreatCheckLimits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	st := storetest.New(t)
	svc := auth.NewService(st, false)
	vic := newAIUser(t, ctx, st, svc, "vic", auth.RoleViewer)

	tasks := ai.NewTasks(ctx, st, "test")
	tasks.Register(ai.TaskThreatCheck, func(context.Context, ai.Task) (any, error) { return map[string]any{"results": []any{}}, nil })
	svcAI := aifake.Service(t, st, aifake.Model(), nil)
	rt := &AIRuntime{Service: svcAI, Tasks: tasks, Config: svcAI.Config(), InstanceStart: time.Now(),
		TaskKinds: map[ai.TaskKind]bool{ai.TaskThreatCheck: true}, Agents: map[string]bool{}}
	h, _ := newHandlers(Deps{Store: st, Auth: svc, AI: rt})
	pctx := context.WithValue(ctx, principalKey{}, vic.p)

	domains := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("d%d.ait.test", i)
		}
		return out
	}
	body := StartAiThreatCheckJSONRequestBody{Domains: domains(100)}
	res, err := h.StartAiThreatCheck(pctx, StartAiThreatCheckRequestObject{Body: &body})
	if err != nil {
		t.Fatalf("100 domains: %v", err)
	}
	if _, ok := res.(StartAiThreatCheck202JSONResponse); !ok {
		t.Fatalf("100 domains = %T, want 202", res)
	}
	tooMany := StartAiThreatCheckJSONRequestBody{Domains: domains(101)}
	if _, err := h.StartAiThreatCheck(pctx, StartAiThreatCheckRequestObject{Body: &tooMany}); !isInvalid(err) {
		t.Fatalf("101 domains = %v, want 400", err)
	}
	empty := StartAiThreatCheckJSONRequestBody{Domains: []string{" "}}
	if _, err := h.StartAiThreatCheck(pctx, StartAiThreatCheckRequestObject{Body: &empty}); !isInvalid(err) {
		t.Fatalf("a blank domain = %v, want 400", err)
	}
}
