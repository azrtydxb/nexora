package zone

import (
	"errors"

	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// ErrNotFound and ErrConflict are the store sentinels, wrapped with detail.
var (
	ErrNotFound = store.ErrNotFound
	ErrConflict = store.ErrConflict
)

// ErrReadOnly refuses record changes on secondary zones.
var ErrReadOnly = errors.New("zone is a secondary zone and read-only")

// LineError locates a problem in submitted zone data.
type LineError struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// ValidationError is invalid zone input; Code is the API error code.
type ValidationError struct {
	Code, Message string
	Details       []LineError
}

func (e *ValidationError) Error() string { return e.Code + ": " + e.Message }

func invalid(code, message string) error { return &ValidationError{Code: code, Message: message} }
