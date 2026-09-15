package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/piwi3910/nexora/e2e/harness"
)

type engineLogs struct {
	Lines []struct {
		Seq     int64  `json:"seq"`
		Level   string `json:"level"`
		Message string `json:"message"`
	} `json:"lines"`
	LastSeq int64 `json:"last_seq"`
}

// TestEngineLogsFromRealEngine reads a managed engine's "applied version" line through getEngineLogs,
// then reads again after the cursor and gets only newer lines.
func TestEngineLogsFromRealEngine(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	mgmt := env.StartMgmt(pg, env.InitCA(), harness.MgmtOptions{})
	api := harness.Bootstrap(t, env, mgmt.SetupToken(t), mgmt.BaseURL)
	env.StartManagedEngine("log-engine", []string{mgmt.GRPCURL}, api.CreateJoinToken())
	v := api.LatestVersion()
	e := api.WaitEngine("log-engine", 15*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion == v })

	var logs engineLogs
	harness.EventuallyTrue(t, 15*time.Second, func() bool {
		code, _ := api.Do("GET", "/engines/"+e.ID+"/logs?q=applied%20version", nil, &logs)
		return code == 200 && len(logs.Lines) > 0
	}, "engine log line through getEngineLogs")
	if !strings.Contains(logs.Lines[0].Message, "applied version") || logs.Lines[0].Level != "info" {
		t.Fatalf("line: %+v", logs.Lines[0])
	}

	cursor := logs.LastSeq
	var after engineLogs
	if code, _ := api.Do("GET", fmt.Sprintf("/engines/%s/logs?after=%d", e.ID, cursor), nil, &after); code != 200 {
		t.Fatalf("cursor read -> %d", code)
	}
	for _, l := range after.Lines {
		if l.Seq <= cursor {
			t.Fatalf("cursor %d returned an old line: %+v", cursor, l)
		}
	}
}
