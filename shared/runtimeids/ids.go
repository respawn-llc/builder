package runtimeids

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type uuidv4Value struct {
	value uuid.UUID
}

func parseUUIDv4Value(raw string, field string) (uuidv4Value, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return uuidv4Value{}, fmt.Errorf("%s is required", field)
	}
	parsed, err := uuid.Parse(trimmed)
	if err != nil {
		return uuidv4Value{}, fmt.Errorf("%s must be a UUID", field)
	}
	if parsed.Version() != 4 || parsed.Variant() != uuid.RFC4122 {
		return uuidv4Value{}, fmt.Errorf("%s must be a UUIDv4", field)
	}
	return uuidv4Value{value: parsed}, nil
}

func ValidateUUIDv4(raw string, field string) error {
	_, err := parseUUIDv4Value(raw, field)
	return err
}

// ParseCanonicalUUIDv4 accepts the exact UUIDv4 text form used by CLI
// selectors. Unlike internal ID validation, selector input cannot normalize
// whitespace or non-canonical UUID spellings.
func ParseCanonicalUUIDv4(raw string, field string) (uuid.UUID, error) {
	if strings.TrimSpace(raw) == "" {
		return uuid.Nil, fmt.Errorf("%s is required", field)
	}
	if strings.TrimSpace(raw) != raw {
		return uuid.Nil, fmt.Errorf("%s must not have leading or trailing whitespace", field)
	}
	parsed, err := uuid.Parse(raw)
	if err != nil || parsed.Version() != 4 || parsed.Variant() != uuid.RFC4122 || parsed.String() != raw {
		return uuid.Nil, fmt.Errorf("%s must be a UUIDv4", field)
	}
	return parsed, nil
}

func newUUIDv4Value() uuidv4Value {
	return uuidv4Value{value: uuid.New()}
}

func (id uuidv4Value) String() string {
	return id.value.String()
}

func (id uuidv4Value) IsZero() bool {
	return id.value == uuid.Nil
}

func (id uuidv4Value) MarshalText() ([]byte, error) {
	return []byte(id.String()), nil
}

func (id *uuidv4Value) UnmarshalText(raw []byte) error {
	parsed, err := parseUUIDv4Value(string(raw), "id")
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}
