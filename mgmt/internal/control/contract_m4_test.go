package control_test

import (
	"testing"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestM4ContractFieldNumbers(t *testing.T) {
	snap := (&controlv1.ConfigSnapshot{}).ProtoReflect().Descriptor()
	srv := (&controlv1.ServerMessage{}).ProtoReflect().Descriptor()
	eng := (&controlv1.EngineMessage{}).ProtoReflect().Descriptor()
	cases := []struct {
		msg   protoreflect.MessageDescriptor
		field protoreflect.Name
		num   protoreflect.FieldNumber
	}{
		{snap, "auth_zones", 200},
		{srv, "key_material", 200},
		{srv, "update_result", 201},
		{eng, "notify_received", 200},
		{eng, "update_request", 201},
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
	sha384 := controlv1.TsigAlgorithm_TSIG_ALGORITHM_HMAC_SHA384.Descriptor().Values().ByName("TSIG_ALGORITHM_HMAC_SHA384")
	if sha384 == nil || sha384.Number() != 3 {
		t.Fatalf("TsigAlgorithm HMAC_SHA384 = %v, want 3", sha384)
	}
}
