package kwrollout

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckManagementFleet(t *testing.T) {
	for name, mutate := range map[string]func(*[]managedEngine){
		"healthy":      func(*[]managedEngine) {},
		"disconnected": func(e *[]managedEngine) { (*e)[0].Connected = false },
		"behind":       func(e *[]managedEngine) { (*e)[0].AppliedVersion-- },
		"ahead":        func(e *[]managedEngine) { (*e)[0].AppliedVersion++ },
		"old-target":   func(e *[]managedEngine) { (*e)[0].TargetVersion-- },
		"wrong-node":   func(e *[]managedEngine) { (*e)[0].NodeName = "other-node" },
		"wrong-group":  func(e *[]managedEngine) { (*e)[0].GroupID = "other-group" },
		"persist":      func(e *[]managedEngine) { (*e)[0].PersistError = "disk full" },
		"rejected":     func(e *[]managedEngine) { (*e)[0].RejectedReason = "invalid config" },
		"stale": func(e *[]managedEngine) {
			stale := time.Now().Add(-time.Minute)
			(*e)[0].LastSeenAt = &stale
		},
		"unknown-last-seen": func(e *[]managedEngine) { (*e)[0].LastSeenAt = nil },
		"revoked":           func(e *[]managedEngine) { (*e)[0].RevokedAt = (*e)[0].LastSeenAt },
		"missing":           func(e *[]managedEngine) { *e = (*e)[1:] },
		"duplicate":         func(e *[]managedEngine) { *e = append(*e, (*e)[0]) },
		"unexpected-connected": func(e *[]managedEngine) {
			extra := (*e)[0]
			extra.ID = "unexpected"
			*e = append(*e, extra)
		},
	} {
		t.Run(name, func(t *testing.T) {
			engines, expected := healthyManagementFixture()
			mutate(&engines)
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch r.URL.RequestURI() {
				case "/api/v1/config-versions?limit=1":
					_, _ = w.Write([]byte(`[{"version":123}]`))
				case "/api/v1/engines":
					_ = json.NewEncoder(w).Encode(engines)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			err := CheckManagement(context.Background(), server.Client(), server.URL, expected)
			if (err == nil) != (name == "healthy") {
				t.Fatalf("error = %v", err)
			}
			if name == "healthy" && calls.Load() != 3 {
				t.Fatalf("missing before/after config check: %d requests", calls.Load())
			}
		})
	}
}

func TestCheckManagementRejectsConfigDrift(t *testing.T) {
	engines, expected := healthyManagementFixture()
	var versions atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/engines" {
			_ = json.NewEncoder(w).Encode(engines)
			return
		}
		version := 122 + versions.Add(1)
		_ = json.NewEncoder(w).Encode([]map[string]int32{{"version": version}})
	}))
	defer server.Close()
	if err := CheckManagement(context.Background(), server.Client(), server.URL, expected); err == nil || !strings.Contains(err.Error(), "changed during") {
		t.Fatalf("error = %v", err)
	}
}

func TestCheckManagementRejectsHTTPFailuresAndRedirects(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable, http.StatusFound} {
		var calls atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Location", "/do-not-follow")
			w.WriteHeader(status)
			_, _ = w.Write([]byte("sensitive-server-response"))
		}))
		_, expected := healthyManagementFixture()
		err := CheckManagement(context.Background(), server.Client(), server.URL, expected)
		server.Close()
		if err == nil || strings.Contains(err.Error(), "sensitive-server-response") || calls.Load() != 1 {
			t.Fatalf("status=%d error=%v requests=%d", status, err, calls.Load())
		}
	}
}

func TestCheckManagementCancellationInterruptsBody(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("["))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, expected := healthyManagementFixture()
	done := make(chan error, 1)
	go func() { done <- CheckManagement(ctx, server.Client(), server.URL, expected) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not reach server")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not stop management body read")
	}
}

func healthyManagementFixture() ([]managedEngine, []EngineBinding) {
	now := time.Now()
	var engines []managedEngine
	var expected []EngineBinding
	for _, id := range []string{"a", "b"} {
		engines = append(engines, managedEngine{ID: id, NodeName: "node-" + id, GroupID: "default", Connected: true, Status: "current", AppliedVersion: 123, TargetVersion: 123, LastSeenAt: &now})
		expected = append(expected, EngineBinding{ID: id, NodeName: "node-" + id, GroupID: "default"})
	}
	return engines, expected
}
