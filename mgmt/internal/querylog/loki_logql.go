package querylog

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Structured metadata names of the engine's OTLP attributes in Loki (dots become underscores).
const (
	lokiClient       = "client_address"
	lokiName         = "dns_question_name"
	lokiQType        = "dns_question_type"
	lokiRCode        = "dns_response_code"
	lokiCache        = "nexora_cache"
	lokiFilter       = "nexora_filter"
	lokiFilterResult = "nexora_filter_result"
	lokiListID       = "nexora_filter_list_id"
	lokiCategory     = "nexora_filter_category"
	lokiSource       = "nexora_filter_source"
	lokiRule         = "nexora_filter_rule"
	lokiPolicyGroup  = "nexora_policy_group"
	lokiRPZZone      = "nexora_rpz_zone"
	lokiRPZAction    = "nexora_rpz"
	lokiACLRefused   = "nexora_acl_refused"
	lokiUpstream     = "nexora_upstream"
	lokiRaced        = "nexora_upstream_raced"
	lokiTransport    = "nexora_transport"
	lokiEngineID     = "nexora_engine_id"
	lokiDurationUS   = "nexora_duration_us"
)

// lokiTopLabels maps each top list field to its structured metadata name.
var lokiTopLabels = map[TopField]string{TopName: lokiName, TopClient: lokiClient, TopCategory: lokiCategory}

// lokiQuote writes s as a LogQL string literal (LogQL unquotes strings with Go's rules).
func lokiQuote(s string) string { return strconv.Quote(s) }

// lokiAny is an anchored regex matching exactly one of values, each matched literally.
func lokiAny(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = regexp.QuoteMeta(v)
	}
	return "^(?:" + strings.Join(quoted, "|") + ")$"
}

// lokiStages is the label-filter pipeline of q's field filters (time, limit and cursor aside). Every
// user value is a quoted literal: regex values are regexp.QuoteMeta-escaped, then Go-quoted.
func lokiStages(q Query) string {
	var b strings.Builder
	if q.Client != "" {
		b.WriteString(" | " + lokiClient + "=" + lokiQuote(q.Client))
	}
	if name := strings.TrimSuffix(q.Name, "."); name != "" {
		b.WriteString(" | " + lokiName + "=~" + lokiQuote("(?i)^.*"+regexp.QuoteMeta(strings.ToLower(name))+".*$"))
	}
	groups := slices.Clone(q.PolicyGroups)
	for i, g := range groups {
		if g == GlobalPolicyGroup {
			groups[i] = "" // global clients carry no attribute or an empty one
		}
	}
	for _, f := range []struct {
		label  string
		values []string
	}{
		{lokiQType, q.QTypes}, {lokiRCode, q.RCodes}, {lokiCache, q.Caches}, {lokiCategory, q.Categories},
		{lokiSource, q.Sources}, {lokiListID, q.ListIDs}, {lokiEngineID, q.EngineIDs}, {lokiPolicyGroup, groups},
	} {
		if len(f.values) > 0 {
			b.WriteString(" | " + f.label + "=~" + lokiQuote(lokiAny(f.values)))
		}
	}
	if len(q.Filters) > 0 {
		// Collectors with the OpenSearch transform rename nexora.filter to nexora.filter.result.
		re := lokiQuote(lokiAny(q.Filters))
		b.WriteString(" | (" + lokiFilter + "=~" + re + " or " + lokiFilterResult + "=~" + re + ")")
	}
	return b.String()
}

// lokiPartitionChars are the partition characters of partitions 0..35.
const lokiPartitionChars = "0123456789abcdefghijklmnopqrstuvwxyz"

// LokiPartitionRegexes returns the 37 Top partitions of field's keys. Every string matches exactly
// one regex, case-insensitively.
//   - Names: partition i holds the names whose first character, after one optional leading "www.",
//     is lokiPartitionChars[i], so a.com. and b.com. fall apart. Partition 36 ("none") holds the
//     rest: empty, "www." alone, or a first character outside [0-9a-z] (the root ".", "_dmarc.").
//   - Clients and categories: partition i holds the keys whose last character, ignoring one
//     trailing dot, is lokiPartitionChars[i] (IPv4 clients share their first digits but not their
//     last); partition 36 holds the rest.
func LokiPartitionRegexes(field TopField) []string {
	out := make([]string, 0, len(lokiPartitionChars)+1)
	if field == TopName {
		for _, c := range lokiPartitionChars {
			if c == 'w' {
				// A w name without the www. prefix, or www. followed by w. RE2 has no lookahead, so
				// "not www." is spelled out: w then not w, ww then not w, www then not a dot.
				out = append(out, `(?is)^(?:www\.w.*|w(?:[^w].*|w(?:[^w].*|w(?:[^.].*)?)?)?)$`)
				continue
			}
			out = append(out, `(?is)^(?:www\.)?`+string(c)+`.*$`)
		}
		return append(out, `(?is)^(?:|www\.(?:[^0-9a-z].*)?|[^0-9a-z].*)$`)
	}
	for _, c := range lokiPartitionChars {
		out = append(out, `(?is)^.*`+string(c)+`\.?$`)
	}
	return append(out, `(?is)^(?:\.?|.*[^0-9a-z.]|.*[^0-9a-z]\.)$`)
}

// lokiKey is the tiebreak key of an entry: the hex SHA-256 of its line and its structured metadata
// sorted by name.
func lokiKey(line string, md map[string]string) string {
	h := sha256.New()
	h.Write([]byte(line))
	names := make([]string, 0, len(md))
	for k := range md {
		names = append(names, k)
	}
	slices.Sort(names)
	for _, k := range names {
		h.Write([]byte("\x00" + k + "=" + md[k]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// recordFromLoki builds the record of an entry at ts from its structured metadata (the line only
// repeats client, name, type, transport and engine).
//
// debt: Loki drops entries with equal timestamp and line in one stream; the transform/loki body makes that need an identical query from one client in one microsecond; revisit if a conformance or kw count ever differs
func recordFromLoki(ts int64, md map[string]string) Record {
	integer := func(k string) int64 {
		n, err := strconv.ParseInt(md[k], 10, 64)
		if err != nil {
			return 0
		}
		return n
	}
	r := Record{
		Time:   time.Unix(0, ts).UTC(),
		Client: md[lokiClient], Name: md[lokiName], QType: md[lokiQType], RCode: md[lokiRCode], Cache: md[lokiCache],
		Filter: md[lokiFilter], Upstream: md[lokiUpstream], Transport: md[lokiTransport], EngineID: md[lokiEngineID],
		ListID: md[lokiListID], Category: md[lokiCategory], Source: md[lokiSource], Rule: md[lokiRule],
		PolicyGroupID: md[lokiPolicyGroup], RPZZoneID: md[lokiRPZZone], RPZAction: md[lokiRPZAction],
		ACLRefused: md[lokiACLRefused], UpstreamsRaced: integer(lokiRaced), DurationUS: integer(lokiDurationUS),
	}
	if r.Filter == "" {
		r.Filter = md[lokiFilterResult]
	}
	if r.RPZAction == "none" {
		r.RPZAction = ""
	}
	return r
}
