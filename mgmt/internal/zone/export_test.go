package zone_test

import (
	"context"
	"strings"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/zone"
	"github.com/piwi3910/nexora/mgmt/internal/zonefile"
)

// TestExportToIsStableAndReparses catches an export whose order depends on insertion order, or
// whose output does not parse back.
func TestExportToIsStableAndReparses(t *testing.T) {
	ctx := context.Background()
	export := func(order []int) string {
		s := newService(t)
		z := createZone(t, s, "stable.test.")
		recs := []zone.RecordInput{
			{Name: "www.stable.test.", Type: "A", TTL: 300, Data: "192.0.2.1"},
			{Name: "a.b.stable.test.", Type: "TXT", TTL: 300, Data: `"x"`},
			{Name: "stable.test.", Type: "MX", TTL: 300, Data: "10 mail.stable.test."},
			{Name: "mail.stable.test.", Type: "AAAA", TTL: 300, Data: "2001:db8::1"},
		}
		for _, i := range order {
			if _, err := s.CreateRecord(ctx, actor, z.ID, recs[i]); err != nil {
				t.Fatal(err)
			}
		}
		var b strings.Builder
		if err := s.ExportTo(ctx, z.ID, &b); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	one, two := export([]int{0, 1, 2, 3}), export([]int{3, 2, 1, 0})
	if one != two {
		t.Fatalf("export depends on insertion order:\n%s\n---\n%s", one, two)
	}
	res, err := zonefile.Parse(strings.NewReader(one), "stable.test.", zonefile.Options{AllowedTypes: zone.ManagedTypes, MaxRecords: zone.MaxImportRecords})
	if err != nil || len(res.Records) < 5 {
		t.Fatalf("export does not reparse: %v (%d records)", err, len(res.Records))
	}
}
