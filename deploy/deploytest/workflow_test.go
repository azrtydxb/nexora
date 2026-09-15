// Package deploytest checks the deployment artifacts (workflow, compose, Helm chart, docs) statically.
package deploytest

import (
	"fmt"
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
	build, merge := wf.Jobs["build"], wf.Jobs["merge"]
	images, archs := map[string]bool{}, map[string]bool{}
	for _, im := range build.Strategy.Matrix["image"].([]any) {
		images[im.(map[string]any)["name"].(string)] = true
	}
	for _, a := range build.Strategy.Matrix["arch"].([]any) {
		m := a.(map[string]any)
		archs[fmt.Sprint(m["runner"], "|", m["platform"])] = true
	}
	for _, want := range []string{"nexora-engine", "nexora-mgmt", "nexora-operator"} {
		if !images[want] {
			t.Errorf("build matrix lacks image %s", want)
		}
	}
	for _, want := range []string{"arc-azrtydxb-publish|linux/arm64", "arc-azrtydxb-amd64-publish|linux/amd64"} {
		if !archs[want] {
			t.Errorf("build matrix lacks %s", want)
		}
	}
	if build.RunsOn != "${{ matrix.arch.runner }}" || merge.RunsOn != "arc-azrtydxb-publish" || merge.Needs != "build" {
		t.Errorf("runs-on/needs: build %q merge %q needs %v", build.RunsOn, merge.RunsOn, merge.Needs)
	}
	pinned := regexp.MustCompile(`^[^@]+@[0-9a-f]{40}$`)
	var all strings.Builder
	for name, j := range wf.Jobs {
		if j.TimeoutMinutes == 0 {
			t.Errorf("job %s has no timeout-minutes", name)
		}
		for _, s := range j.Steps {
			if s.Uses != "" && !pinned.MatchString(s.Uses) {
				t.Errorf("job %s step %q uses unpinned action %s", name, s.Name, s.Uses)
			}
			all.WriteString(s.Run)
			for _, v := range s.With {
				all.WriteString(v)
			}
		}
	}
	for _, want := range []string{"push-by-digest=true", "192.168.10.131:5000/azrtydxb", "imagetools create", "sha-${GITHUB_SHA::7}",
		`refs/heads/main`, `:main"`, `grep -q '"arm64"'`, `grep -q '"amd64"'`} {
		if !strings.Contains(all.String()+string(raw), want) {
			t.Errorf("workflow does not contain %q", want)
		}
	}
}
