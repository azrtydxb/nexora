package harness_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/piwi3910/nexora/e2e/harness"
)

func TestOperationCoverageMatching(t *testing.T) {
	ops := harness.LoadOperations(t)
	if len(ops) < 40 {
		t.Fatalf("only %d operations loaded from openapi.yaml", len(ops))
	}
	for _, c := range []struct{ method, path, want string }{
		{"GET", "/upstreams", "listUpstreams"},
		{"PUT", "/upstreams/0b6c5f9e-6f0e-4c1e-9a55-2d9f1c7f0a11", "updateUpstream"},
		{"DELETE", "/users/abc?revision=3", "deleteUser"},
		{"POST", "/filter-lists/abc/refresh", "refreshFilterList"},
		{"GET", "/auth/oidc/callback?state=s&code=c", "oidcCallback"},
	} {
		if got, ok := harness.MatchOperation(ops, c.method, c.path); !ok || got != c.want {
			t.Errorf("MatchOperation(%s %s) = %q, %v; want %q", c.method, c.path, got, ok, c.want)
		}
	}
	for _, c := range []struct{ method, path string }{
		{"PUT", "/upstreams/"},       // a template segment must be non-empty
		{"PATCH", "/upstreams/abc"},  // method must match
		{"GET", "/filter-lists/a/b"}, // segment count must match
		{"GET", "/api/v1/upstreams"}, // paths are relative to /api/v1
	} {
		if got, ok := harness.MatchOperation(ops, c.method, c.path); ok {
			t.Errorf("MatchOperation(%s %s) matched %q", c.method, c.path, got)
		}
	}

	dir := t.TempDir()
	lines := `{"method":"GET","path":"/upstreams","test":"a"}
{"method":"DELETE","path":"/engines/abc","test":"b"}
{"method":"GET","path":"/not-an-operation","test":"c"}
`
	if err := os.WriteFile(filepath.Join(dir, "requests-0.jsonl"), []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	covered := harness.CoveredOperations(t, ops, dir)
	if len(covered) != 2 || !covered["listUpstreams"] || !covered["deleteEngine"] {
		t.Fatalf("covered = %v, want listUpstreams and deleteEngine only", covered)
	}
}
