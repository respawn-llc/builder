package analyzer

import (
	"fmt"
)

const maxOperationTextBytes = 1 * 1024 * 1024

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
		payload := []byte(text)
		return TextSpan{}, &EvidenceLimitExceeded{
			Source:   EvidenceSourceOperationText,
			Limit:    a.limit,
			Observed: len(a.bytes) + len(text),
			Prefix:   boundedPrefix(a.bytes, payload),
			Tail:     boundedTail(a.bytes, payload),
		}
	}
	start := len(a.bytes)
	a.bytes = append(a.bytes, text...)
	return TextSpan{Start: start, End: len(a.bytes)}, nil
}

func boundedPrefix(existing []byte, extra []byte) []byte {
	size := min(evidenceExcerptSize, len(existing)+len(extra))
	prefix := make([]byte, 0, size)
	prefix = append(prefix, existing[:min(len(existing), size)]...)
	if len(prefix) < size {
		prefix = append(prefix, extra[:size-len(prefix)]...)
	}
	return prefix
}

func boundedTail(existing []byte, extra []byte) []byte {
	size := min(evidenceExcerptSize, len(existing)+len(extra))
	tail := make([]byte, size)
	fromExtra := min(len(extra), size)
	if fromExtra > 0 {
		copy(tail[size-fromExtra:], extra[len(extra)-fromExtra:])
	}
	remaining := size - fromExtra
	if remaining > 0 {
		copy(tail[:remaining], existing[len(existing)-remaining:])
	}
	return tail
}
