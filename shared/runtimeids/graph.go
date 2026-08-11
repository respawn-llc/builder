package runtimeids

import (
	"fmt"

	"github.com/google/uuid"
)

// NewGraphEntityID creates canonical UUIDv4 text for a persistent Workflow
// graph entity.
func NewGraphEntityID() string {
	return newUUIDv4Value().String()
}

// GraphEntityIDBlob converts canonical UUIDv4 text to its 16-byte SQLite
// representation.
func GraphEntityIDBlob(raw string) ([]byte, error) {
	parsed, err := ParseCanonicalUUIDv4(raw, "graph entity ID")
	if err != nil {
		return nil, err
	}
	value := make([]byte, len(parsed))
	copy(value, parsed[:])
	return value, nil
}

// GraphEntityIDText converts a 16-byte SQLite UUIDv4 value to canonical text.
func GraphEntityIDText(raw []byte) (string, error) {
	if len(raw) != 16 {
		return "", fmt.Errorf("graph entity ID must be a 16-byte UUIDv4 BLOB")
	}
	parsed, err := uuid.FromBytes(raw)
	if err != nil || parsed == uuid.Nil || parsed.Version() != 4 || parsed.Variant() != uuid.RFC4122 {
		return "", fmt.Errorf("graph entity ID must be a non-zero UUIDv4 BLOB")
	}
	return parsed.String(), nil
}
