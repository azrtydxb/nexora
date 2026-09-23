package harness

import (
	"strings"
	"testing"
)

// Multicast crosses a veth link between two namespaces of the lab; nothing touches the pod network.
func TestNetLabVethMulticast(t *testing.T) {
	if !InNetLab() {
		RunInNetLab(t)
		return
	}
	e := New(t)
	lab := e.NewNetLab()
	_, peerIP := lab.Link("gw0", "lan0", "lanA", 0)
	lab.StartIn("lanA", "nexora-fixture", "mdns-responder", "--interface", "lan0",
		"--record", "printer.local. 120 IN A 10.254.0.9")
	out := e.RunBin(t, "nexora-fixture", "mdns-query", "--interface", "gw0", "--name", "printer.local.",
		"--type", "A", "--wait", "1s", "--legacy")
	if !strings.Contains(out, "ANSWER printer.local.\t120\tIN\tA\t10.254.0.9") {
		t.Fatalf("legacy query across the veth got:\n%s", out)
	}
	if peerIP.String() != "10.254.0.2" {
		t.Fatalf("peer address %s", peerIP)
	}
	quiet := e.RunBin(t, "nexora-fixture", "mdns-query", "--interface", "gw0", "--name", "nothere.local.",
		"--type", "A", "--wait", "500ms", "--legacy")
	if !strings.Contains(quiet, "PACKETS 0") {
		t.Fatalf("an unknown name was answered:\n%s", quiet)
	}
}
