package worktreecontract

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type OperationID uuid.UUID

func NewOperationID() OperationID {
	return OperationID(uuid.New())
}

func ParseOperationID(value string) (OperationID, error) {
	parsed, err := parseUUIDv4(value, "operation_id")
	if err != nil {
		return OperationID{}, err
	}
	return OperationID(parsed), nil
}

func (id OperationID) String() string {
	value := uuid.UUID(id)
	if value == uuid.Nil {
		return ""
	}
	return value.String()
}

func (id OperationID) Validate() error {
	return validateUUIDv4(uuid.UUID(id), "operation_id")
}

type SetupOperationID uuid.UUID

func NewSetupOperationID() SetupOperationID {
	return SetupOperationID(uuid.New())
}

func ParseSetupOperationID(value string) (SetupOperationID, error) {
	parsed, err := parseUUIDv4(value, "setup_operation_id")
	if err != nil {
		return SetupOperationID{}, err
	}
	return SetupOperationID(parsed), nil
}

func (id SetupOperationID) String() string {
	value := uuid.UUID(id)
	if value == uuid.Nil {
		return ""
	}
	return value.String()
}

func (id SetupOperationID) Validate() error {
	return validateUUIDv4(uuid.UUID(id), "setup_operation_id")
}

func parseUUIDv4(value string, field string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return uuid.Nil, fmt.Errorf("%s must be a UUID v4: %w", field, err)
	}
	if err := validateUUIDv4(parsed, field); err != nil {
		return uuid.Nil, err
	}
	return parsed, nil
}

func validateUUIDv4(value uuid.UUID, field string) error {
	if value == uuid.Nil {
		return errors.New(field + " is required")
	}
	if value.Version() != 4 {
		return fmt.Errorf("%s must be a UUID v4", field)
	}
	return nil
}
