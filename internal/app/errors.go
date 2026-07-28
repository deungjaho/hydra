package app

import (
	"errors"
	"fmt"
)

// ExitCode constants follow §12.6 of the framework spec:
//
//	0 = success
//	2 = usage/argument error
//	3 = configuration error
//	4 = dependency unavailable
//	5 = permission denied
//	1 = other operational error (default)
const (
	ExitSuccess    = 0
	ExitGeneric    = 1
	ExitUsage      = 2
	ExitConfig     = 3
	ExitDependency = 4
	ExitPermission = 5
)

// ErrCode is a stable, machine-readable error code. It is the same
// code used in CLI JSON output, TUI messages, and logs. Adding a new
// code is a backwards-compatible change; removing or renaming one is
// a breaking change that requires a schema version bump.
type ErrCode string

const (
	CodeNotFound        ErrCode = "NOT_FOUND"
	CodeAlreadyExists   ErrCode = "ALREADY_EXISTS"
	CodeInvalidArgument ErrCode = "INVALID_ARGUMENT"
	CodePermission      ErrCode = "PERMISSION_DENIED"
	CodeUnavailable     ErrCode = "UNAVAILABLE"
	CodeInternal        ErrCode = "INTERNAL"
	CodeConfig          ErrCode = "CONFIG"
)

// AppError is the typed error used throughout the application service.
// It carries a stable code, a human-readable message (safe to show),
// and a retryable hint. The underlying cause is retained but not
// serialized by default — callers should not expose it to end users.
type AppError struct {
	Code      ErrCode
	Message   string
	Retryable bool
	Cause     error
	Details   any
}

func (e *AppError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *AppError) Unwrap() error { return e.Cause }

// ExitCode maps an AppError to the CLI exit code per §12.6.
func (e *AppError) ExitCode() int {
	switch e.Code {
	case CodeNotFound, CodeAlreadyExists:
		return ExitGeneric
	case CodeInvalidArgument:
		return ExitUsage
	case CodePermission:
		return ExitPermission
	case CodeUnavailable:
		return ExitDependency
	case CodeConfig:
		return ExitConfig
	default:
		return ExitGeneric
	}
}

// NewError constructs an AppError.
func NewError(code ErrCode, msg string, opts ...ErrorOption) *AppError {
	e := &AppError{Code: code, Message: msg}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

type ErrorOption func(*AppError)

func WithCause(err error) ErrorOption     { return func(e *AppError) { e.Cause = err } }
func WithRetryable(b bool) ErrorOption    { return func(e *AppError) { e.Retryable = b } }
func WithDetails(details any) ErrorOption { return func(e *AppError) { e.Details = details } }

// AsAppError unwraps a generic error into an AppError. If the error is
// already an *AppError, it is returned as-is. Otherwise it is wrapped
// as an internal error.
func AsAppError(err error) *AppError {
	if err == nil {
		return nil
	}
	var ae *AppError
	if errors.As(err, &ae) {
		return ae
	}
	return &AppError{
		Code:    CodeInternal,
		Message: err.Error(),
		Cause:   err,
	}
}

// NotFound returns a NOT_FOUND error for a given entity.
func NotFound(entity string, id any) *AppError {
	return &AppError{
		Code:    CodeNotFound,
		Message: fmt.Sprintf("%s %v was not found", entity, id),
		Details: map[string]any{"entity": entity, "id": id},
	}
}
