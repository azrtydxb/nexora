package control_test

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
)

// M8 fields use 900-999 and survive a round trip; a renumbering or a missing field fails here.
func TestContractM8FieldsRoundTrip(t *testing.T) {
	snap := &controlv1.ConfigSnapshot{
		Mdns: &controlv1.MdnsConfig{Enabled: true, Interfaces: []string{"gw0"}, TimeoutMs: 500, Reflect: true, ReflectInterfaces: []string{"gwA", "gwB"}},
		Odoh: &controlv1.OdohConfig{TargetEnabled: true, ProxyEnabled: true, ProxyTimeoutMs: 2000,
			ProxyTargets: []*controlv1.OdohProxyTarget{{Host: "odoh.example:8443", CaPem: "pem"}}},
		RpzZones: []*controlv1.RpzZone{{Id: "z", Name: "rpz.test.", Source: &controlv1.RpzZone_Transfer{
			Transfer: &controlv1.RpzTransferSource{Primary: "127.0.0.1:53", ZonemdVerify: controlv1.ZonemdVerify_ZONEMD_VERIFY_REQUIRED}}}},
	}
	stats := &controlv1.Stats{RpzZones: []*controlv1.RpzZoneStatus{{Id: "z", Zonemd: controlv1.ZonemdStatus_ZONEMD_STATUS_FAILED, ZonemdError: "digest mismatch"}}}
	keys := &controlv1.ServerMessage{Msg: &controlv1.ServerMessage_OdohKeys{OdohKeys: &controlv1.OdohKeys{
		Keys: []*controlv1.OdohKey{{Seed: make([]byte, 32), PublishAfterUnix: 1, NotAfterUnix: 2}}}}}
	for _, m := range []proto.Message{snap, stats, keys} {
		b, err := proto.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		back := m.ProtoReflect().New().Interface()
		if err := proto.Unmarshal(b, back); err != nil || !proto.Equal(m, back) {
			t.Fatalf("round trip of %T: %v", m, err)
		}
	}
	for _, c := range []struct {
		msg   proto.Message
		field string
		num   protoreflect.FieldNumber
	}{
		{snap, "mdns", 900}, {snap, "odoh", 901},
		{&controlv1.RpzTransferSource{}, "zonemd_verify", 900},
		{&controlv1.RpzZoneStatus{}, "zonemd", 900}, {&controlv1.RpzZoneStatus{}, "zonemd_error", 901},
		{keys, "odoh_keys", 900},
	} {
		f := c.msg.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(c.field))
		if f == nil || f.Number() != c.num {
			t.Fatalf("%s.%s must be field %d", c.msg.ProtoReflect().Descriptor().Name(), c.field, c.num)
		}
	}
}

// ODoH seeds must never become part of a persisted snapshot.
func TestSnapshotCannotReachOdohKeys(t *testing.T) {
	seen := map[protoreflect.FullName]bool{}
	var walk func(md protoreflect.MessageDescriptor)
	walk = func(md protoreflect.MessageDescriptor) {
		if seen[md.FullName()] {
			return
		}
		seen[md.FullName()] = true
		if md.FullName() == "nexora.control.v1.OdohKeys" || md.FullName() == "nexora.control.v1.OdohKey" {
			t.Fatalf("ConfigSnapshot reaches %s", md.FullName())
		}
		fields := md.Fields()
		for i := 0; i < fields.Len(); i++ {
			if m := fields.Get(i).Message(); m != nil {
				walk(m)
			}
		}
	}
	walk((&controlv1.ConfigSnapshot{}).ProtoReflect().Descriptor())
	if !seen["nexora.control.v1.MdnsConfig"] || !seen["nexora.control.v1.OdohConfig"] {
		t.Fatal("the walk did not reach the M8 snapshot messages")
	}
}
