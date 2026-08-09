package client

import "fmt"

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
