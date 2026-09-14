package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/api"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

type fakeLogReader struct {
	err  error
	last *controlv1.LogRequest
}

func (f *fakeLogReader) Read(_ context.Context, _ uuid.UUID, req *controlv1.LogRequest) (*controlv1.LogBatch, error) {
	f.last = req
	if f.err != nil {
		return nil, f.err
	}
	return &controlv1.LogBatch{RequestId: req.RequestId, LastSeq: 9, OldestSeq: 2, Lines: []*controlv1.LogLine{
		{Seq: 9, UnixMs: 1_700_000_000_123, Level: controlv1.LogLevel_LOG_LEVEL_WARN, Message: "nexora-engine: snapshot rejected"},
	}}, nil
}

// GetEngineLogs maps parameters onto the LogRequest, lines onto EngineLogs, and the broker errors
// onto 409, 504 and (for an engine whose stats predate M6) 501.
func TestGetEngineLogsMapsRequestsAndErrors(t *testing.T) {
	reader := &fakeLogReader{}
	e := newAPIWith(t, func(d *api.Deps) { d.EngineLogs = reader })
	admin := e.client(t)
	if code := admin.do("POST", "/setup", map[string]any{"token": e.setup, "username": "admin", "email": "a@example.test", "password": "admin-password-1"}, nil); code != http.StatusCreated {
		t.Fatalf("setup -> %d", code)
	}
	id := storetest.InsertEngine(t, e.st, "logs-1", store.DefaultEngineGroupID)
	path := "/engines/" + id.String() + "/logs"

	var out api.EngineLogs
	if code := admin.do("GET", path+"?after=4&level=warn&q=Rejected&limit=50", nil, &out); code != http.StatusOK {
		t.Fatalf("logs -> %d", code)
	}
	if r := reader.last; r.AfterSeq != 4 || r.MinLevel != controlv1.LogLevel_LOG_LEVEL_WARN || r.Contains != "Rejected" || r.Limit != 50 {
		t.Fatalf("request %+v", r)
	}
	if out.EngineId != id || out.LastSeq != 9 || out.OldestSeq != 2 || len(out.Lines) != 1 ||
		out.Lines[0].Level != api.EngineLogsLinesLevelWarn || out.Lines[0].Time.UnixMilli() != 1_700_000_000_123 {
		t.Fatalf("logs %+v", out)
	}
	if code := admin.do("GET", path, nil, nil); code != http.StatusOK || reader.last.MinLevel != controlv1.LogLevel_LOG_LEVEL_DEBUG || reader.last.Limit != 1000 {
		t.Fatalf("defaults -> %d %+v", code, reader.last)
	}

	var apiErr struct {
		Code string `json:"code"`
	}
	for _, c := range []struct {
		err  error
		want int
		code string
	}{
		{api.ErrEngineDisconnected, http.StatusConflict, "engine_disconnected"},
		{api.ErrEngineTimeout, http.StatusGatewayTimeout, "engine_timeout"},
	} {
		reader.err = c.err
		if code := admin.do("GET", path, nil, &apiErr); code != c.want || apiErr.Code != c.code {
			t.Fatalf("%v -> %d %q", c.err, code, apiErr.Code)
		}
	}
	// The same timeout from an engine whose newest stats carry no M6 field is an old build.
	raw, _ := proto.Marshal(&controlv1.Stats{QueriesTotal: 1})
	if _, err := e.st.Pool.Exec(e.ctx, "insert into engine_stats (engine_id, at, stats) values ($1, now(), $2)", id, raw); err != nil {
		t.Fatal(err)
	}
	if code := admin.do("GET", path, nil, &apiErr); code != http.StatusNotImplemented || apiErr.Code != "engine_unsupported" {
		t.Fatalf("pre-M6 timeout -> %d %q", code, apiErr.Code)
	}
	if code := admin.do("GET", "/engines/"+uuid.NewString()+"/logs", nil, &apiErr); code != http.StatusNotFound {
		t.Fatalf("unknown engine -> %d", code)
	}
}
