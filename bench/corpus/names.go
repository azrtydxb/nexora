// Package corpus generates the deterministic query names used by the performance gate.
package corpus

import "strconv"

// Names returns perf-<i>.example. for i in [0, n).
func Names(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "perf-" + strconv.Itoa(i) + ".example."
	}
	return out
}
