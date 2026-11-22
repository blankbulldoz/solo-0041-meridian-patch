package db

import "fmt"

type Error struct {
	Code    string
	Message string
	cause   error
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.cause }

func invalid(format string, args ...any) *Error {
	return &Error{Code: "invalid", Message: fmt.Sprintf(format, args...)}
}

// invalidCause keeps an underlying typed failure available to errors.As while
// presenting the same invalid-request classification.
func invalidCause(cause error, format string, args ...any) *Error {
	return &Error{Code: "invalid", Message: fmt.Sprintf(format, args...), cause: cause}
}

func conflict(format string, args ...any) *Error {
	return &Error{Code: "conflict", Message: fmt.Sprintf(format, args...)}
}

func missing(format string, args ...any) *Error {
	return &Error{Code: "missing", Message: fmt.Sprintf(format, args...)}
}

func exhausted(format string, args ...any) *Error {
	return &Error{Code: "capacity", Message: fmt.Sprintf(format, args...)}
}
