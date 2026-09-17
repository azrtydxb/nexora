package deploytest

import (
	"os"
	"strings"
	"testing"
)

func TestToolboxPrivilegeIsExplicit(t *testing.T) {
	data, err := os.ReadFile("../dev/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	lastUser := ""
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.EqualFold(fields[0], "USER") {
			lastUser = fields[1]
		}
	}
	if lastUser != "dev" {
		t.Fatalf("toolbox image default user = %q, want dev", lastUser)
	}
	found := false
	for _, doc := range decodeFile(t, "../dev/dev-pod.yaml") {
		if doc["kind"] != "Deployment" {
			continue
		}
		for _, raw := range doc.path("spec", "template", "spec", "containers").([]any) {
			container := obj(raw.(map[string]any))
			if container["name"] != "toolbox" {
				continue
			}
			found = true
			if container.path("securityContext", "runAsUser") != 0 {
				t.Fatal("development fixture root requirement must be explicit")
			}
		}
	}
	if !found {
		t.Fatal("toolbox deployment not found")
	}
}
