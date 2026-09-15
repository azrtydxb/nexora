package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/piwi3910/nexora/e2e/harness"
)

func TestBootstrapTokenFleetBootstrap(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	tokFile := filepath.Join(t.TempDir(), "token")
	const first = "nxt_DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD"
	const second = "nxt_EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE"
	if err := os.WriteFile(tokFile, []byte(first+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{ExtraEnv: []string{
		"NEXORA_BOOTSTRAP_TOKEN_FILE=" + tokFile, "NEXORA_BOOTSTRAP_TOKEN_RELOAD_INTERVAL=1s"}})
	api := env.NewAPI(mg.BaseURL)
	api.Bearer = first
	harness.EventuallyTrue(t, 10*time.Second, func() bool {
		code, _ := api.Do(http.MethodGet, "/engine-groups", nil, nil)
		return code == http.StatusOK
	}, "the bootstrap token authenticates")
	g := api.CreateEngineGroup(map[string]any{"name": "boot"})
	join := api.CreateJoinTokenFor(g.ID, nil)
	en := env.StartManagedEngine("boot-1", []string{mg.GRPCURL}, join)
	_ = en
	v := api.WaitEngine("boot-1", 30*time.Second, func(e harness.EngineView) bool { return e.Connected })
	if v.EngineGroupID != g.ID {
		t.Fatalf("engine enrolled into %s, want %s", v.EngineGroupID, g.ID)
	}
	var setup map[string]bool
	api.Must(http.MethodGet, "/setup", nil, &setup, http.StatusOK)
	if !setup["required"] {
		t.Fatal("setup is no longer required after the bootstrap token created a system user")
	}
	if err := os.WriteFile(tokFile, []byte(second), 0o600); err != nil {
		t.Fatal(err)
	}
	next := env.NewAPI(mg.BaseURL)
	next.Bearer = second
	harness.EventuallyTrue(t, 5*time.Second, func() bool {
		oldCode, _ := api.Do(http.MethodGet, "/engine-groups", nil, nil)
		newCode, _ := next.Do(http.MethodGet, "/engine-groups", nil, nil)
		return oldCode == http.StatusUnauthorized && newCode == http.StatusOK
	}, "the rotated token replaces the old one")
	next.CreateEngineGroup(map[string]any{"name": "boot2"})
}
