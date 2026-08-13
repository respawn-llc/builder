package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const (
	MinCommittedAtUnixMs int64 = -8_640_000_000_000_000
	MaxCommittedAtUnixMs int64 = 8_640_000_000_000_000
)

// CommittedAtUnixMs is the canonical absolute transcript commit-time value.
type CommittedAtUnixMs int64

func NewCommittedAtUnixMs(value int64) (CommittedAtUnixMs, error) {
	result := CommittedAtUnixMs(value)
	if err := result.Validate(); err != nil {
		return 0, err
	}
	return result, nil
}

func (value CommittedAtUnixMs) UnixMs() int64 {
	return int64(value)
}

func (value CommittedAtUnixMs) Validate() error {
	if int64(value) < MinCommittedAtUnixMs || int64(value) > MaxCommittedAtUnixMs {
		return fmt.Errorf(
			"committed time must be between %d and %d Unix milliseconds, got %d",
			MinCommittedAtUnixMs,
			MaxCommittedAtUnixMs,
			value,
		)
	}
	return nil
}

func ValidateCommittedAtUnixMs(value *CommittedAtUnixMs) error {
	if value == nil {
		return nil
	}
	return value.Validate()
}

func (value CommittedAtUnixMs) MarshalJSON() ([]byte, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(int64(value))
}

func (value *CommittedAtUnixMs) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("committed time must be omitted or an integer")
	}
	var raw int64
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("decode committed time: %w", err)
	}
	*value = CommittedAtUnixMs(raw)
	return nil
}

type committedAtUnixMsField struct {
	value   CommittedAtUnixMs
	present bool
}

func (field *committedAtUnixMsField) UnmarshalJSON(data []byte) error {
	var value CommittedAtUnixMs
	if err := value.UnmarshalJSON(data); err != nil {
		return err
	}
	field.value = value
	field.present = true
	return nil
}

// DecodeCommittedAtUnixMsField applies encoding/json's case-insensitive field
// matching while retaining presence and rejecting null, including when a
// differently-cased duplicate appears anywhere in the object.
func DecodeCommittedAtUnixMsField(data []byte, fieldName string) (*CommittedAtUnixMs, bool, error) {
	if fieldName != "committed_at_unix_ms" {
		return nil, false, fmt.Errorf("unsupported committed-time field %q", fieldName)
	}
	var decoded struct {
		CommittedAt committedAtUnixMsField `json:"committed_at_unix_ms"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, false, err
	}
	if !decoded.CommittedAt.present {
		return nil, false, nil
	}
	value := decoded.CommittedAt.value
	return &value, true, nil
}
