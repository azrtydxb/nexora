package querylog

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"

	"github.com/piwi3910/nexora/mgmt/internal/control"
)

const (
	defaultLimit = 100
	maxLimit     = 1000
)

// Builtin receives engine query logs over OTLP on the management gRPC port and keeps the newest
// records in a fixed-size in-memory ring. Each management instance holds only the records its
// connected engines sent it.
type Builtin struct {
	collogspb.UnimplementedLogsServiceServer

	// Authenticate, when set, authenticates the calling engine (certificate revocation included);
	// without it only the client certificate's CN is used.
	Authenticate func(context.Context) (string, error)

	mu   sync.RWMutex
	ring []entry
	next uint64 // sequence number of the next record; ring[(next-1) % cap] is the newest
}

type entry struct {
	seq uint64
	rec Record
}

// NewBuiltin returns a ring holding at most capacity records.
func NewBuiltin(capacity int) *Builtin {
	return &Builtin{ring: make([]entry, 0, max(capacity, 1)), next: 1}
}

// Name implements Backend.
func (*Builtin) Name() string { return "builtin" }

// Export implements the OTLP LogsService for engines authenticated by their client certificate.
func (b *Builtin) Export(ctx context.Context, req *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error) {
	authenticate := b.Authenticate
	if authenticate == nil {
		authenticate = control.EngineID
	}
	engineID, err := authenticate(ctx)
	if err != nil {
		return nil, err
	}
	b.Ingest(engineID, req)
	return &collogspb.ExportLogsServiceResponse{}, nil
}

// Ingest appends every log record of req, attributed to the authenticated engineID.
func (b *Builtin) Ingest(engineID string, req *collogspb.ExportLogsServiceRequest) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, rl := range req.GetResourceLogs() {
		for _, sl := range rl.GetScopeLogs() {
			for _, lr := range sl.GetLogRecords() {
				ns := lr.GetTimeUnixNano()
				if ns == 0 {
					ns = lr.GetObservedTimeUnixNano()
				}
				r := recordFromAttributes(lr.GetAttributes())
				r.Time = time.Unix(0, int64(ns)).UTC()
				r.EngineID = engineID // the certificate, not the record's own claim
				e := entry{seq: b.next, rec: r}
				if len(b.ring) < cap(b.ring) {
					b.ring = append(b.ring, e)
				} else {
					b.ring[(b.next-1)%uint64(cap(b.ring))] = e
				}
				b.next++
			}
		}
	}
}

func recordFromAttributes(attrs []*commonpb.KeyValue) Record {
	var r Record
	for _, kv := range attrs {
		v := kv.GetValue()
		switch kv.GetKey() {
		case "client.address":
			r.Client = v.GetStringValue()
		case "dns.question.name":
			r.Name = v.GetStringValue()
		case "dns.question.type":
			r.QType = v.GetStringValue()
		case "dns.response.code":
			r.RCode = v.GetStringValue()
		case "nexora.cache":
			r.Cache = v.GetStringValue()
		case "nexora.filter", "nexora.filter.result":
			r.Filter = v.GetStringValue()
		case "nexora.filter.list_id":
			r.ListID = v.GetStringValue()
		case "nexora.filter.category":
			r.Category = v.GetStringValue()
		case "nexora.upstream":
			r.Upstream = v.GetStringValue()
		case "nexora.transport":
			r.Transport = v.GetStringValue()
		case "nexora.duration_us":
			r.DurationUS = v.GetIntValue()
		case "nexora.filter.source":
			r.Source = v.GetStringValue()
		case "nexora.filter.rule":
			r.Rule = v.GetStringValue()
		case "nexora.policy.group":
			r.PolicyGroupID = v.GetStringValue()
		case "nexora.rpz_zone":
			r.RPZZoneID = v.GetStringValue()
		case "nexora.rpz":
			if a := v.GetStringValue(); a != "none" {
				r.RPZAction = a
			}
		case "nexora.acl.refused":
			r.ACLRefused = v.GetStringValue()
		case "nexora.upstream_raced":
			r.UpstreamsRaced = v.GetIntValue()
		}
	}
	return r
}

// Search implements Backend: matching records newest first; the cursor is the decimal sequence
// number of the last record of the previous page.
func (b *Builtin) Search(_ context.Context, q Query) (Page, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	limit = min(limit, maxLimit)
	var before uint64
	if q.Cursor != "" {
		c, err := strconv.ParseUint(q.Cursor, 10, 64)
		if err != nil {
			return Page{}, ErrInvalidCursor
		}
		before = c
	}
	name := strings.ToLower(strings.TrimSuffix(q.Name, "."))
	groups := slices.Clone(q.PolicyGroups)
	for i, g := range groups {
		if g == GlobalPolicyGroup {
			groups[i] = ""
		}
	}

	b.mu.RLock()
	defer b.mu.RUnlock()
	var page Page
	var lastSeq uint64
	n := uint64(len(b.ring))
	for i := uint64(1); i <= n; i++ {
		e := b.ring[(b.next-1-i)%uint64(cap(b.ring))]
		if before != 0 && e.seq >= before {
			continue
		}
		if !matches(e.rec, q, name, groups) {
			continue
		}
		if len(page.Records) == limit {
			page.NextCursor = strconv.FormatUint(lastSeq, 10)
			break
		}
		page.Records = append(page.Records, e.rec)
		lastSeq = e.seq
	}
	return page, nil
}

// Top implements Topper over the records held in the ring.
func (b *Builtin) Top(_ context.Context, q TopQuery) ([]TopEntry, error) {
	counts := map[string]int64{}
	search := q.SearchQuery()
	name := strings.ToLower(strings.TrimSuffix(search.Name, "."))
	groups := slices.Clone(search.PolicyGroups)
	for i, g := range groups {
		if g == GlobalPolicyGroup {
			groups[i] = ""
		}
	}
	b.mu.RLock()
	for _, e := range b.ring {
		r := e.rec
		if !matches(r, search, name, groups) {
			continue
		}
		var key string
		switch q.Field {
		case TopName:
			key = r.Name
		case TopClient:
			key = r.Client
		case TopCategory:
			key = r.Category
		}
		if key != "" {
			counts[key]++
		}
	}
	b.mu.RUnlock()
	out := make([]TopEntry, 0, len(counts))
	for k, n := range counts {
		out = append(out, TopEntry{Key: k, Count: n})
	}
	slices.SortFunc(out, func(a, b TopEntry) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), strings.Compare(a.Key, b.Key))
	})
	return out[:min(len(out), max(q.Limit, 0))], nil
}

// matches reports whether r passes q; lowerName is the normalised name fragment and groups the
// policy group ids with "global" mapped to "".
func matches(r Record, q Query, lowerName string, groups []string) bool {
	in := func(v string, set []string) bool { return len(set) == 0 || slices.Contains(set, v) }
	return (q.From.IsZero() || !r.Time.Before(q.From)) &&
		(q.To.IsZero() || !r.Time.After(q.To)) &&
		(q.Client == "" || r.Client == q.Client) &&
		(lowerName == "" || strings.Contains(strings.ToLower(r.Name), lowerName)) &&
		in(r.QType, q.QTypes) && in(r.RCode, q.RCodes) && in(r.Cache, q.Caches) &&
		in(r.Filter, q.Filters) && in(r.Category, q.Categories) && in(r.Source, q.Sources) &&
		in(r.ListID, q.ListIDs) && in(r.PolicyGroupID, groups) && in(r.EngineID, q.EngineIDs)
}
