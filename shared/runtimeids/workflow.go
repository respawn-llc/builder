package runtimeids

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// WorkflowID is the canonical UUIDv4 identity of a Workflow.
type WorkflowID struct {
	uuidv4Value
}

// ParseWorkflowID accepts only the exact lower-case canonical UUIDv4 text form.
func ParseWorkflowID(raw string) (WorkflowID, error) {
	parsed, err := ParseCanonicalUUIDv4(raw, "workflow_id")
	if err != nil {
		return WorkflowID{}, err
	}
	return WorkflowID{uuidv4Value: uuidv4Value{value: parsed}}, nil
}

// NewWorkflowID creates a new canonical UUIDv4 Workflow identity.
func NewWorkflowID() WorkflowID {
	return WorkflowID{uuidv4Value: newUUIDv4Value()}
}

func (id *WorkflowID) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed, err := ParseWorkflowID(raw)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func (id WorkflowID) MarshalText() ([]byte, error) {
	if id.IsZero() {
		return nil, fmt.Errorf("workflow_id is required")
	}
	return []byte(id.String()), nil
}

func (id *WorkflowID) UnmarshalText(text []byte) error {
	parsed, err := ParseWorkflowID(string(text))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

// Value stores WorkflowID as its RFC 4122 16-byte UUID representation.
func (id WorkflowID) Value() (driver.Value, error) {
	if id.IsZero() {
		return nil, fmt.Errorf("workflow_id is required")
	}
	value := make([]byte, len(id.value))
	copy(value, id.value[:])
	return value, nil
}

// Scan reads only a 16-byte UUIDv4 BLOB from SQLite.
func (id *WorkflowID) Scan(src any) error {
	if id == nil {
		return fmt.Errorf("workflow_id destination is nil")
	}
	raw, ok := src.([]byte)
	if !ok {
		return fmt.Errorf("workflow_id must be a 16-byte UUIDv4 BLOB")
	}
	if len(raw) != 16 {
		return fmt.Errorf("workflow_id must be a 16-byte UUIDv4 BLOB")
	}
	parsed, err := uuid.FromBytes(raw)
	if err != nil || parsed.Version() != 4 || parsed.Variant() != uuid.RFC4122 || parsed == uuid.Nil {
		return fmt.Errorf("workflow_id must be a non-zero UUIDv4 BLOB")
	}
	*id = WorkflowID{uuidv4Value: uuidv4Value{value: parsed}}
	return nil
}
