package serverapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"core/shared/protocol"
)

var ErrSessionRetarget = errors.New("session workspace retarget failed")

type SessionRetargetErrorReason string

const (
	SessionRetargetTargetProjectRequired SessionRetargetErrorReason = "target_project_required"
	SessionRetargetTargetProjectConflict SessionRetargetErrorReason = "target_project_conflict"
	SessionRetargetWorkflowOwned         SessionRetargetErrorReason = "workflow_owned"
	SessionRetargetBackgroundProcess     SessionRetargetErrorReason = "background_process_active"
)

type ProjectReference struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SessionRetargetError struct {
	Reason            SessionRetargetErrorReason `json:"reason"`
	SessionID         string                     `json:"session_id"`
	SourceProject     ProjectReference           `json:"source_project"`
	TargetRoot        string                     `json:"target_root"`
	CandidateProjects []ProjectReference         `json:"candidate_projects,omitempty"`
	WorkflowTaskIDs   []string                   `json:"workflow_task_ids,omitempty"`
}

func (e *SessionRetargetError) Error() string {
	if e == nil {
		return ErrSessionRetarget.Error()
	}
	return fmt.Sprintf("%s: %s", ErrSessionRetarget, e.Reason)
}

func (e *SessionRetargetError) Is(target error) bool {
	return target == ErrSessionRetarget
}

func (e *SessionRetargetError) Validate() error {
	if e == nil {
		return errors.New("session retarget error is required")
	}
	if strings.TrimSpace(e.SessionID) == "" {
		return errors.New("session id is required")
	}
	if err := e.SourceProject.Validate(); err != nil {
		return fmt.Errorf("source project: %w", err)
	}
	if strings.TrimSpace(e.TargetRoot) == "" {
		return errors.New("target root is required")
	}
	switch e.Reason {
	case SessionRetargetTargetProjectRequired,
		SessionRetargetTargetProjectConflict:
		if len(e.CandidateProjects) == 0 {
			return errors.New("candidate projects are required")
		}
		for _, project := range e.CandidateProjects {
			if err := project.Validate(); err != nil {
				return fmt.Errorf("candidate project: %w", err)
			}
		}
	case SessionRetargetWorkflowOwned,
		SessionRetargetBackgroundProcess:
	default:
		return fmt.Errorf("invalid session retarget reason %q", e.Reason)
	}
	return nil
}

func (p ProjectReference) Validate() error {
	if strings.TrimSpace(p.ID) == "" {
		return errors.New("project id is required")
	}
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("project name is required")
	}
	return nil
}

func (e *SessionRetargetError) SortedCandidateProjects() []ProjectReference {
	if e == nil {
		return nil
	}
	sorted := append([]ProjectReference(nil), e.CandidateProjects...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].ID < sorted[j].ID
	})
	return sorted
}

func (e *SessionRetargetError) RPCErrorCode() int {
	return protocol.ErrCodeSessionRetarget
}

func (e *SessionRetargetError) RPCErrorData() json.RawMessage {
	if e == nil || e.Validate() != nil {
		return nil
	}
	normalized := *e
	normalized.CandidateProjects = e.SortedCandidateProjects()
	return marshalRPCErrorData(struct {
		Type string `json:"type"`
		SessionRetargetError
	}{
		Type:                 "session_retarget_error",
		SessionRetargetError: normalized,
	})
}

func DecodeSessionRetargetError(data json.RawMessage, message string) error {
	var envelope struct {
		Type string `json:"type"`
		SessionRetargetError
	}
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Type != "session_retarget_error" || envelope.SessionRetargetError.Validate() != nil {
		trimmed := strings.TrimSpace(message)
		if trimmed == "" {
			trimmed = ErrSessionRetarget.Error()
		}
		return errors.New(trimmed)
	}
	return &envelope.SessionRetargetError
}
