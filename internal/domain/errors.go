package domain

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors returned by the domain and repository layers. The transport
// layer maps them to gRPC status codes, so nothing below it imports grpc.
var (
	// ErrNotFound means the requested entity does not exist.
	ErrNotFound = errors.New("entity not found")
	// ErrAlreadyExists means a uniqueness constraint would be violated.
	ErrAlreadyExists = errors.New("entity already exists")
	// ErrInvalidArgument means the caller supplied malformed input.
	ErrInvalidArgument = errors.New("invalid argument")
)

// FieldViolation describes a single rejected field.
type FieldViolation struct {
	Field       string
	Description string
}

// ValidationError aggregates every field violation found in one request so the
// caller can fix all of them at once instead of one round trip per field.
type ValidationError struct {
	Violations []FieldViolation
}

// Error implements error.
func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Violations))
	for _, v := range e.Violations {
		parts = append(parts, fmt.Sprintf("%s: %s", v.Field, v.Description))
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// Unwrap lets errors.Is(err, ErrInvalidArgument) match any validation error.
func (e *ValidationError) Unwrap() error { return ErrInvalidArgument }

// newValidationError returns nil when there is nothing to report, which keeps
// call sites free of empty-slice checks.
func newValidationError(violations []FieldViolation) error {
	if len(violations) == 0 {
		return nil
	}
	return &ValidationError{Violations: violations}
}
