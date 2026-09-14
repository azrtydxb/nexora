package api

import (
	"context"
	"net/http"
)

func (h *handlers) GetDashboardSeries(ctx context.Context, _ GetDashboardSeriesRequestObject) (GetDashboardSeriesResponseObject, error) {
	return nil, coded(http.StatusNotImplemented, "not_implemented", "not implemented yet")
}

func (h *handlers) GetDashboardTop(ctx context.Context, _ GetDashboardTopRequestObject) (GetDashboardTopResponseObject, error) {
	return nil, coded(http.StatusNotImplemented, "not_implemented", "not implemented yet")
}

func (h *handlers) GetDashboardHealth(ctx context.Context, _ GetDashboardHealthRequestObject) (GetDashboardHealthResponseObject, error) {
	return nil, coded(http.StatusNotImplemented, "not_implemented", "not implemented yet")
}
