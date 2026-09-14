package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/control"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// EngineLogReader reads an engine's log ring buffer through whichever instance holds its stream.
type EngineLogReader interface {
	Read(ctx context.Context, engineID uuid.UUID, req *controlv1.LogRequest) (*controlv1.LogBatch, error)
}

var (
	ErrEngineDisconnected = control.ErrEngineDisconnected
	ErrEngineTimeout      = control.ErrEngineTimeout
	ErrEngineUnsupported  = errors.New("this engine version cannot send logs")
)

// engineLogsError maps the EngineLogReader sentinels to their API errors; other errors pass through.
func engineLogsError(err error) error {
	switch {
	case errors.Is(err, ErrEngineDisconnected):
		return coded(http.StatusConflict, "engine_disconnected", "%v", err)
	case errors.Is(err, ErrEngineTimeout):
		return coded(http.StatusGatewayTimeout, "engine_timeout", "%v", err)
	case errors.Is(err, ErrEngineUnsupported):
		return coded(http.StatusNotImplemented, "engine_unsupported", "%v", err)
	}
	return err
}

var (
	logLevelsIn = map[GetEngineLogsParamsLevel]controlv1.LogLevel{
		"error": controlv1.LogLevel_LOG_LEVEL_ERROR, "warn": controlv1.LogLevel_LOG_LEVEL_WARN,
		"info": controlv1.LogLevel_LOG_LEVEL_INFO, "debug": controlv1.LogLevel_LOG_LEVEL_DEBUG,
	}
	logLevelsOut = map[controlv1.LogLevel]EngineLogsLinesLevel{
		controlv1.LogLevel_LOG_LEVEL_ERROR: EngineLogsLinesLevelError, controlv1.LogLevel_LOG_LEVEL_WARN: EngineLogsLinesLevelWarn,
		controlv1.LogLevel_LOG_LEVEL_INFO: EngineLogsLinesLevelInfo, controlv1.LogLevel_LOG_LEVEL_DEBUG: EngineLogsLinesLevelDebug,
	}
)

// GetEngineLogs reads the engine's log ring buffer over its control stream.
func (h *handlers) GetEngineLogs(ctx context.Context, req GetEngineLogsRequestObject) (GetEngineLogsResponseObject, error) {
	p := req.Params
	lr := &controlv1.LogRequest{MinLevel: controlv1.LogLevel_LOG_LEVEL_DEBUG, Limit: 1000}
	if p.After != nil {
		if *p.After < 0 {
			return nil, invalid("after must not be negative")
		}
		lr.AfterSeq = uint64(*p.After)
	}
	if p.Level != nil {
		level, ok := logLevelsIn[*p.Level]
		if !ok {
			return nil, invalid("level must be error, warn, info or debug")
		}
		lr.MinLevel = level
	}
	if p.Q != nil {
		if len(*p.Q) > 128 {
			return nil, invalid("q must be at most 128 characters")
		}
		lr.Contains = *p.Q
	}
	if p.Limit != nil {
		if *p.Limit < 1 || *p.Limit > 1000 {
			return nil, invalid("limit must be between 1 and 1000")
		}
		lr.Limit = uint32(*p.Limit)
	}
	if _, err := getEngine(ctx, h.d.Store.Pool, req.Id); err != nil {
		return nil, err
	}
	if h.d.EngineLogs == nil {
		return nil, engineLogsError(ErrEngineUnsupported)
	}
	batch, err := h.d.EngineLogs.Read(ctx, req.Id, lr)
	if errors.Is(err, ErrEngineTimeout) {
		if pre, perr := preM6Engine(ctx, h.d.Store, req.Id); perr != nil {
			return nil, perr
		} else if pre {
			err = ErrEngineUnsupported
		}
	}
	if err != nil {
		return nil, engineLogsError(err)
	}
	out := EngineLogs{EngineId: req.Id, LastSeq: int64(batch.LastSeq), OldestSeq: int64(batch.OldestSeq)}
	out.Lines = makeOf(out.Lines, len(batch.Lines))
	for i, l := range batch.Lines {
		level, ok := logLevelsOut[l.Level]
		if !ok {
			level = EngineLogsLinesLevelInfo
		}
		out.Lines[i].Seq, out.Lines[i].Time, out.Lines[i].Level, out.Lines[i].Message = int64(l.Seq), time.UnixMilli(l.UnixMs).UTC(), level, l.Message
	}
	return GetEngineLogs200JSONResponse(out), nil
}

// preM6Engine reports whether the engine's newest stats sample comes from a build before M6: every
// M6 engine reports started_unix_ms, and only M6 engines answer log requests. Image tags (sha-<7>)
// do not order, so the version string cannot tell. No sample: false (the timeout stands).
func preM6Engine(ctx context.Context, st *store.Store, engineID uuid.UUID) (bool, error) {
	var raw []byte
	err := st.Pool.QueryRow(ctx, "select stats from engine_stats where engine_id = $1 order by at desc limit 1", engineID).Scan(&raw)
	if errors.Is(store.MapError(err), store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, store.MapError(err)
	}
	s := &controlv1.Stats{}
	if proto.Unmarshal(raw, s) != nil {
		return false, nil
	}
	return s.StartedUnixMs == 0, nil
}
