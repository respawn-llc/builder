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
	if parsed.Version() != 4 {
		return uuidv4Value{}, fmt.Errorf("%s must be a UUIDv4", field)
	}
	return uuidv4Value{value: parsed}, nil
}

func ValidateUUIDv4(raw string, field string) error {
	_, err := parseUUIDv4Value(raw, field)
	return err
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
