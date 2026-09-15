package proposal_test

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/ai/proposal"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func createGroup(t *testing.T, ctx context.Context, st *store.Store, name, cidr string) store.PolicyGroup {
	t.Helper()
	var g store.PolicyGroup
	err := st.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		g, err = store.CreatePolicyGroup(ctx, tx, store.PolicyGroup{Name: name, CIDRs: []netip.Prefix{netip.MustParsePrefix(cidr)}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func groupBody(rev int64, extra map[string]any) json.RawMessage {
	b := map[string]any{"name": "guests", "cidrs": []string{"10.9.0.0/16"}, "revision": rev}
	for k, v := range extra {
		b[k] = v
	}
	raw, _ := json.Marshal(b)
	return raw
}

func rpzAction(record string) proposal.Action {
	body, _ := json.Marshal(map[string]any{"rules": []map[string]any{{"record": record, "policy": "nxdomain", "category": "c2", "reason": "beaconing", "confidence": 0.9}}})
	return proposal.Action{OperationID: proposal.OpAppendAiRpzRules, PathParams: map[string]string{}, Body: body}
}

// TestProposalActionValidation catches a validator that lets through an operation outside the allowlist,
// unknown or mistyped body fields, a missing path resource or a stale revision.
func TestProposalActionValidation(t *testing.T) {
	ctx := testCtx(t)
	st := storetest.New(t)
	g := createGroup(t, ctx, st, "guests", "10.9.0.0/16")
	v := &proposal.Validator{Store: st, PublicURL: "https://nexora.kw.watteel.lab"}
	id := map[string]string{"id": g.ID.String()}

	valid := proposal.Action{OperationID: "updatePolicyGroup", PathParams: id, Body: groupBody(g.Revision, nil)}
	if err := v.Validate(ctx, []proposal.Action{valid}); err != nil {
		t.Fatalf("valid updatePolicyGroup: %v", err)
	}
	cidrString, _ := json.Marshal(map[string]any{"name": "guests", "cidrs": "10.0.0.0/8", "revision": g.Revision})
	for _, c := range []struct {
		name   string
		action proposal.Action
		want   string
	}{
		{"not allowlisted", proposal.Action{OperationID: "deleteZone", PathParams: map[string]string{"id": uuid.NewString()}}, "operation deleteZone is not allowed"},
		{"unknown field", proposal.Action{OperationID: "updatePolicyGroup", PathParams: id, Body: groupBody(g.Revision, map[string]any{"colour": "red"})}, "colour"},
		{"mistyped field", proposal.Action{OperationID: "updatePolicyGroup", PathParams: id, Body: cidrString}, "cidrs"},
		{"missing resource", proposal.Action{OperationID: "updatePolicyGroup", PathParams: map[string]string{"id": uuid.NewString()}, Body: groupBody(1, nil)}, "not found"},
		{"stale revision", proposal.Action{OperationID: "updatePolicyGroup", PathParams: id, Body: groupBody(g.Revision-1, nil)}, "stale revision"},
	} {
		err := v.Validate(ctx, []proposal.Action{c.action})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err %v, want it to contain %q", c.name, err, c.want)
		}
	}
	tooMany := make([]proposal.Action, 9)
	for i := range tooMany {
		tooMany[i] = valid
	}
	if err := v.Validate(ctx, tooMany); err == nil || !strings.Contains(err.Error(), "at most 8") {
		t.Errorf("9 actions: err %v", err)
	}
}

// TestRpzSuggestionValidation catches RPZ rules that would block whole TLDs, hosted zones, the
// upstreams, the management host or allowlisted names.
func TestRpzSuggestionValidation(t *testing.T) {
	ctx := testCtx(t)
	st := storetest.New(t)
	if _, err := st.Pool.Exec(ctx, `insert into zones(name, kind, soa_mname, soa_rname) values ('corp.example.', 'primary', 'ns1.corp.example.', 'hostmaster.corp.example.')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `insert into upstreams(name, protocol, doh_url, position) values ('quad9', 'doh', 'https://dns.quad9.net/dns-query', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `update allowlist set domains = '{ok.example}'`); err != nil {
		t.Fatal(err)
	}
	v := &proposal.Validator{Store: st, PublicURL: "https://nexora.kw.watteel.lab"}
	if err := v.Validate(ctx, []proposal.Action{rpzAction("c2.evil.example")}); err != nil {
		t.Fatalf("c2.evil.example: %v", err)
	}
	for _, c := range []struct{ record, want string }{
		{"*.com", "wildcard needs two labels"},
		{"www.corp.example", "hosted zone corp.example."},
		{"nexora.kw.watteel.lab", "management host"},
		{"bad..name", "invalid name"},
		{"dns.quad9.net", "upstream host"},
		{"ok.example", "allowlisted"},
	} {
		err := v.Validate(ctx, []proposal.Action{rpzAction(c.record)})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err %v, want it to contain %q", c.record, err, c.want)
		}
	}
}
