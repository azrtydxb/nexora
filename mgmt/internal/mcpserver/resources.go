package mcpserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/klauspost/compress/zstd"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// maxFilterListLines bounds the filter-list content resource.
const maxFilterListLines = 10_000

type resource struct {
	URI, Name, Description, OperationID string
	Query                               url.Values
}

var resources = []resource{
	{"nexora://fleet/status", "fleet-status", "Fleet summary: engines by state and config version spread.", "getFleetSummary", nil},
	{"nexora://policy-groups", "policy-groups", "The per-client policy groups.", "listPolicyGroups", nil},
	{"nexora://engine-groups", "engine-groups", "The engine groups.", "listEngineGroups", nil},
	{"nexora://query-log/recent", "query-log-recent", "The 100 most recent query log entries.", "searchQueryLog", url.Values{"limit": {"100"}}},
}

const (
	filterListPrefix   = "nexora://filter-lists/"
	filterListSuffix   = "/content"
	filterListTemplate = filterListPrefix + "{id}" + filterListSuffix
)

func (s *server) listResources(*http.Request, auth.Principal, json.RawMessage) (any, *rpcError) {
	out := make([]map[string]string, len(resources))
	for i, res := range resources {
		out[i] = map[string]string{"uri": res.URI, "name": res.Name, "description": res.Description, "mimeType": "application/json"}
	}
	return map[string]any{"resources": out}, nil
}

func (s *server) listResourceTemplates(*http.Request, auth.Principal, json.RawMessage) (any, *rpcError) {
	return map[string]any{"resourceTemplates": []map[string]string{{
		"uriTemplate": filterListTemplate, "name": "filter-list-content", "mimeType": "text/plain",
		"description": "The first 10,000 names of a filter list's current content.",
	}}}, nil
}

func (s *server) readResource(r *http.Request, _ auth.Principal, params json.RawMessage) (any, *rpcError) {
	var in struct {
		URI string `json:"uri"`
	}
	if err := decodeParams(params, &in); err != nil {
		return nil, err
	}
	contents := func(mimeType, text string) (any, *rpcError) {
		return map[string]any{"contents": []map[string]string{{"uri": in.URI, "mimeType": mimeType, "text": text}}}, nil
	}
	for _, res := range resources {
		if res.URI == in.URI {
			body, rerr := s.replayGET(r, res.OperationID, nil, res.Query)
			if rerr != nil {
				return nil, rerr
			}
			return contents("application/json", bounded(body))
		}
	}
	id, ok := strings.CutPrefix(in.URI, filterListPrefix)
	if id, ok = strings.CutSuffix(id, filterListSuffix); !ok || id == "" || strings.Contains(id, "/") {
		return nil, &rpcError{codeResourceNotFound, "resource not found: " + in.URI}
	}
	text, rerr := s.filterListContent(r, id)
	if rerr != nil {
		return nil, rerr
	}
	return contents("text/plain", text)
}

// filterListContent authorizes through a replayed getFilterList, then returns the first names of the
// list's current blob.
func (s *server) filterListContent(r *http.Request, id string) (string, *rpcError) {
	body, rerr := s.replayGET(r, "getFilterList", map[string]string{"id": id}, nil)
	if rerr != nil {
		return "", rerr
	}
	var list struct {
		CurrentBlobSHA256 *string `json:"current_blob_sha256"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return "", &rpcError{codeInternal, "decode filter list: " + err.Error()}
	}
	if list.CurrentBlobSHA256 == nil {
		return "", nil // never fetched successfully
	}
	if s.o.Store == nil {
		return "", &rpcError{codeInternal, "filter list content is unavailable"}
	}
	var data []byte
	err := s.o.Store.Pool.QueryRow(r.Context(), "select data from blobs where sha256 = $1", *list.CurrentBlobSHA256).Scan(&data)
	if err = store.MapError(err); errors.Is(err, store.ErrNotFound) {
		return "", &rpcError{codeResourceNotFound, "filter list content not found"}
	} else if err != nil {
		return "", &rpcError{codeInternal, "read filter list content: " + err.Error()}
	}
	// Streamed, so a large list costs only the lines returned.
	dec, err := zstd.NewReader(bytes.NewReader(data), zstd.WithDecoderConcurrency(1))
	if err != nil {
		return "", &rpcError{codeInternal, "decode filter list content: " + err.Error()}
	}
	defer dec.Close()
	var out strings.Builder
	sc := bufio.NewScanner(dec)
	for n := 0; n < maxFilterListLines && sc.Scan(); n++ {
		out.Write(sc.Bytes())
		out.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		return "", &rpcError{codeInternal, "decode filter list content: " + err.Error()}
	}
	return out.String(), nil
}
