package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/piwi3910/nexora/e2e/harness"
)

func filterFleetFixture(now time.Time) ([]kwExpectedEngine, []kwFilterEngine, map[string]uint64) {
	var expected []kwExpectedEngine
	var observed []kwFilterEngine
	versions := map[string]uint64{}
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		group := "00000000-0000-4000-8000-000000000100"
		name := fmt.Sprintf("engine-%d", i)
		expected = append(expected, kwExpectedEngine{id, name, group})
		observed = append(observed, kwFilterEngine{EngineView: harness.EngineView{ID: id, NodeName: name, EngineGroupID: group, Connected: true, Status: "current", AppliedVersion: 7, TargetVersion: 7}, LastSeenAt: now})
		versions[id] = 7
	}
	return expected, observed, versions
}

func TestAcceptanceExpectedFleet(t *testing.T) {
	expected, _, _ := filterFleetFixture(time.Now())
	raw, _ := json.Marshal(expected)
	for _, tc := range []struct {
		name, raw string
		count     int
		valid     bool
	}{
		{"paired-four", string(raw), 4, true},
		{"unset", "", 4, false}, {"null", "null", 4, false}, {"empty", "[]", 0, false},
		{"legacy-eight-count", string(raw), 8, false}, {"negative-count", string(raw), -1, false},
		{"trailing-json", string(raw) + "[]", 4, false},
		{"duplicate", strings.Replace(string(raw), expected[1].ID, expected[0].ID, 1), 4, false},
		{"duplicate-name", strings.Replace(string(raw), expected[1].NodeName, expected[0].NodeName, 1), 4, false},
		{"invalid-id", strings.Replace(string(raw), expected[0].ID, "engine-a", 1), 4, false},
		{"nil-id", strings.Replace(string(raw), expected[0].ID, "00000000-0000-0000-0000-000000000000", 1), 4, false},
		{"empty-name", strings.Replace(string(raw), expected[0].NodeName, "", 1), 4, false},
		{"empty-group", strings.Replace(string(raw), expected[0].GroupID, "", 1), 4, false},
		{"unknown-field", strings.Replace(string(raw), "node_name", "node", 1), 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := kwExpectedFleet(tc.raw, tc.count)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

func TestAcceptanceFleetValidation(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name   string
		mutate func(*[]kwFilterEngine)
		valid  bool
	}{
		{"paired-four", func(*[]kwFilterEngine) {}, true},
		{"empty", func(e *[]kwFilterEngine) { *e = nil }, false},
		{"missing", func(e *[]kwFilterEngine) { *e = (*e)[:3] }, false},
		{"extra", func(e *[]kwFilterEngine) { *e = append(*e, (*e)[0]) }, false},
		{"duplicate-uuid", func(e *[]kwFilterEngine) { (*e)[1].ID = (*e)[0].ID }, false},
		{"duplicate-name", func(e *[]kwFilterEngine) { (*e)[1].NodeName = (*e)[0].NodeName }, false},
		{"replacement", func(e *[]kwFilterEngine) { (*e)[0].ID = "00000000-0000-4000-8000-000000000099" }, false},
		{"renamed", func(e *[]kwFilterEngine) { (*e)[0].NodeName = "wrong" }, false},
		{"wrong-group", func(e *[]kwFilterEngine) { (*e)[0].EngineGroupID = "wrong" }, false},
		{"disconnected", func(e *[]kwFilterEngine) { (*e)[0].Connected = false }, false},
		{"all-disconnected", func(e *[]kwFilterEngine) {
			for i := range *e {
				(*e)[i].Connected = false
			}
		}, false},
		{"stale", func(e *[]kwFilterEngine) { (*e)[0].LastSeenAt = now.Add(-31 * time.Second) }, false},
		{"missing-timestamp", func(e *[]kwFilterEngine) { (*e)[0].LastSeenAt = time.Time{} }, false},
		{"future", func(e *[]kwFilterEngine) { (*e)[0].LastSeenAt = now.Add(6 * time.Second) }, false},
		{"behind", func(e *[]kwFilterEngine) { (*e)[0].Status = "behind" }, false},
		{"mismatched-applied", func(e *[]kwFilterEngine) { (*e)[0].AppliedVersion = 6 }, false},
		{"changed-target", func(e *[]kwFilterEngine) { (*e)[0].AppliedVersion = 8; (*e)[0].TargetVersion = 8 }, false},
		{"zero-versions", func(e *[]kwFilterEngine) { (*e)[0].AppliedVersion = 0; (*e)[0].TargetVersion = 0 }, false},
		{"revoked", func(e *[]kwFilterEngine) { (*e)[0].RevokedAt = &now }, false},
		{"persist-error", func(e *[]kwFilterEngine) { (*e)[0].PersistError = "failed" }, false},
		{"ahead", func(e *[]kwFilterEngine) { (*e)[0].VersionAhead = true }, false},
		{"rejection", func(e *[]kwFilterEngine) { v := uint64(8); (*e)[0].RejectedVersion = &v }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected, observed, versions := filterFleetFixture(now)
			tc.mutate(&observed)
			err := kwValidateFleet(expected, observed, versions, now)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
	expected, observed, versions := filterFleetFixture(now)
	if err := kwValidateFleet(nil, nil, nil, now); err == nil {
		t.Fatal("empty fleet passed")
	}
	delete(versions, expected[0].ID)
	if err := kwValidateFleet(expected, observed, versions, now); err == nil {
		t.Fatal("incomplete pinned versions passed")
	}
}

func validFilterIndex(now time.Time) kwFilterIndex {
	return kwFilterIndex{At: now, Entries: 5_100_000, Bytes: 110_000_000, MaxBytes: 120_000_000, BuildSeconds: 1.5, DecisionNSBlocked: 250, DecisionNSClean: 100, CPU: "cortex-a76"}
}
func TestAcceptanceFilterIndex(t *testing.T) {
	now := time.Now()
	after := now.Add(-time.Second)
	for _, tc := range []struct {
		name   string
		mutate func(*kwFilterIndex)
		valid  bool
	}{
		{"valid", func(*kwFilterIndex) {}, true},
		{"stale", func(f *kwFilterIndex) { f.At = now.Add(-31 * time.Second) }, false},
		{"before-application", func(f *kwFilterIndex) { f.At = after.Add(-time.Nanosecond) }, false},
		{"future", func(f *kwFilterIndex) { f.At = now.Add(6 * time.Second) }, false},
		{"missing-at", func(f *kwFilterIndex) { f.At = time.Time{} }, false},
		{"small-index", func(f *kwFilterIndex) { f.Entries = 999_999 }, false},
		{"missing-bytes", func(f *kwFilterIndex) { f.Bytes = 0 }, false},
		{"missing-cap", func(f *kwFilterIndex) { f.MaxBytes = 0 }, false},
		{"over-cap", func(f *kwFilterIndex) { f.Bytes = f.MaxBytes + 1 }, false},
		{"missing-cpu", func(f *kwFilterIndex) { f.CPU = "" }, false},
		{"zero-build", func(f *kwFilterIndex) { f.BuildSeconds = 0 }, false},
		{"negative-timing", func(f *kwFilterIndex) { f.DecisionNSBlocked = -1 }, false},
		{"zero-clean", func(f *kwFilterIndex) { f.DecisionNSClean = 0 }, false},
		{"nan", func(f *kwFilterIndex) { f.BuildSeconds = math.NaN() }, false},
		{"infinity", func(f *kwFilterIndex) { f.DecisionNSClean = math.Inf(1) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := validFilterIndex(now)
			tc.mutate(&f)
			err := kwValidateFilterIndex(&f, now, after)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
	if kwValidateFilterIndex(nil, now, after) == nil {
		t.Fatal("missing stats passed")
	}
}

type acceptanceRoundTrip func(*http.Request) (*http.Response, error)

func (f acceptanceRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func acceptanceJSON(v any) *http.Response {
	raw, _ := json.Marshal(v)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}
}

func TestAcceptanceFilterReport(t *testing.T) {
	for _, scenario := range []string{"complete", "disappeared-after-stats", "stale-stats", "empty-api", "disconnected-api", "missing-stats", "http-error", "changed-version"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now()
			expected, engines, versions := filterFleetFixture(now)
			gets, stats := 0, 0
			api := &harness.API{Base: "https://acceptance.invalid", HC: &http.Client{Transport: acceptanceRoundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/api/v1/engines" {
					gets++
					if scenario == "empty-api" || scenario == "disappeared-after-stats" && gets == 2 {
						return acceptanceJSON([]kwFilterEngine{}), nil
					}
					if scenario == "disconnected-api" {
						engines[0].Connected = false
					}
					if scenario == "changed-version" && gets == 2 {
						engines[0].AppliedVersion++
						engines[0].TargetVersion++
					}
					return acceptanceJSON(engines), nil
				}
				stats++
				if r.URL.RawQuery != "window=5m" {
					t.Errorf("unexpected query: %s", r.URL.RawQuery)
				}
				if scenario == "http-error" {
					return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("unavailable"))}, nil
				}
				if scenario == "missing-stats" {
					return acceptanceJSON(map[string]any{}), nil
				}
				fi := validFilterIndex(time.Now())
				if scenario == "stale-stats" {
					fi.At = now.Add(-time.Hour)
				}
				return acceptanceJSON(map[string]any{"filter_index": fi}), nil
			})}}
			report, err := kwCollectFilterReport(api, expected, versions, now)
			if scenario == "complete" {
				if err != nil || len(report) != 4 || stats != 4 || gets != 2 {
					t.Fatalf("report=%v err=%v gets=%d stats=%d", report, err, gets, stats)
				}
				for _, e := range expected {
					if _, ok := report[e.ID]; !ok {
						t.Fatalf("UUID %s missing", e.ID)
					}
				}
			} else if err == nil || report != nil {
				t.Fatalf("invalid fleet accepted: %v %v", report, err)
			}
		})
	}
}

func TestAcceptanceDeadline(t *testing.T) {
	if mgmtHARecoveryBudget != 10*time.Second {
		t.Fatal("HA contract must remain exactly 10s")
	}
	t.Run("expired-no-observation", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		err := acceptancePoll(ctx, time.Hour, func() error { t.Fatal("observed after deadline"); return nil })
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	})
	t.Run("late-success-rejected", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		err := acceptancePoll(ctx, time.Millisecond, func() error { cancel(); return nil })
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
	t.Run("poll-wait-bounded", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := acceptancePoll(ctx, time.Hour, func() error { return errors.New("not ready") })
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
			t.Fatalf("%v after %v", err, time.Since(start))
		}
	})
	t.Run("shared-http-deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		calls := 0
		api := &harness.API{Base: "https://acceptance.invalid", HC: &http.Client{Timeout: 30 * time.Second, Transport: acceptanceRoundTrip(func(r *http.Request) (*http.Response, error) {
			calls++
			deadline, _ := r.Context().Deadline()
			want, _ := ctx.Deadline()
			if !deadline.Equal(want) {
				t.Error("request got a fresh deadline")
			}
			if calls == 1 {
				return acceptanceJSON(map[string]any{}), nil
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		})}}
		bounded := acceptanceAPI(ctx, api)
		if _, err := bounded.Do("GET", "/upstreams", nil, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := bounded.Do("POST", "/upstreams", map[string]string{"name": "new"}, nil); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		if _, err := bounded.Do("GET", "/engines", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		if calls != 2 {
			t.Fatalf("made %d calls; expired phase must not start", calls)
		}
		if api.HC == bounded.HC || api.HC.Timeout != 30*time.Second {
			t.Fatal("changed setup client")
		}
	})
}

func TestAcceptanceReportCompletion(t *testing.T) {
	start := time.Now()
	expected, _, _ := filterFleetFixture(start)
	report := map[string]kwFilterIndex{}
	for _, e := range expected {
		report[e.ID] = validFilterIndex(start)
	}
	if err := kwValidateFilterReport(expected, report, start, start); err != nil {
		t.Fatal(err)
	}
	if err := kwValidateFilterReport(expected, report, start.Add(31*time.Second), start); err == nil {
		t.Fatal("samples aged out during collection accepted")
	}
	delete(report, expected[0].ID)
	if err := kwValidateFilterReport(expected, report, start, start); err == nil {
		t.Fatal("partial report accepted")
	}
	report["unexpected"] = validFilterIndex(start)
	if err := kwValidateFilterReport(expected, report, start, start); err == nil {
		t.Fatal("wrong UUID report accepted")
	}
	if err := kwValidateFilterReport(nil, nil, start, start); err == nil {
		t.Fatal("empty report accepted")
	}
}

type acceptanceContextBody struct{ ctx context.Context }

func (b acceptanceContextBody) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }
func (b acceptanceContextBody) Close() error             { return nil }

func TestAcceptanceResponseBodyDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	api := &harness.API{Base: "https://acceptance.invalid", HC: &http.Client{Transport: acceptanceRoundTrip(func(r *http.Request) (*http.Response, error) {
		// Headers arrive successfully; the body stalls until the request deadline.
		return &http.Response{StatusCode: 200, Body: acceptanceContextBody{r.Context()}, Header: make(http.Header)}, nil
	})}}
	start := time.Now()
	_, err := acceptanceAPI(ctx, api).Do("GET", "/engines", nil, new([]kwFilterEngine))
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("body read escaped deadline: %v after %v", err, time.Since(start))
	}
}

func TestAcceptanceDisruptionDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release, started, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- acceptanceDisrupt(ctx, func() { close(started); defer close(stopped); <-release })
	}()
	// Synchronize startup so expiration cannot bypass the callback and strand cleanup.
	<-started
	defer func() { close(release); <-stopped }()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("process wait ignored recovery context cancellation")
	}
}
