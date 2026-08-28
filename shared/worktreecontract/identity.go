package worktreecontract

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type OperationID uuid.UUID

func NewOperationID() OperationID { return OperationID(uuid.New()) }

func (id OperationID) String() string { return uuidV4String(uuid.UUID(id)) }

func (id OperationID) Validate() error { return validateUUIDv4(uuid.UUID(id), "operation_id") }

type SetupOperationID uuid.UUID

func NewSetupOperationID() SetupOperationID { return SetupOperationID(uuid.New()) }

func ParseSetupOperationID(value string) (SetupOperationID, error) {
	parsed, err := parseUUIDv4(value, "setup_operation_id")
	if err != nil {
		return SetupOperationID{}, err
	}
	return SetupOperationID(parsed), nil
}

func (id SetupOperationID) String() string { return uuidV4String(uuid.UUID(id)) }

func (id SetupOperationID) Validate() error {
	return validateUUIDv4(uuid.UUID(id), "setup_operation_id")
}

func parseUUIDv4(value string, field string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return uuid.Nil, fmt.Errorf("%s must be a UUID v4: %w", field, err)
	}
	return parsed, validateUUIDv4(parsed, field)
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

func uuidV4String(value uuid.UUID) string {
	if value == uuid.Nil {
		return ""
	}
	return value.String()
}
