// Package querylog defines the query-log search backends behind the HTTP API.
package querylog

import (
	"context"
	"errors"
	"time"
)

// ErrBackendUnavailable is returned when the backend cannot be queried.
var ErrBackendUnavailable = errors.New("query log backend unavailable")

// Query is a query-log search. Empty strings and zero times do not filter.
type Query struct {
	From, To                                  time.Time
	Client, Name, QType, RCode, Cache, Filter string
	Limit                                     int
	Cursor                                    string
}

// Record is one logged DNS query.
type Record struct {
	Time                                                                     time.Time
	Client, Name, QType, RCode, Cache, Filter, Upstream, Transport, EngineID string
	DurationUS                                                               int64
}

// Page is one page of search results; NextCursor is empty on the last page.
type Page struct {
	Records    []Record
	NextCursor string
}

// Backend searches the query log.
type Backend interface {
	Name() string
	Search(ctx context.Context, q Query) (Page, error)
}

// Noop is a backend without records.
type Noop struct{}

// Name implements Backend.
func (Noop) Name() string { return "none" }

// Search implements Backend and returns an empty page.
func (Noop) Search(context.Context, Query) (Page, error) { return Page{}, nil }
