package serverapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type WorktreeSetupOperationID uuid.UUID

func NewWorktreeSetupOperationID() WorktreeSetupOperationID {
	return WorktreeSetupOperationID(uuid.New())
}

func ParseWorktreeSetupOperationID(value string) (WorktreeSetupOperationID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return WorktreeSetupOperationID{}, fmt.Errorf("setup_operation_id must be a UUID v4: %w", err)
	}
	id := WorktreeSetupOperationID(parsed)
	if err := id.Validate(); err != nil {
		return WorktreeSetupOperationID{}, err
	}
	return id, nil
}

func (id WorktreeSetupOperationID) String() string {
	value := uuid.UUID(id)
	if value == uuid.Nil {
		return ""
	}
	return value.String()
}

func (id WorktreeSetupOperationID) Validate() error {
	value := uuid.UUID(id)
	if value == uuid.Nil {
		return errors.New("setup_operation_id is required")
	}
	if value.Version() != 4 {
		return errors.New("setup_operation_id must be a UUID v4")
	}
	return nil
}

func (id WorktreeSetupOperationID) MarshalJSON() ([]byte, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(id.String())
}

func (id *WorktreeSetupOperationID) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed, err := ParseWorktreeSetupOperationID(raw)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

type WorktreeSetupPhase string

const (
	WorktreeSetupPhaseStarted   WorktreeSetupPhase = "started"
	WorktreeSetupPhaseCompleted WorktreeSetupPhase = "completed"
	WorktreeSetupPhaseFailed    WorktreeSetupPhase = "failed"
)

type WorktreeSetupEvent struct {
	SetupOperationID    WorktreeSetupOperationID `json:"setup_operation_id"`
	SourceWorkspaceRoot string                   `json:"source_workspace_root"`
	WorktreeRoot        string                   `json:"worktree_root"`
	ScriptPath          string                   `json:"script_path"`
	Phase               WorktreeSetupPhase       `json:"phase"`
	Timeout             bool                     `json:"timeout,omitempty"`
	Canceled            bool                     `json:"canceled,omitempty"`
	ExitCode            *int                     `json:"exit_code,omitempty"`
	Stdout              string                   `json:"stdout,omitempty"`
	Stderr              string                   `json:"stderr,omitempty"`
	Error               string                   `json:"error,omitempty"`
}

func (e WorktreeSetupEvent) Validate() error {
	if err := e.SetupOperationID.Validate(); err != nil {
		return err
	}
	switch e.Phase {
	case WorktreeSetupPhaseStarted:
		if strings.TrimSpace(e.SourceWorkspaceRoot) == "" || strings.TrimSpace(e.WorktreeRoot) == "" || strings.TrimSpace(e.ScriptPath) == "" {
			return errors.New("started setup event requires source_workspace_root, worktree_root, and script_path")
		}
		if e.Timeout || e.Canceled || e.ExitCode != nil || strings.TrimSpace(e.Error) != "" {
			return errors.New("started setup event cannot contain terminal facts")
		}
	case WorktreeSetupPhaseCompleted:
		if strings.TrimSpace(e.WorktreeRoot) == "" || strings.TrimSpace(e.ScriptPath) == "" {
			return errors.New("completed setup event requires worktree_root and script_path")
		}
		if e.Timeout || e.Canceled || e.ExitCode != nil || strings.TrimSpace(e.Error) != "" || strings.TrimSpace(e.Stdout) != "" || strings.TrimSpace(e.Stderr) != "" {
			return errors.New("completed setup event cannot contain failure facts")
		}
	case WorktreeSetupPhaseFailed:
		if strings.TrimSpace(e.WorktreeRoot) == "" || strings.TrimSpace(e.ScriptPath) == "" {
			return errors.New("failed setup event requires worktree_root and script_path")
		}
		if strings.TrimSpace(e.Error) == "" && strings.TrimSpace(e.Stdout) == "" && strings.TrimSpace(e.Stderr) == "" && e.ExitCode == nil && !e.Timeout && !e.Canceled {
			return errors.New("failed setup event requires failure facts")
		}
	default:
		return errors.New("setup phase is invalid")
	}
	return nil
}

type WorktreeSetupSubscribeRequest struct {
	SetupOperationID WorktreeSetupOperationID `json:"setup_operation_id"`
}

func (r WorktreeSetupSubscribeRequest) Validate() error {
	return r.SetupOperationID.Validate()
}

type WorktreeSetupSubscription interface {
	Next(context.Context) (WorktreeSetupEvent, error)
	Close() error
}
