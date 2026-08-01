package protocol

import (
	"errors"
	"strings"
)

type SentinelError struct {
	Sentinel error
	Message  string
}

type SentinelErrorRendering uint8

const (
	SentinelErrorMessageOnly SentinelErrorRendering = iota
	SentinelErrorJoined
)

func NewSentinelError(sentinel error, message string) error {
	return NewSentinelErrorWithRendering(sentinel, message, SentinelErrorMessageOnly)
}

func NewSentinelErrorWithRendering(sentinel error, message string, rendering SentinelErrorRendering) error {
	trimmed := strings.TrimSpace(message)
	if sentinel == nil {
		if trimmed == "" {
			return errors.New("sentinel error requires a sentinel or a message")
		}
		return errors.New(trimmed)
	}
	if trimmed == "" || trimmed == sentinel.Error() {
		return sentinel
	}
	switch rendering {
	case SentinelErrorMessageOnly:
		return SentinelError{Sentinel: sentinel, Message: trimmed}
	case SentinelErrorJoined:
		return errors.Join(sentinel, errors.New(trimmed))
	default:
		panic("unsupported sentinel error rendering")
	}
}

func (e SentinelError) Error() string {
	return e.Message
}

func (e SentinelError) Unwrap() error {
	return e.Sentinel
}
