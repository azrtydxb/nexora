package auth_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
)

func TestArgon2idHashAndVerify(t *testing.T) {
	h, err := auth.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("hash format %q", h)
	}
	if ok, _ := auth.VerifyPassword(h, "correct horse battery"); !ok {
		t.Fatal("verify failed")
	}
	if ok, _ := auth.VerifyPassword(h, "wrong horse battery"); ok {
		t.Fatal("wrong password verified")
	}
	h2, _ := auth.HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("salt not random")
	}
}

func TestRBACMatrix(t *testing.T) {
	viewer := auth.Principal{Username: "v", Role: auth.RoleViewer}
	operator := auth.Principal{Username: "o", Role: auth.RoleOperator}
	admin := auth.Principal{Username: "a", Role: auth.RoleAdmin}
	cases := []struct {
		p     auth.Principal
		op    string
		allow bool
	}{
		{viewer, "listUpstreams", true}, {viewer, "createUpstream", false}, {viewer, "listUsers", false},
		{viewer, "listApiTokens", false}, {viewer, "listAuditEvents", false}, {viewer, "searchQueryLog", true},
		{operator, "createUpstream", true}, {operator, "refreshFilterList", true}, {operator, "createUser", false},
		{operator, "listAuditEvents", false}, {operator, "createJoinToken", false},
		{admin, "createUser", true}, {admin, "listAuditEvents", true}, {admin, "deleteEngine", true},
		{admin, "noSuchOperation", false},
	}
	for _, c := range cases {
		err := auth.Authorize(c.p, c.op)
		if (err == nil) != c.allow {
			t.Errorf("%s %s: err=%v want allow=%v", c.p.Role, c.op, err, c.allow)
		}
		if err != nil && !errors.Is(err, auth.ErrForbidden) {
			t.Errorf("error must be ErrForbidden, got %v", err)
		}
	}
	for op := range auth.Public {
		if _, dup := auth.Permissions[op]; dup {
			t.Errorf("%s is both public and permissioned", op)
		}
	}
}
