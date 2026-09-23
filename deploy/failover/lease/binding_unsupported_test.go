//go:build !linux

package lease

import (
	"context"
	"testing"
)

func TestUnsupportedBinding(t *testing.T) {
	if c, e := NewBootClock("", ""); e == nil || c != nil {
		t.Fatal("clock fallback")
	}
	c := &BootClock{}
	if c.Now() != 0 || c.Err() == nil {
		t.Fatal("clock must fail closed")
	}
	if g, c, e := LaunchDriver(context.Background(), DriverOptions{Enabled: true}); e == nil || g != nil || c != nil {
		t.Fatal("driver fallback")
	}
}
