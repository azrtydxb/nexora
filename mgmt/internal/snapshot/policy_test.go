package snapshot_test

import (
	"net/netip"
	"testing"

	"github.com/google/uuid"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

func TestBuildPolicySection(t *testing.T) {
	groupOnly := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	unfetched := uuid.MustParse("44444444-4444-4444-4444-444444444444")
	lists := []snapshot.PolicyList{{ID: groupOnly, Ref: &controlv1.BlobRef{Sha256: "ab12", Size: 42, Name: "ads"}, Position: snapshot.CustomListPosition}}
	kidsID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	kids := store.PolicyGroup{
		ID: kidsID, Name: "kids",
		CIDRs:         []netip.Prefix{netip.MustParsePrefix("192.168.50.0/24")},
		FilterListIDs: []uuid.UUID{groupOnly, unfetched},
		Allowlist:     []string{"school.example"},
		SafeSearch:    store.SafeSearch{Google: true, YouTube: "strict"},
	}
	rewrites := []store.Rewrite{
		{Name: "nas.home.test", Type: "A", Value: "192.168.1.50", TTL: 120},
		{GroupID: &kidsID, Name: "www.google.com", Type: "A", Value: "192.0.2.99", TTL: 60},
	}
	sec := snapshot.BuildPolicySection([]store.PolicyGroup{kids}, lists, rewrites, store.SafeSearch{Bing: true, YouTube: "moderate"})

	if bl := sec.Groups[0].Blocklists; len(bl) != 1 || bl[0].Sha256 != "ab12" || bl[0].Size != 42 {
		t.Fatalf("group blocklists = %v (a list without a fetched blob is skipped)", bl)
	}
	wantGlobal := []string{"custom:global", snapshot.SafeSearchBing, snapshot.SafeSearchYouTubeModerate}
	if !equal(sec.GlobalRewriteSetIDs, wantGlobal) {
		t.Fatalf("global sets = %v, want %v", sec.GlobalRewriteSetIDs, wantGlobal)
	}
	g := sec.Groups[0]
	wantGroup := []string{"custom:group:" + kidsID.String(), snapshot.SafeSearchGoogle, snapshot.SafeSearchYouTubeStrict}
	if g.Id != kidsID.String() || !equal(g.Cidrs, []string{"192.168.50.0/24"}) || !equal(g.RewriteSetIds, wantGroup) || !equal(g.Allowlist, []string{"school.example"}) {
		t.Fatalf("group = %v", g)
	}
	ids := map[string]*controlv1.RewriteSet{}
	for _, s := range sec.RewriteSets {
		if ids[s.Id] != nil {
			t.Fatalf("duplicate set %s", s.Id)
		}
		ids[s.Id] = s
	}
	if len(ids) != 6 {
		t.Fatalf("sets = %d, want 6 (2 custom + google, bing, youtube strict, youtube moderate)", len(ids))
	}
	google := ids[snapshot.SafeSearchGoogle]
	found := false
	for _, r := range google.Rules {
		if r.Name == "www.google.co.uk" && r.Type == controlv1.RewriteType_REWRITE_TYPE_CNAME && r.Value == "forcesafesearch.google.com" {
			found = true
		}
	}
	if !found {
		t.Fatalf("google set lacks www.google.co.uk -> forcesafesearch.google.com")
	}
	if ids[snapshot.SafeSearchYouTubeStrict].Rules[0].Value != "restrict.youtube.com" {
		t.Fatalf("youtube strict target = %s", ids[snapshot.SafeSearchYouTubeStrict].Rules[0].Value)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
