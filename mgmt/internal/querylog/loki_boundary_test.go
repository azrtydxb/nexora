package querylog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"text/template"
	"time"
)

type boundaryTransport func(*http.Request) (*http.Response, error)

func (f boundaryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Exercise Top's emitted query with millisecond range selectors and Loki's actual Go-template
// timestamp type. Real-Loki tests separately verify parsing and backend execution.
func TestLokiTopExactBoundaries(t *testing.T) {
	for _, width := range []time.Duration{0, 2 * time.Nanosecond, 1900 * time.Microsecond, 2 * time.Second} {
		t.Run(width.String(), func(t *testing.T) {
			from := time.Date(2026, 9, 17, 12, 0, 59, 999999999, time.UTC)
			to := from.Add(width)
			times := []time.Time{from.Add(-time.Nanosecond), from, to, to.Add(time.Nanosecond), to.Add(500 * time.Microsecond)}
			l := &Loki{selector: lokiDefaultSelector, lookback: time.Hour, url: "http://loki", client: &http.Client{Transport: boundaryTransport(func(r *http.Request) (*http.Response, error) {
				q := r.URL.Query().Get("query")
				at, _ := strconv.ParseInt(r.URL.Query().Get("time"), 10, 64)
				match := regexp.MustCompile(`\[(\d+)ms\]`).FindStringSubmatch(q)
				if len(match) != 2 {
					return nil, fmt.Errorf("missing range: %s", q)
				}
				ms, _ := strconv.ParseInt(match[1], 10, 64)
				var n int64
				for _, ts := range times {
					if ts.UnixNano() <= at-ms*int64(time.Millisecond) || ts.UnixNano() > at {
						continue
					}
					if m := regexp.MustCompile(`nexora_window=("(?:[^"\\]|\\.)*")`).FindStringSubmatch(q); len(m) > 0 {
						raw, err := strconv.Unquote(m[1])
						if err != nil {
							return nil, err
						}
						tmpl, err := template.New("window").Funcs(template.FuncMap{"__timestamp__": func() time.Time { return ts }}).Parse(raw)
						if err != nil {
							return nil, err
						}
						var out bytes.Buffer
						if err := tmpl.Execute(&out, nil); err != nil {
							return nil, err
						}
						if out.String() != "true" {
							continue
						}
					}
					n++
				}
				body, _ := json.Marshal(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": []any{map[string]any{"metric": map[string]string{lokiName: "edge.test."}, "value": []any{0, strconv.FormatInt(n, 10)}}}}})
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})}}
			got, err := l.Top(context.Background(), TopQuery{From: from, To: to, Field: TopName, Limit: 1})
			if err != nil || len(got) != 1 || got[0].Count != 2 {
				t.Fatalf("Top %v %v; want exactly two endpoint records", got, err)
			}
		})
	}
}
