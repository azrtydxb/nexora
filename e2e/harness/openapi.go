package harness

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// Operation is one OpenAPI operation of mgmt/api/openapi.yaml.
type Operation struct {
	ID, Method, Path string
}

// RepoRoot returns the repository root (the directory holding go.mod), found by walking up from
// the working directory.
func RepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the working directory")
		}
		dir = parent
	}
}

var httpMethods = map[string]bool{"get": true, "put": true, "post": true, "delete": true, "patch": true, "head": true, "options": true}

// LoadOperations reads every operation of <repo>/mgmt/api/openapi.yaml, sorted by path and method.
func LoadOperations(t *testing.T) []Operation {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(RepoRoot(t), "mgmt", "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}
	var ops []Operation
	for path, item := range doc.Paths {
		for method, node := range item {
			if !httpMethods[method] {
				continue
			}
			var op struct {
				OperationID string `yaml:"operationId"`
			}
			if err := node.Decode(&op); err != nil || op.OperationID == "" {
				t.Fatalf("openapi.yaml: %s %s has no operationId (%v)", method, path, err)
			}
			ops = append(ops, Operation{ID: op.OperationID, Method: strings.ToUpper(method), Path: path})
		}
	}
	if len(ops) == 0 {
		t.Fatal("openapi.yaml declares no operations")
	}
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].Path != ops[j].Path {
			return ops[i].Path < ops[j].Path
		}
		return ops[i].Method < ops[j].Method
	})
	return ops
}

// MatchOperation returns the ID of the operation serving method and path (relative to /api/v1).
// A `{x}` template segment matches one non-empty segment; a query string is ignored.
func MatchOperation(ops []Operation, method, path string) (string, bool) {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	got := strings.Split(path, "/")
	for _, op := range ops {
		if !strings.EqualFold(op.Method, method) {
			continue
		}
		want := strings.Split(op.Path, "/")
		if len(want) != len(got) {
			continue
		}
		ok := true
		for i, w := range want {
			if strings.HasPrefix(w, "{") && strings.HasSuffix(w, "}") {
				ok = got[i] != ""
			} else {
				ok = w == got[i]
			}
			if !ok {
				break
			}
		}
		if ok {
			return op.ID, true
		}
	}
	return "", false
}

// CoveredOperations reads every requests-*.jsonl in coverageDir (written by web/e2e/fixtures.ts)
// and returns the IDs of the operations those requests hit.
func CoveredOperations(t *testing.T, ops []Operation, coverageDir string) map[string]bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(coverageDir, "requests-*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	covered := map[string]bool{}
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			var r struct{ Method, Path string }
			if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
				f.Close()
				t.Fatalf("%s: %v", name, err)
			}
			if id, ok := MatchOperation(ops, r.Method, r.Path); ok {
				covered[id] = true
			}
		}
		err = sc.Err()
		f.Close()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	return covered
}
