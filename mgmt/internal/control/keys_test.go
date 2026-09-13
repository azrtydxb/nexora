package control_test

import (
	"testing"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/control"
)

func TestKeysFilteredToTargetSnapshot(t *testing.T) {
	snap := &controlv1.ConfigSnapshot{
		RpzZones: []*controlv1.RpzZone{{Id: "zone-a"}},
		AuthZones: []*controlv1.AuthZone{{
			Name:           "served.test.",
			Transfer:       &controlv1.TransferPolicy{TsigKey: "xfr.key."},
			Notify:         []*controlv1.NotifyTarget{{Address: "192.0.2.9:53", TsigKey: "notify.key."}},
			UpdateTsigKeys: []string{"update.key."},
		}, {
			Name:            "secondary.test.",
			Primaries:       []string{"192.0.2.10:53", "192.0.2.11:53"},
			PrimaryTsigKeys: []string{"primary.key.", ""},
		}},
	}
	rpz := control.FilterRPZKeys(snap, &controlv1.RpzTsigKeys{Keys: []*controlv1.RpzTsigKey{{ZoneId: "zone-a"}, {ZoneId: "zone-b"}}})
	if len(rpz.Keys) != 1 || rpz.Keys[0].ZoneId != "zone-a" {
		t.Fatalf("rpz keys %v, want only zone-a", rpz.Keys)
	}
	km := control.FilterKeyMaterial(snap, &controlv1.KeyMaterial{TsigKeys: []*controlv1.TsigSecret{
		{Name: "xfr.key."}, {Name: "notify.key."}, {Name: "update.key."}, {Name: "primary.key."}, {Name: "other-group.key."},
		{Name: "no-zone.key."},
	}}, map[string]bool{"xfr.key.": true, "notify.key.": true, "update.key.": true, "primary.key.": true, "other-group.key.": true})
	names := map[string]bool{}
	for _, k := range km.TsigKeys {
		names[k.Name] = true
	}
	if len(names) != 5 || names["other-group.key."] || !names["no-zone.key."] {
		t.Fatalf("key material %v, want the four keys the snapshot names and the key no zone names", names)
	}
}
