package dnssecconf

import "testing"

func TestValidateDS(t *testing.T) {
	for _, ok := range IANARootAnchors {
		if err := ValidateDS(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "20326 8 2", "20326 8 2 ZZ", "70000 8 2 E06D", "20326 8 2 E06D44"} {
		if err := ValidateDS(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestValidateDomainAndForwardAddresses(t *testing.T) {
	if d, err := ValidateDomain("Corp.Example"); err != nil || d != "corp.example." {
		t.Fatalf("ValidateDomain = %q, %v", d, err)
	}
	if _, err := ValidateDomain("bad..name"); err == nil {
		t.Fatal("expected error for empty label")
	}
	if err := ValidateForwardAddresses([]string{"10.0.0.1:53", "[fd00::1]:5353"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateForwardAddresses([]string{"10.0.0.1"}); err == nil {
		t.Fatal("expected error for missing port")
	}
	if err := ValidateRootHints([]RootHint{{Name: "a.root.test.", Addresses: []string{"127.0.53.1:53"}}}); err == nil {
		t.Fatal("expected error for root hint with port")
	}
}
