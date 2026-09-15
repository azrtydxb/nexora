package auth_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

const tokA = "nxt_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
const tokB = "nxt_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"

func bearer(tok string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	return r
}

func TestBootstrapTokenEnsuresSystemUser(t *testing.T) {
	st := storetest.New(t)
	svc := auth.NewService(st, false)
	ctx := context.Background()
	if _, err := svc.EnsureBootstrapToken(ctx, "not-a-token"); !errors.Is(err, auth.ErrBootstrapTokenInvalid) {
		t.Fatalf("invalid token: %v", err)
	}
	var users int
	_ = st.Pool.QueryRow(ctx, "select count(*) from users").Scan(&users)
	if users != 0 {
		t.Fatalf("an invalid token created %d users", users)
	}
	changed, err := svc.EnsureBootstrapToken(ctx, tokA)
	if err != nil || !changed {
		t.Fatalf("first ensure: changed=%v err=%v", changed, err)
	}
	var source, role string
	var hash *string
	if err := st.Pool.QueryRow(ctx, "select source, role, password_hash from users where username = $1", auth.BootstrapUsername).Scan(&source, &role, &hash); err != nil {
		t.Fatal(err)
	}
	if source != "system" || role != "admin" || hash != nil {
		t.Fatalf("system user: source=%s role=%s hash=%v", source, role, hash)
	}
	p, err := svc.Authenticate(ctx, bearer(tokA))
	if err != nil || p.Role != auth.RoleAdmin || p.Username != auth.BootstrapUsername {
		t.Fatalf("token principal %+v err=%v", p, err)
	}
	var audits int
	_ = st.Pool.QueryRow(ctx, "select count(*) from audit_log where action = 'ensureBootstrapToken'").Scan(&audits)
	changed, err = svc.EnsureBootstrapToken(ctx, tokA)
	if err != nil || changed {
		t.Fatalf("second ensure: changed=%v err=%v", changed, err)
	}
	var audits2, tokens int
	_ = st.Pool.QueryRow(ctx, "select count(*) from audit_log where action = 'ensureBootstrapToken'").Scan(&audits2)
	_ = st.Pool.QueryRow(ctx, "select count(*) from api_tokens").Scan(&tokens)
	if audits != 1 || audits2 != 1 || tokens != 1 {
		t.Fatalf("idempotence: audits %d->%d tokens %d", audits, audits2, tokens)
	}
	var diff string
	_ = st.Pool.QueryRow(ctx, "select diff::text from audit_log where action = 'ensureBootstrapToken'").Scan(&diff)
	if strings.Contains(diff, tokA) || strings.Contains(diff, tokA[4:]) {
		t.Fatal("audit diff contains the token")
	}
}

func TestBootstrapTokenRotation(t *testing.T) {
	st := storetest.New(t)
	svc := auth.NewService(st, false)
	ctx := context.Background()
	if _, err := svc.EnsureBootstrapToken(ctx, tokA); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnsureBootstrapToken(ctx, tokB); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, bearer(tokB)); err != nil {
		t.Fatalf("new token: %v", err)
	}
	if _, err := svc.Authenticate(ctx, bearer(tokA)); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("old token after rotation: %v", err)
	}
}

func TestBootstrapTokenRefusesHumanUser(t *testing.T) {
	st := storetest.New(t)
	svc := auth.NewService(st, false)
	ctx := context.Background()
	if _, err := st.Pool.Exec(ctx, "insert into users(username, role, source, password_hash) values ($1, 'viewer', 'local', 'x')", auth.BootstrapUsername); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnsureBootstrapToken(ctx, tokA); !errors.Is(err, auth.ErrBootstrapUserConflict) {
		t.Fatalf("human user: %v", err)
	}
	var tokens int
	_ = st.Pool.QueryRow(ctx, "select count(*) from api_tokens").Scan(&tokens)
	if tokens != 0 {
		t.Fatal("a token was created for a human user")
	}
}

func TestSetupRequiredIgnoresSystemUsers(t *testing.T) {
	st := storetest.New(t)
	svc := auth.NewService(st, false)
	ctx := context.Background()
	if _, err := svc.EnsureBootstrapToken(ctx, tokA); err != nil {
		t.Fatal(err)
	}
	if req, err := svc.SetupRequired(ctx); err != nil || !req {
		t.Fatalf("setup required with only a system user: %v %v", req, err)
	}
	tok, created, err := svc.EnsureSetupToken(ctx, "test")
	if err != nil || !created {
		t.Fatalf("setup token: created=%v err=%v", created, err)
	}
	if _, err := svc.CompleteSetup(ctx, tok, "admin", "a@x", "admin-password-1"); err != nil {
		t.Fatalf("complete setup: %v", err)
	}
	if _, _, err := svc.Login(ctx, auth.BootstrapUsername, "", "127.0.0.1"); err == nil {
		t.Fatal("system user logged in")
	}
}
