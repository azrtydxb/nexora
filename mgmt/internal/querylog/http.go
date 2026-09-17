package querylog

import "net/http"

// Backend requests carry credentials and query data. Never forward either to a
// redirect destination (Go forwards custom ClickHouse authentication headers).
func rejectBackendRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}
