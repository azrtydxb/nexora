package deploytest

import (
	"os"
	"strings"
	"testing"
)

func TestArchitectureDocNamesPlatform(t *testing.T) {
	b, err := os.ReadFile("../../docs/architecture.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	i := strings.Index(doc, "\n## Platform (M9)\n")
	if i < 0 {
		t.Fatal("docs/architecture.md lacks ## Platform (M9)")
	}
	section := doc[i:]
	if j := strings.Index(section[1:], "\n## "); j >= 0 {
		section = section[:j+1]
	}
	for _, want := range []string{"NexoraInstallation", "NexoraEngineGroup", "NEXORA_BOOTSTRAP_TOKEN_FILE", "01000_system_users.sql", "nexora.io/installation", "barmanObjectStore"} {
		if !strings.Contains(section, want) {
			t.Errorf("## Platform (M9) does not mention %s", want)
		}
	}
}
