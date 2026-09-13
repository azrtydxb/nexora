// Package zone holds hosted-zone logic shared by the management plane.
package zone

// SerialLess reports a < b under RFC 1982 with SERIAL_BITS = 32.
func SerialLess(a, b uint32) bool {
	if a == b {
		return false
	}
	const half = uint32(1) << 31
	return (a < b && b-a < half) || (a > b && a-b > half)
}

// SerialNext is the next primary serial; it wraps 4294967295 -> 0.
func SerialNext(s uint32) uint32 { return s + 1 }
