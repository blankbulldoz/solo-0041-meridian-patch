package db

import "fmt"

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func invalid(format string, args ...any) *Error {
	return &Error{Code: "invalid", Message: fmt.Sprintf(format, args...)}
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

// sessionGone reports a review session token that is unknown, expired, or
// already closed, so callers always get one explicit terminal result.
func sessionGone() *Error {
	return &Error{Code: "session_gone", Message: "review session is unknown, expired, or closed"}
}
