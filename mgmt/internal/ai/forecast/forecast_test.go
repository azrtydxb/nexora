package forecast_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/ai/forecast"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func TestLatestPerSubject(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	put := func(subject string, at time.Time, trend string) {
		t.Helper()
		f := forecast.Forecast{Kind: "upstream", Subject: subject, Detail: json.RawMessage(`{"trend":"` + trend + `"}`),
			GeneratedAt: at, ValidUntil: at.Add(6 * time.Hour)}
		if err := forecast.Put(ctx, st, f); err != nil {
			t.Fatal(err)
		}
	}
	put("u1", t0, "stable")
	put("u1", t0.Add(time.Hour), "degrading")
	put("u2", t0, "stable")
	// A capacity forecast must not leak into the upstream list.
	if err := forecast.Put(ctx, st, forecast.Forecast{Kind: "capacity", Subject: "cache", Detail: json.RawMessage(`{}`), GeneratedAt: t0, ValidUntil: t0.Add(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}

	got, err := forecast.Latest(ctx, st.Pool, "upstream")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("latest: %d rows, want 2: %+v", len(got), got)
	}
	bySubject := map[string]forecast.Forecast{}
	for _, f := range got {
		bySubject[f.Subject] = f
	}
	u1 := bySubject["u1"]
	var detail struct{ Trend string }
	_ = json.Unmarshal(u1.Detail, &detail)
	if !u1.GeneratedAt.Equal(t0.Add(time.Hour)) || detail.Trend != "degrading" || u1.ID.String() == "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("u1 is not the newer forecast: %+v (%s)", u1, u1.Detail)
	}
	if _, ok := bySubject["u2"]; !ok {
		t.Fatalf("u2 missing: %+v", got)
	}
	all, _ := forecast.Latest(ctx, st.Pool, "")
	if len(all) != 3 {
		t.Fatalf("latest of every kind: %d rows, want 3", len(all))
	}
}
