package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
)

// EngineLogReader reads an engine's log ring buffer through whichever instance holds its stream.
type EngineLogReader interface {
	Read(ctx context.Context, engineID uuid.UUID, req *controlv1.LogRequest) (*controlv1.LogBatch, error)
}

var (
	ErrEngineDisconnected = errors.New("engine is not connected")
	ErrEngineTimeout      = errors.New("engine did not answer in time")
	ErrEngineUnsupported  = errors.New("engine version does not serve logs")
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

func (h *handlers) GetEngineLogs(ctx context.Context, _ GetEngineLogsRequestObject) (GetEngineLogsResponseObject, error) {
	if h.d.EngineLogs == nil {
		return nil, engineLogsError(ErrEngineUnsupported)
	}
	return nil, coded(http.StatusNotImplemented, "not_implemented", "not implemented yet")
}
