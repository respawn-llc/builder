// Package openaiwire owns the small set of provider-ready output items that
// can be materialized from canonical tool results.
package openaiwire

import (
	"encoding/json"
	"errors"
	"fmt"
)

var (
	ErrInvalidCallID = errors.New("openai wire call id is required")
	ErrInvalidOutput = errors.New("openai wire output must be valid json")
)

type ValidationKind string

const (
	ValidationMissingCallID ValidationKind = "missing_call_id"
	ValidationInvalidOutput ValidationKind = "invalid_output"
)

type ValidationError struct {
	Kind ValidationKind
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid OpenAI wire output (%s)", e.Kind)
}

func (e *ValidationError) Unwrap() error {
	switch e.Kind {
	case ValidationMissingCallID:
		return ErrInvalidCallID
	case ValidationInvalidOutput:
		return ErrInvalidOutput
	default:
		return nil
	}
}

// Raw is an immutable provider-ready OpenAI input item. Its only constructors
// intentionally produce tool-output items; message, call, reasoning,
// compaction, and passthrough items have no representation in this package.
type Raw struct {
	bytes []byte
}

func (r Raw) Bytes() json.RawMessage {
	return append(json.RawMessage(nil), r.bytes...)
}
