package runtimeids

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

type SessionID struct {
	value           string
	canonicalUUIDv4 bool
}

func ParseSessionID(raw string) (SessionID, error) {
	if raw == "" {
		return SessionID{}, fmt.Errorf("session_id is required")
	}
	if strings.TrimSpace(raw) != raw {
		return SessionID{}, fmt.Errorf("session_id must not have leading or trailing whitespace")
	}
	if parsed, err := uuid.Parse(raw); err == nil {
		if parsed.String() != raw || parsed.Version() != 4 || parsed.Variant() != uuid.RFC4122 {
			return SessionID{}, fmt.Errorf("session_id must be a canonical UUIDv4")
		}
		return SessionID{value: raw, canonicalUUIDv4: true}, nil
	}
	if filepath.IsAbs(raw) || raw == "." || raw == ".." ||
		strings.Contains(raw, "/") || strings.Contains(raw, `\`) ||
		filepath.Clean(raw) != raw {
		return SessionID{}, fmt.Errorf("session_id must be a path-safe persisted identifier")
	}
	return SessionID{value: raw}, nil
}

func NewSessionID() SessionID {
	return SessionID{value: uuid.NewString(), canonicalUUIDv4: true}
}

func (id SessionID) String() string {
	return id.value
}

func (id SessionID) IsZero() bool {
	return id.value == ""
}

func (id SessionID) IsCanonicalUUIDv4() bool {
	return id.canonicalUUIDv4
}

func (id SessionID) MarshalJSON() ([]byte, error) {
	if id.IsZero() {
		return nil, fmt.Errorf("session_id is required")
	}
	return json.Marshal(id.String())
}

func (id *SessionID) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed, err := ParseSessionID(raw)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}
