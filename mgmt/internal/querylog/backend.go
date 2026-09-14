// Package querylog defines the query-log search backends behind the HTTP API.
package querylog

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ErrBackendUnavailable is returned when the backend cannot be queried.
var ErrBackendUnavailable = errors.New("query log backend unavailable")

// ErrInvalidCursor is returned for a cursor the backend did not issue.
var ErrInvalidCursor = errors.New("invalid query log cursor")

// Query is a query-log search. Empty strings, empty slices and zero times do not filter. Values
// within one slice are alternatives (OR); different fields all apply (AND). Name is a
// case-insensitive substring of the query name with one trailing dot ignored, and PolicyGroups
// value "global" matches records without a policy group.
type Query struct {
	From, To                                                                               time.Time
	Client, Name                                                                           string
	QTypes, RCodes, Caches, Filters, Categories, Sources, ListIDs, PolicyGroups, EngineIDs []string
	Limit                                                                                  int
	Cursor                                                                                 string
}

// GlobalPolicyGroup is the PolicyGroups value that matches records without a policy group.
const GlobalPolicyGroup = "global"

// Record is one logged DNS query.
type Record struct {
	Time                                                                     time.Time
	Client, Name, QType, RCode, Cache, Filter, Upstream, Transport, EngineID string
	// ListID and Category attribute a filter decision to the matching list; empty otherwise.
	ListID, Category string
	// Source, Rule, PolicyGroupID, RPZZoneID, RPZAction and ACLRefused explain the decision; empty
	// when the engine sent no attribution.
	Source, Rule, PolicyGroupID, RPZZoneID, RPZAction, ACLRefused string
	UpstreamsRaced                                                int64
	DurationUS                                                    int64
}

// EscapeWildcard escapes \, * and ? for an OpenSearch wildcard value.
func EscapeWildcard(s string) string {
	return strings.NewReplacer("\\", "\\\\", "*", "\\*", "?", "\\?").Replace(s)
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

// TopField is the record field a top list counts: "name", "client" or "category".
type TopField string

// Top list fields.
const (
	TopName     TopField = "name"
	TopClient   TopField = "client"
	TopCategory TopField = "category"
)

// TopQuery counts records between From and To (zero times do not bound) whose filter result is one
// of Filters (empty: any) by Field, returning at most Limit entries.
type TopQuery struct {
	From, To time.Time
	Field    TopField
	Filters  []string
	Limit    int
}

// TopEntry is one key of a top list with its record count.
type TopEntry struct {
	Key   string
	Count int64
}

// Topper is a backend that aggregates top lists; entries are ordered by count descending, then key,
// and records with an empty key are not counted.
type Topper interface {
	Top(ctx context.Context, q TopQuery) ([]TopEntry, error)
}

// Noop is a backend without records.
type Noop struct{}

// Name implements Backend.
func (Noop) Name() string { return "none" }

// Search implements Backend and returns an empty page.
func (Noop) Search(context.Context, Query) (Page, error) { return Page{}, nil }
