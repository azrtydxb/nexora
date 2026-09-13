package control

import (
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
)

// FilterRPZKeys returns the RPZ TSIG keys of the RPZ zones in snap (the elements are shared with
// keys, not copied).
func FilterRPZKeys(snap *controlv1.ConfigSnapshot, keys *controlv1.RpzTsigKeys) *controlv1.RpzTsigKeys {
	zones := map[string]bool{}
	for _, z := range snap.GetRpzZones() {
		zones[z.Id] = true
	}
	out := &controlv1.RpzTsigKeys{}
	for _, k := range keys.GetKeys() {
		if zones[k.ZoneId] {
			out.Keys = append(out.Keys, k)
		}
	}
	return out
}

// FilterKeyMaterial returns the hosted-zone TSIG keys of km an engine running snap may hold: those
// an auth zone in snap names (transfer key, a NOTIFY target's key, an update key or a primary's
// key) and those no zone of any engine group names (zoned lists the names zones use). A key only
// zones of other engine groups use is left out. The elements are shared with km.
func FilterKeyMaterial(snap *controlv1.ConfigSnapshot, km *controlv1.KeyMaterial, zoned map[string]bool) *controlv1.KeyMaterial {
	names := map[string]bool{}
	for _, z := range snap.GetAuthZones() {
		names[z.GetTransfer().GetTsigKey()] = true
		for _, n := range z.Notify {
			names[n.TsigKey] = true
		}
		for _, k := range z.UpdateTsigKeys {
			names[k] = true
		}
		for _, k := range z.PrimaryTsigKeys {
			names[k] = true
		}
	}
	delete(names, "")
	out := &controlv1.KeyMaterial{}
	for _, k := range km.GetTsigKeys() {
		if names[k.Name] || !zoned[k.Name] {
			out.TsigKeys = append(out.TsigKeys, k)
		}
	}
	return out
}
