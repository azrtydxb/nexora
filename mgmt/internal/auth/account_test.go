package auth_test

import (
	"errors"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
)

// TestLoginThrottleIsPerUsernameAndClient catches a throttle keyed on the username alone, which
// would let a remote attacker lock the admin out from everywhere.
func TestLoginThrottleIsPerUsernameAndClient(t *testing.T) {
	_, st, ctx := db(t)
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
