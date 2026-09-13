package e2e

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestZoneFileRoundTrip(t *testing.T) {
	e := startAuthEnv(t, nil)
	api := e.api

	original, err := os.ReadFile("testdata/zones/roundtrip.test.zone")
	if err != nil {
		t.Fatal(err)
	}
	z := createPrimaryZone(t, api, "roundtrip.test.", nil)
	var imported struct {
		Zone            zoneResp `json:"zone"`
		RecordsImported int      `json:"records_imported"`
	}
	api.Must(http.MethodPost, "/zones/"+z.ID+"/import", map[string]any{"revision": z.Revision, "content": string(original)}, &imported, http.StatusOK)
	if imported.RecordsImported != 24 || imported.Zone.Serial != 2026091301 {
		t.Fatalf("import: %+v", imported)
	}

	// negative after positive: a stale revision is a conflict, not a lost write
	if status, _ := api.Do(http.MethodPost, "/zones/"+z.ID+"/import", map[string]any{"revision": z.Revision, "content": string(original)}, nil); status != http.StatusConflict {
		t.Fatalf("stale import: status %d, want 409", status)
	}

	req, _ := http.NewRequest(http.MethodGet, api.Base+"/api/v1/zones/"+z.ID+"/export", nil)
	req.Header.Set("Authorization", "Bearer "+api.Bearer)
	resp, err := api.HC.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("export: %v %v", err, resp)
	}
	exported, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	dir := t.TempDir()
	a, b := filepath.Join(dir, "original.zone"), filepath.Join(dir, "exported.zone")
	if err := os.WriteFile(a, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, exported, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ldns-compare-zones", "-a", "-s", "-e", a, b).CombinedOutput()
	if err != nil {
		t.Fatalf("zones differ (exit %v):\n%s\n--- exported ---\n%s", err, out, exported)
	}
	if !strings.Contains(string(out), "+0") || !strings.Contains(string(out), "-0") || !strings.Contains(string(out), "~0") {
		t.Fatalf("unexpected ldns-compare-zones output: %s", out)
	}
}
