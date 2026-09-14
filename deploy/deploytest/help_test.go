package deploytest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var opsRef = regexp.MustCompile(`^<!-- operations: (.+) -->`)

// TestHelpTopicsReferenceOperationsDoc checks that every in-app help topic names the
// docs/operations.md sections it restates, and that those headings exist.
func TestHelpTopicsReferenceOperationsDoc(t *testing.T) {
	root := filepath.Join("..", "..")
	ops, err := os.ReadFile(filepath.Join(root, "docs/operations.md"))
	if err != nil {
		t.Fatal(err)
	}
	headings := map[string]bool{}
	for _, l := range strings.Split(string(ops), "\n") {
		if strings.HasPrefix(l, "#") {
			headings[strings.TrimSpace(strings.TrimLeft(l, "#"))] = true
		}
	}
	topics, _ := filepath.Glob(filepath.Join(root, "web/src/help/topics/*.md"))
	if len(topics) < 8 {
		t.Fatalf("want 8 help topics, have %d", len(topics))
	}
	for _, p := range topics {
		b, _ := os.ReadFile(p)
		m := opsRef.FindStringSubmatch(strings.SplitN(string(b), "\n", 2)[0])
		if m == nil {
			t.Errorf("%s does not start with <!-- operations: ... -->", filepath.Base(p))
			continue
		}
		for _, h := range strings.Split(m[1], ";") {
			if !headings[strings.TrimSpace(h)] {
				t.Errorf("%s names missing docs/operations.md heading %q", filepath.Base(p), h)
			}
		}
	}
}
