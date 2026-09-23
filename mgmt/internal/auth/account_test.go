package auth_test

import (
	"errors"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
)

// TestLoginThrottleIsPerUsernameAndClient catches a throttle keyed on the username alone, which
// would let a remote attacker lock the admin out from everywhere.
func TestLoginThrottleIsPerUsernameAndClient(t *testing.T) {
	// 15 logins of deliberately expensive argon2 work; the default budget is too small for it
	// on the CI runner under -race (observed 106s there, a few seconds on a laptop).
	_, st, ctx := dbWithin(t, 6*time.Minute)
	svc := auth.NewService(st, false)
	if err := st.InTx(ctx, func(tx pgx.Tx) error {
		for _, name := range []string{"admin", "bob"} {
			if _, err := auth.CreateUser(ctx, tx, name, "", name+"-password-1", auth.RoleAdmin); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	const attacker, operator = "203.0.113.7", "192.0.2.10"
	if _, _, err := svc.Login(ctx, "admin", "admin-password-1", attacker); err != nil {
		t.Fatalf("login before any failure: %v", err)
	}
	for i := 0; i < 11; i++ {
		if _, _, err := svc.Login(ctx, "ADMIN", "wrong-password-"+strconv.Itoa(i), attacker); !errors.Is(err, auth.ErrInvalidCredentials) && !errors.Is(err, auth.ErrTooManyAttempts) {
			t.Fatalf("failure %d -> %v", i, err)
		}
	}
	if _, _, err := svc.Login(ctx, "admin", "admin-password-1", attacker); !errors.Is(err, auth.ErrTooManyAttempts) {
		t.Fatalf("attacker after 11 failures -> %v, want ErrTooManyAttempts", err)
	}
	if _, _, err := svc.Login(ctx, "admin", "admin-password-1", operator); err != nil {
		t.Fatalf("admin from another address locked out: %v", err)
	}
	if _, _, err := svc.Login(ctx, "bob", "bob-password-1", attacker); err != nil {
		t.Fatalf("another user from the attacker's address locked out: %v", err)
	}
}

// Actual service failures use the same address boundary as the HTTP handlers. The
// persistence assertion catches lost concurrent increments, independently of 429.
func TestProxiedAccountConcurrentFailures(t *testing.T) {
	_, st, ctx := db(t)
	svc := auth.NewService(st, false)
	var user auth.User
	if err := st.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		user, err = auth.CreateUser(ctx, tx, "proxy-admin", "", "proxy-password-1", auth.RoleAdmin)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	proxies := []netip.Prefix{netip.MustParsePrefix("192.0.2.10/32")}
	address := func(xff string) string {
		r := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
		r.RemoteAddr = "192.0.2.10:443"
		r.Header.Set("X-Forwarded-For", xff)
		return auth.ClientAddr(r, proxies)
	}
	attacker := address("203.0.113.99, 198.51.100.1")
	operator := address("198.51.100.2")
	if attacker == operator {
		t.Fatal("distinct proxied clients share a throttle key")
	}
	failures := make(chan error, 11)
	start := make(chan struct{})
	for i := 0; i < 11; i++ {
		go func() {
			<-start
			_, _, err := svc.Login(ctx, "PROXY-ADMIN", "wrong-password", attacker)
			failures <- err
		}()
	}
	close(start)
	for i := 0; i < 11; i++ {
		if err := <-failures; !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Errorf("concurrent failure: %v", err)
		}
	}
	var count int
	if err := st.Pool.QueryRow(ctx, "select count(*) from auth_failures where username = 'proxy-admin' and client = $1", attacker).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 11 {
		t.Fatalf("persisted %d failures, want 11", count)
	}
	// Changing a forged prefix cannot escape the nearest untrusted hop's failures.
	if _, _, err := svc.Login(ctx, "proxy-admin", "proxy-password-1", address("203.0.113.88, 198.51.100.1")); !errors.Is(err, auth.ErrTooManyAttempts) {
		t.Fatalf("forged prefix escaped throttle: %v", err)
	}
	if _, _, err := svc.Login(ctx, "proxy-admin", "proxy-password-1", operator); err != nil {
		t.Fatalf("other proxied client locked out: %v", err)
	}
	p := auth.Principal{Kind: "session", UserID: user.ID}
	if err := svc.ChangePassword(ctx, p, attacker, "", "proxy-password-1", "new-proxy-password-1", false); !errors.Is(err, auth.ErrTooManyAttempts) {
		t.Fatalf("password change did not share login throttle: %v", err)
	}
	if err := svc.ChangePassword(ctx, p, operator, "", "proxy-password-1", "new-proxy-password-1", false); err != nil {
		t.Fatalf("other proxied client's password change locked out: %v", err)
	}
}
