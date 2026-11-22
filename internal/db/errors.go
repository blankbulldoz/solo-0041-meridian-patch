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

func unavailable(format string, args ...any) *Error {
	return &Error{Code: "unavailable", Message: fmt.Sprintf(format, args...)}
}
