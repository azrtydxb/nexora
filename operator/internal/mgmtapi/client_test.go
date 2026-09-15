package mgmtapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/piwi3910/nexora/operator/internal/mgmtapi"
)

func TestClientSendsBearerAndMapsErrors(t *testing.T) {
	status := http.StatusOK
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`{"code":"conflict","message":"stale revision"}`))
	}))
	defer srv.Close()
	c, err := mgmtapi.New(srv.URL, "nxt_TEST", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := c.EngineGroups(ctx); err != nil || auth != "Bearer nxt_TEST" || path != "/api/v1/engine-groups" {
		t.Fatalf("list: err=%v auth=%q path=%q", err, auth, path)
	}
	for code, want := range map[int]error{401: mgmtapi.ErrUnauthorized, 404: mgmtapi.ErrNotFound, 409: mgmtapi.ErrConflict, 503: mgmtapi.ErrUnavailable} {
		status = code
		_, err := c.EngineGroups(ctx)
		var apiErr *mgmtapi.APIError
		if !errors.Is(err, want) || !errors.As(err, &apiErr) || apiErr.Code != "conflict" || apiErr.Status != code {
			t.Errorf("status %d: err=%v", code, err)
		}
	}
	srv.Close()
	if _, err := c.EngineGroups(ctx); !errors.Is(err, mgmtapi.ErrUnavailable) {
		t.Errorf("transport failure: %v", err)
	}
}
