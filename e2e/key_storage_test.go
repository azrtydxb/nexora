package e2e

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/e2e/harness"
)

// TestTSIGKeysWithPKCS11OnlyKeyStorage starts a management plane whose only key storage is a
// SoftHSM2 token: startup creates the token wrap key and TSIG secrets are sealed under it.
func TestTSIGKeysWithPKCS11OnlyKeyStorage(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	tok := harness.InitSoftHSM(t, "nexora-e2e")
	mgmt := env.StartMgmt(pg, ca, harness.MgmtOptions{ExtraEnv: tok.Env()})
	api := harness.Bootstrap(t, env, mgmt.SetupToken(t), mgmt.BaseURL)
	var created struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	api.Must(http.MethodPost, "/tsig-keys", map[string]any{"name": "hsm-key.", "algorithm": "hmac-sha256"}, &created, http.StatusCreated)
	if created.Secret == "" {
		t.Fatal("create did not return the generated secret")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var wrap []byte
	if err := conn.QueryRow(ctx, `SELECT substring(secret_envelope from 5 for 1) FROM tsig_keys WHERE id = $1`, created.ID).Scan(&wrap); err != nil {
		t.Fatal(err)
	}
	if len(wrap) != 1 || wrap[0] != 2 {
		t.Fatalf("envelope wrap byte = %v, want 2 (PKCS#11)", wrap)
	}

	bare := env.StartMgmt(env.StartPostgres(), ca, harness.MgmtOptions{})
	bareAPI := harness.Bootstrap(t, env, bare.SetupToken(t), bare.BaseURL)
	// Do reports non-2xx responses as an error carrying the body.
	if code, err := bareAPI.Do(http.MethodPost, "/tsig-keys", map[string]any{"name": "k.", "algorithm": "hmac-sha256"}, nil); code != http.StatusServiceUnavailable || err == nil || !strings.Contains(err.Error(), "key_storage_unconfigured") {
		t.Fatalf("create without key storage = %d %v", code, err)
	}
}
