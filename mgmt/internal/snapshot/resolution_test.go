package snapshot_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

func TestApplyResolution(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	sha := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	primary := "127.0.0.1:5300"
	alg := "hmac-sha256"
	keyName := "rpz-key."
	envelope := []byte("NXE1-sealed-secret-bytes")
	rows := store.ResolutionRows{
		Resolution:   store.ResolutionSettings{Mode: "recursive", QnameMinimisation: true, MaxUpstreamQueries: 100, MaxDelegationDepth: 32, AuthorityPort: 5353, RootHints: []store.RootHint{{Name: "a.root.test.", Addresses: []string{"127.0.53.1"}}}},
		ForwardZones: []store.ForwardZone{{Domain: "corp.example.", Addresses: []string{"10.0.0.1:53"}, Validate: true}},
		Dnssec:       store.DnssecSettings{Validation: true, ValidateForwarded: true, RFC5011: true},
		Anchors:      []store.TrustAnchor{{Zone: ".", DS: "20326 8 2 E06D44B80B8F1D39A95C0B0D7C65D08458E880409BBC683457104237C7F8EC8D"}},
		NTAs: []store.NegativeTrustAnchor{
			{Domain: "broken.example.", ExpiresAt: now.Add(time.Hour)},
			{Domain: "expired.example.", ExpiresAt: now.Add(-time.Second)},
		},
		RPZ: []store.RPZZone{
			{ID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), Name: "rpz.axfr.test.", Position: 2, SourceType: "transfer", PrimaryAddress: &primary, TSIGAlgorithm: &alg, TSIGKeyName: &keyName, TSIGSecretEnvelope: envelope, MinRefreshSeconds: 5, PolicyOverride: "given"},
			{ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), Name: "rpz.file.test.", Position: 1, SourceType: "file", BlobSHA256: &sha, BlobSize: 42, MinRefreshSeconds: 60, PolicyOverride: "nxdomain", RefreshNonce: 3},
			{ID: uuid.MustParse("33333333-3333-3333-3333-333333333333"), Name: "rpz.empty.test.", Position: 3, SourceType: "file", MinRefreshSeconds: 60, PolicyOverride: "given"},
		},
	}
	s := &controlv1.ConfigSnapshot{}
	snapshot.ApplyResolution(s, rows, now)
	if s.ResolutionMode != controlv1.ResolutionMode_RESOLUTION_MODE_RECURSIVE || s.Recursion.AuthorityPort != 5353 || len(s.Recursion.RootHints) != 1 {
		t.Fatalf("recursion = %v %v", s.ResolutionMode, s.Recursion)
	}
	if len(s.ForwardZones) != 1 || !s.ForwardZones[0].Validate {
		t.Fatalf("forward zones = %v", s.ForwardZones)
	}
	if len(s.Dnssec.NegativeTrustAnchors) != 1 || s.Dnssec.NegativeTrustAnchors[0].ExpiresUnix != now.Add(time.Hour).Unix() {
		t.Fatalf("expired NTAs must be omitted: %v", s.Dnssec.NegativeTrustAnchors)
	}
	if !s.DnssecValidateForwarded {
		t.Fatal("dnssec_validate_forwarded not carried into the snapshot")
	}
	off := &controlv1.ConfigSnapshot{}
	rows.Dnssec.Validation = false
	snapshot.ApplyResolution(off, rows, now)
	if off.DnssecValidateForwarded {
		t.Fatal("dnssec_validate_forwarded sent without validation; the engine rejects that snapshot")
	}
	if len(s.RpzZones) != 2 {
		t.Fatalf("a file zone without an uploaded file must be omitted: %v", s.RpzZones)
	}
	blob := s.RpzZones[0].GetFile().GetBlob()
	if s.RpzZones[0].Name != "rpz.file.test." || blob.GetSha256() != sha || blob.GetSize() != 42 || s.RpzZones[0].PolicyOverride != controlv1.RpzPolicyOverride_RPZ_POLICY_OVERRIDE_NXDOMAIN || s.RpzZones[0].RefreshNonce != 3 {
		t.Fatalf("rpz[0] = %v", s.RpzZones[0])
	}
	tr := s.RpzZones[1].GetTransfer()
	if tr.GetPrimary() != primary || tr.GetTsigAlgorithm() != controlv1.TsigAlgorithm_TSIG_ALGORITHM_HMAC_SHA256 || tr.GetTsigKeyName() != keyName {
		t.Fatalf("rpz[1] = %v", s.RpzZones[1])
	}
	raw, err := proto.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, envelope) {
		t.Fatal("the sealed TSIG secret reached the snapshot")
	}
}
