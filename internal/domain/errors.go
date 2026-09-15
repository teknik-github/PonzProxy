// Package domain holds the core types of ponzproxy and the rules that must
// hold for them regardless of how they are stored or served. It deliberately
// imports nothing from the rest of the module.
package domain

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrNotFound is returned by repositories when a record does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict signals a uniqueness violation, e.g. a domain already
	// routed by another host.
	ErrConflict = errors.New("conflict")
	// ErrInUse signals a record cannot be removed because something still
	// references it.
	ErrInUse = errors.New("in use")
)

// FieldError names the specific input that failed validation so the UI can
// highlight it rather than showing one opaque message.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e FieldError) Error() string { return e.Field + ": " + e.Message }

// ValidationError aggregates every problem found in one payload, so a caller
// fixes all of them in one round trip instead of one per request.
type ValidationError struct {
	Fields []FieldError `json:"fields"`
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		parts = append(parts, f.Error())
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// Add records a failure. Nothing is reported until Err is called.
func (e *ValidationError) Add(field, format string, args ...any) {
	e.Fields = append(e.Fields, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
}

// Err returns the error only when something actually failed, which lets
// validators end with `return v.Err()` unconditionally.
func (e *ValidationError) Err() error {
	if len(e.Fields) == 0 {
		return nil
	}
	return e
}
