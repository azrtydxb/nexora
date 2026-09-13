package control_test

import (
	"testing"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestM3ContractFieldNumbers(t *testing.T) {
	snap := (&controlv1.ConfigSnapshot{}).ProtoReflect().Descriptor()
	stats := (&controlv1.Stats{}).ProtoReflect().Descriptor()
	srv := (&controlv1.ServerMessage{}).ProtoReflect().Descriptor()
	cases := []struct {
		msg   protoreflect.MessageDescriptor
		field protoreflect.Name
		num   protoreflect.FieldNumber
	}{
		{snap, "resolution_mode", 100},
		{snap, "recursion", 101},
		{snap, "forward_zones", 102},
		{snap, "dnssec", 103},
		{snap, "rpz_zones", 104},
		{snap, "dnssec_validate_forwarded", 105},
		{stats, "recursion", 100},
		{stats, "dnssec", 101},
		{stats, "rpz_zones", 102},
		{srv, "rpz_tsig_keys", 100},
	}
	for _, c := range cases {
		f := c.msg.Fields().ByName(c.field)
		if f == nil {
			t.Fatalf("%s.%s missing", c.msg.FullName(), c.field)
		}
		if f.Number() != c.num {
			t.Errorf("%s.%s = %d, want %d", c.msg.FullName(), c.field, f.Number(), c.num)
		}
	}
}

// The snapshot is stored in config_versions and persisted on engine disk: it must never be able to
// carry RPZ TSIG secrets.
func TestSnapshotCannotReachRpzTsigKeys(t *testing.T) {
	seen := map[protoreflect.FullName]bool{}
	var walk func(m protoreflect.MessageDescriptor)
	walk = func(m protoreflect.MessageDescriptor) {
		if seen[m.FullName()] {
			return
		}
		seen[m.FullName()] = true
		if m.FullName() == "nexora.control.v1.RpzTsigKeys" || m.FullName() == "nexora.control.v1.RpzTsigKey" {
			t.Fatalf("ConfigSnapshot reaches %s", m.FullName())
		}
		fields := m.Fields()
		for i := 0; i < fields.Len(); i++ {
			if name := fields.Get(i).Name(); name == "secret" || name == "tsig_secret" {
				t.Fatalf("%s has a %s field", m.FullName(), name)
			}
			if sub := fields.Get(i).Message(); sub != nil {
				walk(sub)
			}
		}
	}
	walk((&controlv1.ConfigSnapshot{}).ProtoReflect().Descriptor())
	if !seen["nexora.control.v1.RpzTransferSource"] {
		t.Fatalf("walk did not reach RpzTransferSource; the positive path is broken")
	}
}
