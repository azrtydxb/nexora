package zone

import "testing"

func TestSerialArithmeticRFC1982(t *testing.T) {
	cases := []struct {
		a, b uint32
		less bool
	}{
		{1, 2, true},
		{2, 1, false},
		{7, 7, false},
		{4294967295, 0, true},
		{0, 4294967295, false},
		{4294967295, 2147483646, true},
		{0, 2147483648, false}, // distance exactly 2^31 is undefined: not less either way
		{2147483648, 0, false},
	}
	for _, c := range cases {
		if got := SerialLess(c.a, c.b); got != c.less {
			t.Errorf("SerialLess(%d, %d) = %v, want %v", c.a, c.b, got, c.less)
		}
	}
	if SerialNext(4294967295) != 0 {
		t.Fatalf("SerialNext must wrap 4294967295 to 0")
	}
	if !SerialLess(4294967295, SerialNext(4294967295)) {
		t.Fatalf("the wrapped serial must be RFC 1982-greater")
	}
}
