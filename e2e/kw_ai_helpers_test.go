package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestKwAITaskDecodesFailure(t *testing.T) {
	var task kwAITask
	if err := json.Unmarshal([]byte(`{"id":"task-1","status":"failed","error_code":"timeout","error_message":"ai call timed out"}`), &task); err != nil {
		t.Fatal(err)
	}
	if task.ID != "task-1" || task.Status != "failed" || task.ErrorCode != "timeout" || task.ErrorMessage != "ai call timed out" {
		t.Fatalf("incorrect task decoding: %+v", task)
	}
}

func TestKwPromValueMixedPair(t *testing.T) {
	for _, value := range []string{`"42.5"`, `42.5`} {
		t.Run(value, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("query") != "sum(test_metric)" {
					t.Error("query not encoded correctly")
				}
				_, _ = fmt.Fprintf(w, `{"status":"success","data":{"result":[{"value":[1234.5,%s]}]}}`, value)
			}))
			defer srv.Close()
			t.Setenv("NEXORA_KW_PROMETHEUS_URL", srv.URL)
			if got := kwPromValue(t, "sum(test_metric)"); got != 42.5 {
				t.Fatalf("got %v, want 42.5", got)
			}
		})
	}
}
