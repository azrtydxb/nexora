package api

import (
	"context"
	"net/http"
)

func (h *handlers) UpdateCurrentUser(ctx context.Context, _ UpdateCurrentUserRequestObject) (UpdateCurrentUserResponseObject, error) {
	return nil, coded(http.StatusNotImplemented, "not_implemented", "not implemented yet")
}

func (h *handlers) ChangeOwnPassword(ctx context.Context, _ ChangeOwnPasswordRequestObject) (ChangeOwnPasswordResponseObject, error) {
	return nil, coded(http.StatusNotImplemented, "not_implemented", "not implemented yet")
}
