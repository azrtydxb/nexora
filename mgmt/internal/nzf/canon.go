package nzf

import (
	"bytes"
	"sort"
)

// CanonicalKey maps an uncompressed wire name to a byte string whose bytewise
// order is the RFC 4034 §6.1 canonical order: labels from the root, lowercase,
// each label terminated by 0x00; label octets 0x00 and 0x01 are escaped to
// 0x01 0x01 and 0x01 0x02 so a shorter label sorts before its extensions.
// The engine twin is `authoritative::name::canon_key`; both must produce identical bytes.
func CanonicalKey(wire []byte) []byte {
	var starts [128]int
	n := 0
	for i := 0; i < len(wire) && wire[i] != 0 && n < len(starts) && i+1+int(wire[i]) <= len(wire); i += int(wire[i]) + 1 {
		starts[n] = i
		n++
	}
	key := make([]byte, 0, len(wire)+n)
	for k := n - 1; k >= 0; k-- {
		o := starts[k]
		for _, b := range wire[o+1 : o+1+int(wire[o])] {
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			switch b {
			case 0:
				key = append(key, 1, 1)
			case 1:
				key = append(key, 1, 2)
			default:
				key = append(key, b)
			}
		}
		key = append(key, 0)
	}
	return key
}

// SortRecords orders records by (CanonicalKey(owner), type, rdata bytes).
func SortRecords(rs []Record) {
	keys := make([][]byte, len(rs))
	for i := range rs {
		keys[i] = CanonicalKey(rs[i].Owner)
	}
	idx := make([]int, len(rs))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ra, rb := rs[idx[a]], rs[idx[b]]
		if c := bytes.Compare(keys[idx[a]], keys[idx[b]]); c != 0 {
			return c < 0
		}
		if ra.Type != rb.Type {
			return ra.Type < rb.Type
		}
		return bytes.Compare(ra.RData, rb.RData) < 0
	})
	sorted := make([]Record, len(rs))
	for i, j := range idx {
		sorted[i] = rs[j]
	}
	copy(rs, sorted)
}
