package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
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
	decoded, err := NewCommittedAtUnixMs(raw)
	if err != nil {
		return err
	}
	*value = decoded
	return nil
}

// DecodeCommittedAtUnixMsField applies encoding/json's case-insensitive field
// matching while retaining presence and rejecting null, including when a
// differently-cased duplicate appears anywhere in the object.
func DecodeCommittedAtUnixMsField(data []byte, fieldName string) (*CommittedAtUnixMs, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, false, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, false, fmt.Errorf("committed-time owner must be a JSON object")
	}
	var value *CommittedAtUnixMs
	present := false
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, false, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, false, fmt.Errorf("committed-time field name must be a string")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, false, err
		}
		if !strings.EqualFold(key, fieldName) {
			continue
		}
		var decoded CommittedAtUnixMs
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, false, err
		}
		copyValue := decoded
		value = &copyValue
		present = true
	}
	if _, err := decoder.Token(); err != nil && err != io.EOF {
		return nil, false, err
	}
	return value, present, nil
}
