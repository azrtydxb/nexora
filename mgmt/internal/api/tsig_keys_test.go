package api_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/api"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
)

func TestTSIGKeyAPISecretsAreWriteOnly(t *testing.T) {
	box, err := secrets.Open(secrets.Config{KEKFile: harness.WriteKEK(t)})
	if err != nil {
		t.Fatal(err)
	}
	op, viewer, env := roleClientsWith(t, func(d *api.Deps) { d.Secrets, d.TSIGKeys.Box = box, box })
	admin := env.client(t)
	if code := admin.do(http.MethodPost, "/auth/login", map[string]string{"username": "admin", "password": "admin-password-1"}, nil); code != 200 {
		t.Fatalf("admin login = %d", code)
	}
	var e apiErr
	secret := base64.StdEncoding.EncodeToString([]byte("fixture-tsig-key-api-secret"))
	body := map[string]any{"name": "Xfr-Key.", "algorithm": "hmac-sha256", "secret": secret}
	if code := op.do(http.MethodPost, "/tsig-keys", body, &e); code != 403 {
		t.Fatalf("operator create = %d", code)
	}
	var created map[string]any
	if code := admin.do(http.MethodPost, "/tsig-keys", body, &created); code != 201 || created["secret"] != secret || created["name"] != "xfr-key." {
		t.Fatalf("admin create = %d %v", code, created["name"])
	}
	var generated map[string]any
	if code := admin.do(http.MethodPost, "/tsig-keys", map[string]any{"name": "gen.", "algorithm": "hmac-sha384"}, &generated); code != 201 {
		t.Fatalf("generated create = %d", code)
	}
	if raw, _ := base64.StdEncoding.DecodeString(generated["secret"].(string)); len(raw) != 48 {
		t.Fatalf("generated hmac-sha384 secret is %d bytes", len(raw))
	}
	if code := admin.do(http.MethodPost, "/tsig-keys", map[string]any{"name": "md5.", "algorithm": "hmac-md5"}, &e); code != 400 {
		t.Fatalf("hmac-md5 = %d %+v", code, e)
	}
	if code := admin.do(http.MethodPost, "/tsig-keys", map[string]any{"name": "bad name", "algorithm": "hmac-sha256"}, &e); code != 400 || e.Code != "invalid_request" {
		t.Fatalf("bad name = %d %+v", code, e)
	}
	if code := admin.do(http.MethodPost, "/tsig-keys", body, &e); code != 409 || e.Code != "conflict" {
		t.Fatalf("duplicate = %d %+v", code, e)
	}

	// Secrets never come back: not from the list, not from the audit log.
	var list json.RawMessage
	if code := viewer.do(http.MethodGet, "/tsig-keys", nil, &list); code != 200 || !bytes.Contains(list, []byte("xfr-key.")) {
		t.Fatalf("viewer list = %d %s", code, list)
	}
	var audit json.RawMessage
	if code := admin.do(http.MethodGet, "/audit", nil, &audit); code != 200 || !bytes.Contains(audit, []byte("createTsigKey")) {
		t.Fatalf("audit = %d %s", code, audit)
	}
	for what, raw := range map[string][]byte{"list": list, "audit": audit} {
		for _, s := range []string{secret, generated["secret"].(string)} {
			if bytes.Contains(raw, []byte(s)) {
				t.Fatalf("TSIG secret returned by %s", what)
			}
		}
	}

	id, gen := created["id"].(string), generated["id"].(string)
	zone := map[string]any{"name": "t.test.", "kind": "primary", "default_ttl": 300, "soa": map[string]any{"mname": "ns.t.test.", "rname": "h.t.test."},
		"nameservers": []string{"ns.t.test."}, "transfer": map[string]any{"allow_cidrs": []string{}, "tsig_key_id": id}}
	if code := op.do(http.MethodPost, "/zones", zone, &e); code != 201 {
		t.Fatalf("zone with transfer key = %d %+v", code, e)
	}
	if code := admin.do(http.MethodDelete, "/tsig-keys/"+id+"?revision=1", nil, &e); code != 409 || e.Code != "tsig_key_in_use" {
		t.Fatalf("delete in-use key = %d %+v", code, e)
	}
	if code := op.do(http.MethodDelete, "/tsig-keys/"+gen+"?revision=1", nil, &e); code != 403 {
		t.Fatalf("operator delete = %d", code)
	}
	if code := admin.do(http.MethodDelete, "/tsig-keys/"+gen+"?revision=2", nil, &e); code != 409 || e.Code != "conflict" {
		t.Fatalf("stale delete = %d %+v", code, e)
	}
	if code := admin.do(http.MethodDelete, "/tsig-keys/"+gen+"?revision=1", nil, nil); code != 204 {
		t.Fatalf("delete = %d", code)
	}
}

func TestTSIGKeyCreateWithoutKeyStorageIs503(t *testing.T) {
	_, viewer, env := roleClientsWith(t, nil)
	admin := env.client(t)
	if code := admin.do(http.MethodPost, "/auth/login", map[string]string{"username": "admin", "password": "admin-password-1"}, nil); code != 200 {
		t.Fatalf("admin login = %d", code)
	}
	var e apiErr
	if code := admin.do(http.MethodPost, "/tsig-keys", map[string]any{"name": "k.", "algorithm": "hmac-sha256"}, &e); code != 503 || e.Code != "key_storage_unconfigured" {
		t.Fatalf("create without key storage = %d %+v", code, e)
	}
	var keys []any
	if code := viewer.do(http.MethodGet, "/tsig-keys", nil, &keys); code != 200 || len(keys) != 0 {
		t.Fatalf("a refused create must not leave a row: %d %v", code, keys)
	}
}
