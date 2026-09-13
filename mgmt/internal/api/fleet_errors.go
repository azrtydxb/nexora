package api

import "fmt"

// apiError is a request refused with a specific status and error code.
type apiError struct {
	status    int
	code, msg string
}

func (e apiError) Error() string { return e.msg }

func coded(status int, code, format string, args ...any) error {
	return apiError{status: status, code: code, msg: fmt.Sprintf(format, args...)}
}
