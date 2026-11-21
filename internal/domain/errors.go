package domain

import (
	"errors"
	"fmt"
)

// Sentinel errors are part of the domain contract. Delivery adapters map them
// to transport-specific codes (HTTP status, gRPC codes, ...) via errors.Is.
var (
	ErrNotFound            = errors.New("resource not found")
	ErrAlreadyExists       = errors.New("resource already exists")
	ErrConflict            = errors.New("concurrent modification conflict")
	ErrInvalidTransition   = errors.New("invalid payment status transition")
	ErrValidation          = errors.New("validation failed")
	ErrUnauthorized        = errors.New("unauthorized")
	ErrExpired             = errors.New("resource expired")
	ErrInsufficientBalance = errors.New("insufficient refundable balance")
)

// ValidationError carries the offending field so API responses can point the
// client at the exact problem.
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// Unwrap lets errors.Is(err, ErrValidation) succeed.
func (e *ValidationError) Unwrap() error { return ErrValidation }

func NewValidationError(field, message string) error {
	return &ValidationError{Field: field, Message: message}
}

// IsValidation reports whether err originated from input validation.
func IsValidation(err error) bool { return errors.Is(err, ErrValidation) }
