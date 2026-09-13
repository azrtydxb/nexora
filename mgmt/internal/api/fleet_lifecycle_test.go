package api_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/pki"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

// TestEngineLifecycleAuditAndHashedJoinTokens: join token secrets are stored only as their hash,
// revocation revokes every certificate and notifies the instances, and every lifecycle action
// through the API is audited.
func TestEngineLifecycleAuditAndHashedJoinTokens(t *testing.T) {
	e := newAPI(t)
	admin := e.client(t)
	if code := admin.do("POST", "/setup", map[string]string{"token": e.setup, "username": "admin", "email": "a@x", "password": "admin-password-1"}, nil); code != 201 {
		t.Fatalf("setup -> %d", code)
	}
	var group map[string]any
	if code := admin.do("POST", "/engine-groups", map[string]any{"name": "life"}, &group); code != 201 {
		t.Fatalf("create engine group -> %d %v", code, group)
	}
	groupID := group["id"].(string)

	var created map[string]any
	if code := admin.do("POST", "/join-tokens", map[string]any{"name": "life", "ttl_seconds": 600, "engine_group_id": groupID, "max_uses": 1,
		"labels": map[string]string{"site": "lab"}}, &created); code != 201 {
		t.Fatalf("create join token -> %d %v", code, created)
	}
	secret, _, err := pki.ParseJoinToken(created["token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	var hash []byte
	var row string
	if err := e.st.Pool.QueryRow(e.ctx, "select secret_hash, row_to_json(j)::text from join_tokens j").Scan(&hash, &row); err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256([]byte(secret)); string(hash) != string(sum[:]) {
		t.Fatal("join token secret_hash is not the SHA-256 of the secret")
	}
	if strings.Contains(row, secret) {
		t.Fatal("the join token secret is stored in plaintext")
	}
	var apiErr map[string]string
	if code := admin.do("POST", "/join-tokens", map[string]any{"name": "bad", "ttl_seconds": 600, "labels": map[string]string{"Bad Key": "x"}}, &apiErr); code != 400 || apiErr["code"] != "invalid_labels" {
		t.Fatalf("invalid labels -> %d %v", code, apiErr)
	}

	id := storetest.InsertEngine(t, e.st, "life-1", store.DefaultEngineGroupID)
	if _, err := e.st.Pool.Exec(e.ctx, `insert into engine_certificates (serial, engine_id, not_before, not_after)
		values ('abc1', $1, now() - interval '1 hour', now() + interval '1 day')`, id); err != nil {
		t.Fatal(err)
	}
	var engine map[string]any
	if code := admin.do("PATCH", "/engines/"+id.String(), map[string]any{"revision": 1, "engine_group_id": groupID, "labels": map[string]string{"nexora.io/canary": "true"}}, &engine); code != 200 || engine["engine_group_name"] != "life" || engine["revision"].(float64) != 2 {
		t.Fatalf("update engine -> %d %v", code, engine)
	}
	if code := admin.do("PATCH", "/engines/"+id.String(), map[string]any{"revision": 1}, &apiErr); code != 409 || apiErr["code"] != "conflict" {
		t.Fatalf("stale engine revision -> %d %v", code, apiErr)
	}
	if code := admin.do("POST", "/engines/"+id.String()+"/rotate-certificate", nil, &engine); code != 202 || engine["cert_rotate_requested_at"] == nil {
		t.Fatalf("rotate -> %d %v", code, engine)
	}

	// Positive path first: the notification arrives on a listening connection.
	conn, err := pgx.Connect(e.ctx, e.pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.WithoutCancel(e.ctx))
	if _, err := conn.Exec(e.ctx, "listen nexora_engine_revoked"); err != nil {
		t.Fatal(err)
	}
	if code := admin.do("POST", "/engines/"+id.String()+"/revoke", nil, &engine); code != 200 || engine["status"] != "revoked" || engine["revoked_at"] == nil {
		t.Fatalf("revoke -> %d %v", code, engine)
	}
	wctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
	n, err := conn.WaitForNotification(wctx)
	cancel()
	if err != nil || n.Payload != id.String() {
		t.Fatalf("revocation notification: %v %v", n, err)
	}
	var unrevoked int
	if err := e.st.Pool.QueryRow(e.ctx, "select count(*) from engine_certificates where engine_id = $1 and revoked_at is null", id).Scan(&unrevoked); err != nil || unrevoked != 0 {
		t.Fatalf("certificates still valid after revocation: %d %v", unrevoked, err)
	}
	for _, op := range []string{"/revoke", "/rotate-certificate"} {
		if code := admin.do("POST", "/engines/"+id.String()+op, nil, &apiErr); code != 409 || apiErr["code"] != "engine_revoked" {
			t.Fatalf("%s on a revoked engine -> %d %v", op, code, apiErr)
		}
	}
	if code := admin.do("PATCH", "/engines/"+id.String(), map[string]any{"revision": 3, "labels": map[string]string{}}, &apiErr); code != 409 || apiErr["code"] != "engine_revoked" {
		t.Fatalf("update revoked engine -> %d %v", code, apiErr)
	}
	if code := admin.do("DELETE", "/engines/"+id.String(), nil, nil); code != 204 {
		t.Fatalf("delete engine -> %d", code)
	}
	if code := admin.do("DELETE", "/join-tokens/"+created["join_token"].(map[string]any)["id"].(string), nil, nil); code != 204 {
		t.Fatalf("revoke join token -> %d", code)
	}
	admin.do("GET", "/engine-groups/"+groupID, nil, &group)
	if code := admin.do("DELETE", fmt.Sprintf("/engine-groups/%s?revision=%d", groupID, int64(group["revision"].(float64))), nil, &apiErr); code != 204 {
		t.Fatalf("delete empty engine group -> %d %v", code, apiErr)
	}

	var audit []map[string]any
	admin.do("GET", "/audit?limit=200", nil, &audit)
	actions := map[string]bool{}
	for _, a := range audit {
		if a["actor_name"] == "admin" {
			actions[a["action"].(string)] = true
		}
	}
	for _, want := range []string{"createEngineGroup", "createJoinToken", "updateEngine", "rotateEngineCertificate", "revokeEngine",
		"deleteEngine", "revokeJoinToken", "deleteEngineGroup"} {
		if !actions[want] {
			t.Errorf("audit log lacks %s: %v", want, actions)
		}
	}
}
