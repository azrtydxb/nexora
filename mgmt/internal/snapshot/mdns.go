package snapshot

import (
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/fleet"
)

// AddMdns sets snap.Mdns from engine group g's settings: nil while the gateway and reflection are
// both off, so an engine without M8 and one with mDNS off see the same snapshot.
func AddMdns(snap *controlv1.ConfigSnapshot, g fleet.EngineGroup) {
	if !g.MdnsEnabled && !g.MdnsReflect {
		snap.Mdns = nil
		return
	}
	snap.Mdns = &controlv1.MdnsConfig{
		Enabled:           g.MdnsEnabled,
		Interfaces:        append([]string{}, g.MdnsInterfaces...),
		TimeoutMs:         uint32(g.MdnsTimeoutMS),
		Reflect:           g.MdnsReflect,
		ReflectInterfaces: append([]string{}, g.MdnsReflectInterfaces...),
	}
}
