package control_test

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
)

func TestM5ContractFieldNumbers(t *testing.T) {
	srv := (&controlv1.ServerMessage{}).ProtoReflect().Descriptor()
	eng := (&controlv1.EngineMessage{}).ProtoReflect().Descriptor()
	for _, c := range []struct {
		msg   protoreflect.MessageDescriptor
		field protoreflect.Name
		num   protoreflect.FieldNumber
	}{
		{eng, "cert_request", 500},
		{srv, "cert_issued", 500},
		{srv, "renew_certificate", 501},
	} {
		f := c.msg.Fields().ByName(c.field)
		if f == nil {
			t.Fatalf("%s.%s missing", c.msg.FullName(), c.field)
		}
		if f.Number() != c.num || f.ContainingOneof() == nil || f.ContainingOneof().Name() != "msg" {
			t.Errorf("%s.%s = %d (oneof %v), want %d in oneof msg", c.msg.FullName(), c.field, f.Number(), f.ContainingOneof(), c.num)
		}
	}
	for _, m := range []protoreflect.MessageDescriptor{
		(&controlv1.ConfigSnapshot{}).ProtoReflect().Descriptor(), (&controlv1.Stats{}).ProtoReflect().Descriptor(),
	} {
		for i := 0; i < m.Fields().Len(); i++ {
			if n := m.Fields().Get(i).Number(); n >= 500 {
				t.Errorf("%s has an M5 field %d; M5 adds none there", m.FullName(), n)
			}
		}
	}

	in := &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_CertRequest{CertRequest: &controlv1.CertificateRequest{
		CsrDer: []byte{0x30, 0x01}, Reason: controlv1.CertificateRequest_REASON_ROTATE}}}
	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out controlv1.EngineMessage
	if err := proto.Unmarshal(raw, &out); err != nil || out.GetCertRequest().GetReason() != controlv1.CertificateRequest_REASON_ROTATE {
		t.Fatalf("round trip: %v %v", out.GetCertRequest(), err)
	}
	issued := &controlv1.ServerMessage{Msg: &controlv1.ServerMessage_CertIssued{CertIssued: &controlv1.CertificateIssued{CertDer: []byte{1}, CaDer: []byte{2}}}}
	renew := &controlv1.ServerMessage{Msg: &controlv1.ServerMessage_RenewCertificate{RenewCertificate: &controlv1.RenewCertificate{
		Reason: controlv1.CertificateRequest_REASON_RENEWAL}}}
	for _, m := range []proto.Message{issued, renew} {
		if _, err := proto.Marshal(m); err != nil {
			t.Fatal(err)
		}
	}
}
