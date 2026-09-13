package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

func db(t *testing.T) (*harness.Env, *store.Store, context.Context) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	st, err := store.Open(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return env, st, ctx
}

func TestSetupTokenIsSingleAndConsumedOnce(t *testing.T) {
	_, st, ctx := db(t)
	svc := auth.NewService(st, true)
	var mu sync.Mutex
	var tokens []string
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tok, created, err := svc.EnsureSetupToken(ctx, "instance-"+string(rune('a'+i)))
			if err != nil {
				t.Error(err)
				return
			}
			if created {
				mu.Lock()
				tokens = append(tokens, tok)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if len(tokens) != 1 {
		t.Fatalf("created %d setup tokens, want 1", len(tokens))
	}
	if _, err := svc.CompleteSetup(ctx, "wrong", "admin", "a@x", "a-long-password-123"); !errors.Is(err, auth.ErrSetupDone) {
		t.Fatalf("wrong token -> %v", err)
	}
	if _, err := svc.CompleteSetup(ctx, tokens[0], "admin", "a@x", "short"); !errors.Is(err, auth.ErrWeakPassword) {
		t.Fatalf("weak password -> %v", err)
	}
	u, err := svc.CompleteSetup(ctx, tokens[0], "admin", "a@x", "a-long-password-123")
	if err != nil || u.Role != auth.RoleAdmin {
		t.Fatalf("setup: %v %v", u, err)
	}
	if _, err := svc.CompleteSetup(ctx, tokens[0], "admin2", "b@x", "a-long-password-123"); !errors.Is(err, auth.ErrSetupDone) {
		t.Fatalf("second setup -> %v", err)
	}
	if req, _ := svc.SetupRequired(ctx); req {
		t.Fatal("setup still required")
	}
}

func TestSessionsTokensAndDisabledUsers(t *testing.T) {
	_, st, ctx := db(t)
	svc := auth.NewService(st, true)
	var op auth.User
	if err := st.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		op, err = auth.CreateUser(ctx, tx, "olga", "o@x", "operator-password-1", auth.RoleOperator)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login(ctx, "olga", "nope-nope-nope"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("bad login -> %v", err)
	}
	sess, _, err := svc.Login(ctx, "olga", "operator-password-1")
	if err != nil {
		t.Fatal(err)
	}
	c := svc.SessionCookie(sess)
	if c.Name != "nexora_session" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie flags %+v", c)
	}
	r := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	r.AddCookie(c)
	p, err := svc.Authenticate(ctx, r)
	if err != nil || p.Username != "olga" || p.Role != auth.RoleOperator || p.Kind != "session" {
		t.Fatalf("session auth: %+v %v", p, err)
	}

	var tok string
	err = st.InTx(ctx, func(tx pgx.Tx) error {
		if _, _, err := svc.CreateAPIToken(ctx, tx, p, "too-strong", auth.RoleAdmin, nil); !errors.Is(err, auth.ErrForbidden) {
			return errors.New("operator minted an admin token")
		}
		var e error
		_, tok, e = svc.CreateAPIToken(ctx, tx, p, "ci", auth.RoleViewer, nil)
		return e
	})
	if err != nil || !strings.HasPrefix(tok, "nxt_") {
		t.Fatalf("token: %q %v", tok, err)
	}
	r = httptest.NewRequest("GET", "/api/v1/upstreams", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	if p, err := svc.Authenticate(ctx, r); err != nil || p.Role != auth.RoleViewer || p.Kind != "api_token" {
		t.Fatalf("token auth: %+v %v", p, err)
	}

	_, _ = st.Pool.Exec(ctx, "update users set disabled=true where id=$1", op.ID)
	r = httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	r.AddCookie(c)
	if _, err := svc.Authenticate(ctx, r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("disabled user -> %v", err)
	}
	r.Header.Set("Authorization", "Bearer "+tok)
	if _, err := svc.Authenticate(ctx, r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("disabled user token -> %v", err)
	}
}

func TestOIDCLoginAndProviderDown(t *testing.T) {
	env, st, ctx := db(t)
	fx := env.StartOIDCFixture(harness.OIDCUser{Username: "ada", Email: "ada@x", Groups: []string{"nexora-admins"}})
	cfg := config.OIDCConfig{Issuer: fx.Issuer, ClientID: fx.ClientID, ClientSecretFile: fx.ClientSecretFile, AdminGroup: "nexora-admins", OperatorGroup: "nexora-operators"}
	o := auth.NewOIDC(cfg, "http://nexora.test", st)
	redirect, err := o.Start(ctx, "/upstreams")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(redirect)
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("state") == "" {
		t.Fatalf("authorize URL lacks PKCE/state: %s", redirect)
	}
	form := url.Values{"user": {"ada"}, "client_id": {q.Get("client_id")}, "redirect_uri": {q.Get("redirect_uri")}, "state": {q.Get("state")}, "nonce": {q.Get("nonce")}, "code_challenge": {q.Get("code_challenge")}}
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.PostForm(fx.Issuer+"/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	cb, _ := url.Parse(resp.Header.Get("Location"))
	user, returnTo, err := o.Callback(ctx, cb.Query().Get("state"), cb.Query().Get("code"))
	if err != nil || user.Role != auth.RoleAdmin || user.Source != "oidc" || returnTo != "/upstreams" {
		t.Fatalf("callback: %+v %q %v", user, returnTo, err)
	}

	svc := auth.NewService(st, false)
	_ = st.InTx(ctx, func(tx pgx.Tx) error {
		_, err := auth.CreateUser(ctx, tx, "local", "l@x", "local-password-12", auth.RoleViewer)
		return err
	})
	fx.Proc.Kill()
	// The instance that already discovered the provider must notice the outage too, instead of
	// redirecting browsers to an unreachable identity provider.
	if _, err := o.Start(ctx, "/"); !errors.Is(err, auth.ErrOIDCUnavailable) {
		t.Fatalf("provider down after discovery -> %v", err)
	}
	o2 := auth.NewOIDC(cfg, "http://nexora.test", st)
	if _, err := o2.Start(ctx, "/"); !errors.Is(err, auth.ErrOIDCUnavailable) {
		t.Fatalf("provider down -> %v", err)
	}
	if _, _, err := svc.Login(ctx, "local", "local-password-12"); err != nil {
		t.Fatalf("local login with OIDC down: %v", err)
	}
}
