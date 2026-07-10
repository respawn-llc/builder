package analyzer

import (
	"errors"
	"fmt"
)

const maxOperationTextBytes = 1 * 1024 * 1024

var errWriteTextArenaLimit = errors.New("write text arena limit exceeded")

type writeTextArena struct {
	bytes []byte
	limit int
}

func newWriteTextArena(limit int) *writeTextArena {
	return &writeTextArena{
		bytes: make([]byte, 0, min(limit, maxOperationTextBytes)),
		limit: min(limit, maxOperationTextBytes),
	}
}

func newDefaultWriteTextArena() *writeTextArena {
	return newWriteTextArena(maxOperationTextBytes)
}

func (a *writeTextArena) append(text string) (TextSpan, error) {
	if a == nil {
		return TextSpan{}, fmt.Errorf("write text arena is required")
	}
	if text == "" {
		return TextSpan{}, fmt.Errorf("write text must not be empty")
	}
	if len(text) > a.limit-len(a.bytes) {
		return TextSpan{}, fmt.Errorf("%w: observed=%d limit=%d", errWriteTextArenaLimit, len(a.bytes)+len(text), a.limit)
	}
	start := len(a.bytes)
	a.bytes = append(a.bytes, text...)
	return TextSpan{Start: start, End: len(a.bytes)}, nil
}
