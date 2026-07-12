package clientui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type WorktreeTransitionID uuid.UUID

func NewWorktreeTransitionID() WorktreeTransitionID {
	return WorktreeTransitionID(uuid.New())
}

func ParseWorktreeTransitionID(value string) (WorktreeTransitionID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return WorktreeTransitionID{}, fmt.Errorf("operation_id must be a UUID v4: %w", err)
	}
	id := WorktreeTransitionID(parsed)
	if err := id.Validate(); err != nil {
		return WorktreeTransitionID{}, err
	}
	return id, nil
}

func (id WorktreeTransitionID) String() string {
	value := uuid.UUID(id)
	if value == uuid.Nil {
		return ""
	}
	return value.String()
}

func (id WorktreeTransitionID) Validate() error {
	value := uuid.UUID(id)
	if value == uuid.Nil {
		return errors.New("operation_id is required")
	}
	if value.Version() != 4 {
		return errors.New("operation_id must be a UUID v4")
	}
	return nil
}

func (id WorktreeTransitionID) MarshalJSON() ([]byte, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(id.String())
}

func (id *WorktreeTransitionID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	parsed, err := ParseWorktreeTransitionID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

type WorktreeTransitionKind string

const (
	WorktreeTransitionEnter  WorktreeTransitionKind = "enter"
	WorktreeTransitionLeave  WorktreeTransitionKind = "leave"
	WorktreeTransitionDelete WorktreeTransitionKind = "delete"
)

type WorktreeTransitionState string

const (
	WorktreeTransitionCompleted WorktreeTransitionState = "completed"
	WorktreeTransitionFailed    WorktreeTransitionState = "failed"
)

type WorktreeTransitionFailure struct {
	Diagnostic string
}

type WorktreeTransitionOutcome struct {
	OperationID WorktreeTransitionID
	Transition  WorktreeTransitionKind
	State       WorktreeTransitionState
	Failure     *WorktreeTransitionFailure
}

func (outcome WorktreeTransitionOutcome) Validate() error {
	if err := outcome.OperationID.Validate(); err != nil {
		return err
	}
	switch outcome.Transition {
	case WorktreeTransitionEnter, WorktreeTransitionLeave, WorktreeTransitionDelete:
	default:
		return errors.New("worktree transition kind is invalid")
	}
	switch outcome.State {
	case WorktreeTransitionCompleted:
		if outcome.Failure != nil {
			return errors.New("completed worktree transition cannot contain failure facts")
		}
	case WorktreeTransitionFailed:
		if outcome.Failure == nil {
			return errors.New("failed worktree transition requires failure facts")
		}
		if strings.TrimSpace(outcome.Failure.Diagnostic) == "" {
			return errors.New("worktree transition failure diagnostic is required")
		}
	default:
		return errors.New("worktree transition state is invalid")
	}
	return nil
}
