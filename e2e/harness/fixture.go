package harness

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

var fixtureReady = regexp.MustCompile(`fixture ready`)

// OIDCUser is a user the OIDC fixture can sign in.
type OIDCUser struct {
	Username string   `json:"username"`
	Email    string   `json:"email"`
	Groups   []string `json:"groups"`
}

// OIDCFixture is a running minimal OIDC provider.
type OIDCFixture struct {
	Issuer, ClientID, ClientSecretFile string
	Proc                               *Proc
}

// StartOIDCFixture starts `nexora-fixture oidc` for users with client id `nexora` and a random
// client secret written to a file.
func (e *Env) StartOIDCFixture(users ...OIDCUser) *OIDCFixture {
	e.T.Helper()
	dir, err := os.MkdirTemp(e.Dir, "oidc-")
	if err != nil {
		e.T.Fatal(err)
	}
	usersJSON, err := json.Marshal(users)
	if err != nil {
		e.T.Fatal(err)
	}
	usersFile := filepath.Join(dir, "users.json")
	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	secretFile := filepath.Join(dir, "client-secret")
	if err := os.WriteFile(usersFile, usersJSON, 0o600); err != nil {
		e.T.Fatal(err)
	}
	if err := os.WriteFile(secretFile, []byte(hex.EncodeToString(secret)), 0o600); err != nil {
		e.T.Fatal(err)
	}
	listen := fmt.Sprintf("127.0.0.1:%d", e.FreePort())
	fx := &OIDCFixture{Issuer: "http://" + listen, ClientID: "nexora", ClientSecretFile: secretFile}
	fx.Proc = e.Start("nexora-fixture", []string{"oidc", "--listen", listen, "--client-id", fx.ClientID,
		"--client-secret-file", secretFile, "--users-file", usersFile}, nil)
	fx.Proc.WaitLog(fixtureReady, 10*time.Second)
	return fx
}
