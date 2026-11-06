// Package deploytest checks the deployment artifacts (workflow, compose, Helm chart, docs) statically.
package deploytest

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type step struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
}

type job struct {
	RunsOn         string `yaml:"runs-on"`
	Needs          any    `yaml:"needs"`
	TimeoutMinutes int    `yaml:"timeout-minutes"`
	Strategy       struct {
		Matrix map[string]any `yaml:"matrix"`
	} `yaml:"strategy"`
	Steps []step `yaml:"steps"`
}

func TestImagesWorkflow(t *testing.T) {
	raw, err := os.ReadFile("../../.github/workflows/images.yml")
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]job `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	build := wf.Jobs["build"]
	images := map[string]bool{}
	for _, im := range build.Strategy.Matrix["image"].([]any) {
		images[im.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"nexora-engine", "nexora-mgmt", "nexora-operator"} {
		if !images[want] {
			t.Errorf("build matrix lacks image %s", want)
		}
	}
	if build.RunsOn != "ubuntu-latest" {
		t.Errorf("build runs-on %q, want a GitHub-hosted ubuntu-latest", build.RunsOn)
	}
	// Actions are referenced by version tag (policy), never by commit hash.
	tagged := regexp.MustCompile(`^[^@]+@v[0-9][0-9.]*$|^azrtydxb/\.github/.+@main$`)
	var all strings.Builder
	for name, j := range wf.Jobs {
		if j.TimeoutMinutes == 0 {
			t.Errorf("job %s has no timeout-minutes", name)
		}
		for _, s := range j.Steps {
			if s.Uses != "" && !tagged.MatchString(s.Uses) {
				t.Errorf("job %s step %q does not use a version tag: %s", name, s.Name, s.Uses)
			}
			all.WriteString(s.Run)
			for _, v := range s.With {
				all.WriteString(v)
			}
		}
	}
	for _, want := range []string{"linux/amd64,linux/arm64", "ant.azrty.com/oci-hosted/azrtydxb", "ant-setup-action", "imagetools inspect", "sha-${GITHUB_SHA::7}",
		`grep -q '"arm64"'`, `grep -q '"amd64"'`} {
		if !strings.Contains(all.String()+string(raw), want) {
			t.Errorf("workflow does not contain %q", want)
		}
	}
}
