package api

import (
	"context"
	"net/http"
)

func (h *handlers) GetVersion(ctx context.Context, _ GetVersionRequestObject) (GetVersionResponseObject, error) {
	return nil, coded(http.StatusNotImplemented, "not_implemented", "not implemented yet")
}
