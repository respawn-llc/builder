package client

import (
	"errors"
	"fmt"
)

type InvalidResponseError struct {
	Operation string
	Cause     error
}

func (e *InvalidResponseError) Error() string {
	return fmt.Sprintf("validate %s response: %v", e.Operation, e.Cause)
}
func (e *InvalidResponseError) Unwrap() error { return e.Cause }
func invalidResponseError(operation string, cause error) error {
	return &InvalidResponseError{Operation: operation, Cause: cause}
}

type CleanupError struct {
	Cause   error
	Cleanup error
}

func (e *CleanupError) Error() string { return e.Cause.Error() }
func (e *CleanupError) Unwrap() error { return e.Cause }

func WithCleanupError(cause, cleanup error) error {
	if cleanup == nil {
		return cause
	}
	return &CleanupError{Cause: cause, Cleanup: cleanup}
}

func CleanupErrorOf(err error) error {
	var cleanup *CleanupError
	if errors.As(err, &cleanup) {
		return cleanup.Cleanup
	}
	return nil
}
