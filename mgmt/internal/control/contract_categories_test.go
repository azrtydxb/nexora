package control_test

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
)

func TestFilterCategoryContractFieldNumbers(t *testing.T) {
	desc := func(m proto.Message) protoreflect.MessageDescriptor { return m.ProtoReflect().Descriptor() }
	for _, c := range []struct {
		msg   protoreflect.MessageDescriptor
		field protoreflect.Name
		num   protoreflect.FieldNumber
	}{
		{desc(&controlv1.FilterConfig{}), "blocklist_refs", 600},
		{desc(&controlv1.FilterConfig{}), "allowlist_refs", 601},
		{desc(&controlv1.PolicyGroup{}), "blocklist_refs", 600},
		{desc(&controlv1.ConfigSnapshot{}), "filter_index_max_bytes", 600},
		{desc(&controlv1.Stats{}), "filter_index", 600},
		{desc(&controlv1.FilterListRef{}), "list_id", 1},
		{desc(&controlv1.FilterListRef{}), "category", 2},
		{desc(&controlv1.FilterListRef{}), "position", 3},
		{desc(&controlv1.FilterListRef{}), "blob", 4},
		{desc(&controlv1.FilterIndexStats{}), "blocked_by_category", 8},
	} {
		f := c.msg.Fields().ByName(c.field)
		if f == nil || f.Number() != c.num {
			t.Errorf("%s.%s = %v, want field %d", c.msg.FullName(), c.field, f, c.num)
		}
	}
	in := &controlv1.ConfigSnapshot{
		FilterIndexMaxBytes: 64 << 20,
		Filter: &controlv1.FilterConfig{BlocklistRefs: []*controlv1.FilterListRef{{
			ListId: "0b6c3e2a-2d57-4a43-9a52-8f0e8bb3c1d1", Category: "gambling", Position: 7,
			Blob: &controlv1.BlobRef{Sha256: "ab", Size: 2, Name: "catalog:gambling:hagezi-gambling"},
		}}},
	}
	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out controlv1.ConfigSnapshot
	if err := proto.Unmarshal(raw, &out); err != nil || !proto.Equal(in, &out) {
		t.Fatalf("round trip: %v %v", &out, err)
	}
	st := &controlv1.Stats{FilterIndex: &controlv1.FilterIndexStats{Entries: 3, Bytes: 4096, Cpu: "cortex-a76", BlockedByCategory: map[string]uint64{"gambling": 2}}}
	if _, err := proto.Marshal(st); err != nil {
		t.Fatal(err)
	}
}
