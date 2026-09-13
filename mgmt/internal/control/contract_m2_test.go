package control_test

import (
	"testing"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestM2ContractFieldNumbers(t *testing.T) {
	snap := (&controlv1.ConfigSnapshot{}).ProtoReflect().Descriptor()
	hello := (&controlv1.Hello{}).ProtoReflect().Descriptor()
	srv := (&controlv1.ServerMessage{}).ProtoReflect().Descriptor()
	eng := (&controlv1.EngineMessage{}).ProtoReflect().Descriptor()
	cases := []struct {
		msg   protoreflect.MessageDescriptor
		field protoreflect.Name
		num   protoreflect.FieldNumber
	}{
		{snap, "policy_groups", 300},
		{snap, "rewrite_sets", 301},
		{snap, "global_rewrite_set_ids", 302},
		{hello, "tls_fingerprint_sha256", 300},
		{srv, "tls_material", 300},
		{eng, "tls_material_result", 300},
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

// The snapshot is persisted on engine disk; it must never be able to carry key material.
func TestSnapshotCannotReachTlsMaterial(t *testing.T) {
	seen := map[protoreflect.FullName]bool{}
	var walk func(m protoreflect.MessageDescriptor)
	walk = func(m protoreflect.MessageDescriptor) {
		if seen[m.FullName()] {
			return
		}
		seen[m.FullName()] = true
		if m.FullName() == "nexora.control.v1.TlsMaterial" {
			t.Fatalf("ConfigSnapshot reaches TlsMaterial")
		}
		fields := m.Fields()
		for i := 0; i < fields.Len(); i++ {
			if sub := fields.Get(i).Message(); sub != nil {
				walk(sub)
			}
		}
	}
	walk((&controlv1.ConfigSnapshot{}).ProtoReflect().Descriptor())
	if seen["nexora.control.v1.PolicyGroup"] != true {
		t.Fatalf("walk did not reach PolicyGroup; the positive path is broken")
	}
}
