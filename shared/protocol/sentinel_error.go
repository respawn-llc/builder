package protocol

import (
	"errors"
	"strings"
)

type SentinelError struct {
	Sentinel error
	Message  string
}

func NewSentinelError(sentinel error, message string) error {
	trimmed := strings.TrimSpace(message)
	if sentinel == nil {
		return errors.New(trimmed)
	}
	if trimmed == "" || trimmed == sentinel.Error() {
		return sentinel
	}
	return SentinelError{Sentinel: sentinel, Message: trimmed}
}

func (e SentinelError) Error() string {
	return e.Message
}

func (e SentinelError) Unwrap() error {
	return e.Sentinel
}
