package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// raw performs an unparsed request and returns the response; the caller closes its body.
func (c *client) raw(method, path string) *http.Response {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.base+path, nil)
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp
}

// TestExportZoneFileStreams catches an export that buffers the zone to announce a Content-Length.
func TestExportZoneFileStreams(t *testing.T) {
	op, _ := roleClients(t)
	var e apiErr
	var z map[string]any
	in := map[string]any{"name": "export.test.", "kind": "primary", "default_ttl": 300,
		"soa": map[string]any{"mname": "ns.export.test.", "rname": "h.export.test."}, "nameservers": []string{"ns.export.test."}}
	if code := op.do(http.MethodPost, "/zones", in, &z); code != 201 {
		t.Fatalf("create zone = %d %v", code, z)
	}
	id := z["id"].(string)
	// net/http announces a Content-Length by itself when the whole body fits its 2 KiB buffer
	// before the handler returns, so the one record is larger than that.
	chunk := `"` + strings.Repeat("x", 255) + `" `
	rec := map[string]any{"name": "www.export.test.", "type": "TXT", "ttl": 300, "data": strings.Repeat(chunk, 12)}
	if code := op.do(http.MethodPost, "/zones/"+id+"/records", rec, &e); code != 201 {
		t.Fatalf("create record = %d %+v", code, e)
	}
	resp := op.raw(http.MethodGet, "/zones/"+id+"/export")
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("export = %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Content-Disposition") == "" {
		t.Fatal("Content-Disposition missing")
	}
	if resp.ContentLength != -1 {
		t.Fatalf("ContentLength = %d, want -1", resp.ContentLength)
	}
	if !strings.Contains(string(body), "www") {
		t.Fatalf("body lacks the record:\n%s", body)
	}
}
