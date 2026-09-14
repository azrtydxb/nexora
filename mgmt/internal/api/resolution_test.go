package api_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/api"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
)

func TestResolutionDnssecAndRPZAPI(t *testing.T) {
	op, viewer := roleClients(t)
	var e apiErr

	var res map[string]any
	if code := op.do(http.MethodGet, "/resolution", nil, &res); code != 200 || res["mode"] != "forward" || res["authority_port"].(float64) != 53 {
		t.Fatalf("get resolution = %d %v", code, res)
	}
	res["mode"] = "recursive"
	res["root_hints"] = []map[string]any{{"name": "a.root.test.", "addresses": []string{"127.0.53.1:53"}}}
	if code := op.do(http.MethodPut, "/resolution", res, &e); code != 400 || e.Code != "invalid_request" || !strings.Contains(e.Message, "not an IP address") {
		t.Fatalf("root hint with port = %d %+v", code, e)
	}
	res["root_hints"] = []map[string]any{{"name": "a.root.test.", "addresses": []string{"127.0.53.1"}}}
	if code := op.do(http.MethodPut, "/resolution", res, &res); code != 200 || res["revision"].(float64) != 2 {
		t.Fatalf("update resolution = %d %v", code, res)
	}
	if code := viewer.do(http.MethodPut, "/resolution", res, &e); code != 403 {
		t.Fatalf("viewer update = %d", code)
	}

	var anchors []map[string]any
	if code := viewer.do(http.MethodGet, "/dnssec/trust-anchors", nil, &anchors); code != 200 || len(anchors) != 2 || anchors[0]["source"] != "iana" {
		t.Fatalf("seeded anchors = %d %v", code, anchors)
	}
	if code := op.do(http.MethodDelete, "/dnssec/trust-anchors/"+anchors[0]["id"].(string), nil, nil); code != 204 {
		t.Fatalf("delete first root anchor = %d", code)
	}
	if code := op.do(http.MethodDelete, "/dnssec/trust-anchors/"+anchors[1]["id"].(string), nil, &e); code != 409 || e.Code != "last_root_anchor" {
		t.Fatalf("delete last root anchor = %d %+v", code, e)
	}

	far := time.Now().Add(31 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if code := op.do(http.MethodPost, "/dnssec/negative-trust-anchors", map[string]any{"domain": "broken.example", "reason": "x", "expires_at": far}, &e); code != 400 {
		t.Fatalf("NTA 31 days ahead = %d %+v", code, e)
	}
	soon := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	var nta map[string]any
	if code := op.do(http.MethodPost, "/dnssec/negative-trust-anchors", map[string]any{"domain": "Broken.Example", "reason": "x", "expires_at": soon}, &nta); code != 201 || nta["domain"] != "broken.example." || nta["created_by"] != "opal" {
		t.Fatalf("NTA = %d %v", code, nta)
	}

	var zone map[string]any
	if code := op.do(http.MethodPost, "/rpz-zones", map[string]any{"name": "rpz.file.test.", "source_type": "file", "policy_override": "given", "min_refresh_seconds": 60}, &zone); code != 201 {
		t.Fatalf("create file zone = %d %v", code, zone)
	}
	body := map[string]any{"content": "$INCLUDE /etc/passwd\n", "revision": zone["revision"]}
	if code := op.do(http.MethodPut, "/rpz-zones/"+zone["id"].(string)+"/file", body, &e); code != 400 || !strings.Contains(e.Message, "$INCLUDE is not allowed") {
		t.Fatalf("upload $INCLUDE = %d %+v", code, e)
	}
	transfer := map[string]any{"name": "rpz.axfr.test.", "source_type": "transfer", "primary": "127.0.0.1:5300", "tsig_key_name": "rpz-key.",
		"tsig_algorithm": "hmac-sha256", "tsig_secret": base64.StdEncoding.EncodeToString([]byte("fixture-tsig-key")), "policy_override": "given", "min_refresh_seconds": 60}
	if code := op.do(http.MethodPost, "/rpz-zones", transfer, &e); code != 503 || e.Code != "key_storage_unconfigured" {
		t.Fatalf("TSIG secret without NEXORA_KEK_FILE = %d %+v", code, e)
	}
	var zones []map[string]any
	if code := viewer.do(http.MethodGet, "/rpz-zones", nil, &zones); code != 200 || len(zones) != 1 {
		t.Fatalf("a refused create must not leave a row: %d %v", code, zones)
	}
}

func TestResolutionAndRPZLifecycleWithKeyStorage(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	kekPath := filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(kekPath, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.LoadKEKFile(kekPath)
	if err != nil {
		t.Fatal(err)
	}
	op, viewer, env := roleClientsWith(t, func(d *api.Deps) { d.Secrets = box })
	var e apiErr

	var fz map[string]any
	if code := op.do(http.MethodPost, "/forward-zones", map[string]any{"domain": "Corp.Example", "addresses": []string{"10.0.0.1:53"}, "validate": false}, &fz); code != 201 || fz["domain"] != "corp.example." {
		t.Fatalf("create forward zone = %d %v", code, fz)
	}
	if code := op.do(http.MethodPost, "/forward-zones", map[string]any{"domain": "corp.example.", "addresses": []string{"10.0.0.2:53"}, "validate": false}, &e); code != 409 {
		t.Fatalf("duplicate forward zone = %d %+v", code, e)
	}
	fz["addresses"], fz["validate"] = []string{"10.0.0.2:5353"}, true
	if code := op.do(http.MethodPut, "/forward-zones/"+fz["id"].(string), fz, &fz); code != 200 || fz["revision"].(float64) != 2 {
		t.Fatalf("update forward zone = %d %v", code, fz)
	}
	stale := map[string]any{"domain": fz["domain"], "addresses": fz["addresses"], "validate": true, "revision": 1}
	if code := op.do(http.MethodPut, "/forward-zones/"+fz["id"].(string), stale, &e); code != 409 {
		t.Fatalf("stale forward zone update = %d", code)
	}

	var ds map[string]any
	if code := viewer.do(http.MethodGet, "/dnssec/settings", nil, &ds); code != 200 || ds["validation"] != true || ds["validate_forwarded"] != true {
		t.Fatalf("default dnssec settings = %d %v", code, ds)
	}
	ds["validate_forwarded"] = false
	if code := op.do(http.MethodPut, "/dnssec/settings", ds, &ds); code != 200 || ds["validate_forwarded"] != false {
		t.Fatalf("update dnssec settings = %d %v", code, ds)
	}

	var file, transfer map[string]any
	if code := op.do(http.MethodPost, "/rpz-zones", map[string]any{"name": "rpz.file.test.", "source_type": "file", "policy_override": "nxdomain", "min_refresh_seconds": 60}, &file); code != 201 {
		t.Fatalf("create file zone = %d %v", code, file)
	}
	content := "$TTL 60\n@ SOA ns.rpz. hostmaster.rpz. 7 60 60 86400 60\n@ NS ns.rpz.\nbad.example CNAME .\n"
	if code := op.do(http.MethodPut, "/rpz-zones/"+file["id"].(string)+"/file", map[string]any{"content": content, "revision": file["revision"]}, &file); code != 200 || file["file_records"].(float64) != 1 {
		t.Fatalf("upload = %d %v", code, file)
	}
	if code := op.do(http.MethodPost, "/rpz-zones/"+file["id"].(string)+"/refresh", map[string]any{}, &e); code != 409 {
		t.Fatalf("refresh file zone = %d", code)
	}
	secret := []byte("fixture-tsig-key-0123")
	body := map[string]any{"name": "rpz.axfr.test.", "source_type": "transfer", "primary": "127.0.0.1:5300", "tsig_key_name": "rpz-key.",
		"tsig_algorithm": "hmac-sha256", "tsig_secret": base64.StdEncoding.EncodeToString(secret), "policy_override": "given", "min_refresh_seconds": 30}
	if code := op.do(http.MethodPost, "/rpz-zones", body, &transfer); code != 201 || transfer["tsig_secret_set"] != true || transfer["position"].(float64) != 2 {
		t.Fatalf("create transfer zone = %d %v", code, transfer)
	}
	if _, leaked := transfer["tsig_secret"]; leaked {
		t.Fatal("tsig_secret returned by the API")
	}
	update := map[string]any{"primary": "127.0.0.1:5301", "tsig_key_name": "rpz-key.", "tsig_algorithm": "hmac-sha512", "policy_override": "given", "min_refresh_seconds": 30, "revision": transfer["revision"]}
	if code := op.do(http.MethodPut, "/rpz-zones/"+transfer["id"].(string), update, &transfer); code != 200 || transfer["tsig_secret_set"] != true || transfer["tsig_algorithm"] != "hmac-sha512" {
		t.Fatalf("update keeping secret = %d %v", code, transfer)
	}
	if code := op.do(http.MethodPost, "/rpz-zones/"+transfer["id"].(string)+"/refresh", map[string]any{}, nil); code != 202 {
		t.Fatalf("refresh transfer zone = %d", code)
	}
	if code := op.do(http.MethodPut, "/rpz-zones/order", map[string]any{"ids": []string{transfer["id"].(string)}}, &e); code != 400 {
		t.Fatalf("partial reorder = %d", code)
	}
	if code := op.do(http.MethodPut, "/rpz-zones/order", map[string]any{"ids": []string{transfer["id"].(string), file["id"].(string)}}, nil); code != 204 {
		t.Fatalf("reorder = %d", code)
	}

	var stored []byte
	if err := env.st.Pool.QueryRow(env.ctx, "select tsig_secret_envelope from rpz_zones where name = 'rpz.axfr.test.'").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, secret) {
		t.Fatal("plaintext TSIG secret stored")
	}
	var audit string
	if err := env.st.Pool.QueryRow(env.ctx, "select string_agg(diff::text, ' ') from audit_log").Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(audit, "rpz.axfr.test.") || strings.Contains(audit, base64.StdEncoding.EncodeToString(secret)) {
		t.Fatal("audit rows must record the zone but never the TSIG secret")
	}

	_, snap, err := snapshot.Latest(env.ctx, env.st.Pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.RpzZones) != 2 || snap.RpzZones[0].Name != "rpz.axfr.test." || snap.RpzZones[0].RefreshNonce != 1 || snap.DnssecValidateForwarded ||
		len(snap.ForwardZones) != 1 || !snap.ForwardZones[0].Validate || len(snap.Dnssec.TrustAnchors) != 2 {
		t.Fatalf("snapshot = %v", snap)
	}

	var zones []map[string]any
	if code := viewer.do(http.MethodGet, "/rpz-zones", nil, &zones); code != 200 || len(zones) != 2 || zones[0]["name"] != "rpz.axfr.test." {
		t.Fatalf("list after reorder = %d %v", code, zones)
	}
	if code := op.do(http.MethodDelete, fmt.Sprintf("/rpz-zones/%s?revision=%v", file["id"], file["revision"]), nil, nil); code != 204 {
		t.Fatalf("delete file zone = %d", code)
	}
	if code := op.do(http.MethodDelete, fmt.Sprintf("/forward-zones/%s?revision=%v", fz["id"], fz["revision"]), nil, nil); code != 204 {
		t.Fatalf("delete forward zone = %d", code)
	}
}

func TestResolutionSettingsRecursorCacheMaxBytes(t *testing.T) {
	op, _, env := roleClientsWith(t, nil)
	var res map[string]any
	if code := op.do(http.MethodGet, "/resolution", nil, &res); code != 200 || res["recursor_cache_max_bytes"] != float64(64<<20) {
		t.Fatalf("default recursor_cache_max_bytes = %d %v", code, res["recursor_cache_max_bytes"])
	}
	var e apiErr
	res["recursor_cache_max_bytes"] = (4 << 20) - 1
	if code := op.do(http.MethodPut, "/resolution", res, &e); code != 400 || e.Code != "invalid_request" {
		t.Fatalf("below 4 MiB = %d %+v", code, e)
	}
	res["recursor_cache_max_bytes"] = 128 << 20
	if code := op.do(http.MethodPut, "/resolution", res, &res); code != 200 || res["recursor_cache_max_bytes"] != float64(128<<20) {
		t.Fatalf("update = %d %v", code, res)
	}
	_, snap, err := snapshot.Latest(env.ctx, env.st.Pool)
	if err != nil || snap.GetRecursion().GetCacheMaxBytes() != 128<<20 {
		t.Fatalf("snapshot recursion.cache_max_bytes = %d (%v)", snap.GetRecursion().GetCacheMaxBytes(), err)
	}
}
