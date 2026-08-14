package runtimeids

import (
	"fmt"
	"strings"

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

// MigrateGraphEntityIDBlob preserves canonical UUIDv4 identities, remaps
// nonblank legacy identities, and rejects absent or zero identities.
func MigrateGraphEntityIDBlob(raw string) ([]byte, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("graph entity ID migration value must not be blank")
	}
	if parsed, err := uuid.Parse(raw); err == nil && parsed == uuid.Nil {
		return nil, fmt.Errorf("graph entity ID migration value must not be zero")
	}
	if value, err := GraphEntityIDBlob(raw); err == nil {
		return value, nil
	}
	return GraphEntityIDBlob(NewGraphEntityID())
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
