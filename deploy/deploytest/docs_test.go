package deploytest

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestOperationsDoc checks that the operations guide and the README keep their required sections
// and that every repository path they mention in backticks exists.
func TestOperationsDoc(t *testing.T) {
	ops, err := os.ReadFile("../../docs/operations.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{
		"## Install with Helm", "## Install with Docker Compose", "## Upgrade", "## Backup and restore PostgreSQL",
		"## Engine groups and staged rollouts", "## Engine lifecycle", "## Monitoring and alerts", "## kw deployment",
		"## Install with the Kubernetes operator", "## PostgreSQL high availability and backups",
	} {
		if !strings.Contains(string(ops), "\n"+h+"\n") {
			t.Errorf("docs/operations.md lacks heading %q", h)
		}
	}
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Quick start", "docs/operations.md", "docs/architecture.md", "deploy/helm/nexora", "deploy/compose"} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("README.md lacks %q", want)
		}
	}
	paths := regexp.MustCompile("`((?:scripts|deploy|docs|mgmt|engine|e2e)/[A-Za-z0-9_./-]+)`")
	for _, doc := range [][]byte{ops, readme} {
		for _, m := range paths.FindAllSubmatch(doc, -1) {
			if _, err := os.Stat("../../" + string(m[1])); err != nil {
				t.Errorf("documented path %s does not exist", m[1])
			}
		}
	}
}
