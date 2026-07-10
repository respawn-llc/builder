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
		prefix := append([]byte(nil), a.bytes...)
		prefix = append(prefix, text...)
		if len(prefix) > evidenceExcerptSize {
			prefix = prefix[:evidenceExcerptSize]
		}
		tail := append(append([]byte(nil), a.bytes...), text...)
		if len(tail) > evidenceExcerptSize {
			tail = tail[len(tail)-evidenceExcerptSize:]
		}
		return TextSpan{}, &EvidenceLimitExceeded{
			Source:   EvidenceSourceOperationText,
			Limit:    a.limit,
			Observed: len(a.bytes) + len(text),
			Prefix:   prefix,
			Tail:     tail,
		}
	}
	start := len(a.bytes)
	a.bytes = append(a.bytes, text...)
	return TextSpan{Start: start, End: len(a.bytes)}, nil
}
