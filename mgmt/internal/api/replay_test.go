package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

// sessionRequest is an API request authenticated as a new user with role.
func sessionRequest(t *testing.T, ctx context.Context, st *store.Store, svc *auth.Service, username string, role auth.Role) *http.Request {
	t.Helper()
	var u auth.User
	if err := st.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		u, err = auth.CreateUser(ctx, tx, username, username+"@x", "long-enough-password-1", role)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	token, err := svc.CreateSession(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/ai/proposals/apply", nil)
	r.AddCookie(svc.SessionCookie(token))
	return r
}

// TestReplayUsesCallerCredentials catches a replay that runs with anything but the caller's own
// credentials (an operator's change audited under their name, a viewer refused) or that can nest.
func TestReplayUsesCallerCredentials(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	st := storetest.New(t)
	if _, err := snapshot.EnsureInitial(ctx, st, snapshot.BuildConfig{}); err != nil {
		t.Fatal(err)
	}
	svc := auth.NewService(st, false)
	h, _ := newHandlers(Deps{Store: st, Auth: svc})
	var g store.PolicyGroup
	if err := st.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		g, err = store.CreatePolicyGroup(ctx, tx, store.PolicyGroup{Name: "guests", CIDRs: []netip.Prefix{netip.MustParsePrefix("10.9.0.0/16")}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	call := func(rev int64) replayCall {
		body, _ := json.Marshal(map[string]any{"name": "guests", "description": "via replay", "cidrs": []string{"10.9.0.0/16"}, "revision": rev})
		return replayCall{OperationID: "updatePolicyGroup", PathParams: map[string]string{"id": g.ID.String()}, Body: body}
	}

	operator := sessionRequest(t, ctx, st, svc, "olga", auth.RoleOperator)
	res, err := h.replay(context.WithValue(ctx, requestKey{}, operator), call(g.Revision))
	if err != nil || res.Status != http.StatusOK {
		t.Fatalf("operator replay: %+v %v", res, err)
	}
	var actor string
	if err := st.Pool.QueryRow(ctx, "select actor_name from audit_log where action = 'updatePolicyGroup'").Scan(&actor); err != nil || actor != "olga" {
		t.Fatalf("audit actor %q %v", actor, err)
	}
	if got, _ := store.GetPolicyGroup(ctx, st.Pool, g.ID); got.Description != "via replay" {
		t.Fatalf("group not changed: %+v", got)
	}

	viewer := sessionRequest(t, ctx, st, svc, "vic", auth.RoleViewer)
	res, err = h.replay(context.WithValue(ctx, requestKey{}, viewer), call(g.Revision+1))
	if err != nil || res.Status != http.StatusForbidden || res.Code != "forbidden" {
		t.Fatalf("viewer replay: %+v %v", res, err)
	}

	nested := operator.Clone(ctx)
	nested.Header.Set(replayHeader, "1")
	_, err = h.replay(context.WithValue(ctx, requestKey{}, nested), call(g.Revision+1))
	var aerr apiError
	if !errors.As(err, &aerr) || aerr.status != http.StatusBadRequest || aerr.code != "invalid_request" {
		t.Fatalf("nested replay: %v", err)
	}
}
