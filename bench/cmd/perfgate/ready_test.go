package main

import "testing"

func TestParseReady(t *testing.T) {
	got, err := parseReady("udp=127.0.0.1:41234 tcp=127.0.0.1:41234 metrics=127.0.0.1:9153")
	if err != nil || got["udp"] != "127.0.0.1:41234" || got["metrics"] != "127.0.0.1:9153" {
		t.Fatalf("parseReady: %v %v", got, err)
	}
	for _, bad := range []string{"udp=127.0.0.1:0", "udp", "tcp=127.0.0.1:53"} {
		if _, err := parseReady(bad); err == nil {
			t.Fatalf("parseReady(%q) accepted", bad)
		}
	}
}
