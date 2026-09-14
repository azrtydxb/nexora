package api

import (
	"context"
	"net/http"
)

func (h *handlers) GetEngineMetrics(ctx context.Context, _ GetEngineMetricsRequestObject) (GetEngineMetricsResponseObject, error) {
	return nil, coded(http.StatusNotImplemented, "not_implemented", "not implemented yet")
}
