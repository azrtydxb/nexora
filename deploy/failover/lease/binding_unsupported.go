//go:build !linux

package lease

import (
	"context"
	"errors"
)

var errUnsupportedBinding = errors.New("driver binding requires Linux zero-offset CLOCK_BOOTTIME; no fallback")

func NewBootClock(string, string) (*BootClock, error) { return nil, errUnsupportedBinding }
func (c *BootClock) Now() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = errUnsupportedBinding
	return 0
}
func LaunchDriver(context.Context, DriverOptions) (*DriverGate, *BootClock, error) {
	return nil, nil, errUnsupportedBinding
}
