package serverapi

import (
	"errors"
	"fmt"
	"strings"
)

type WorkflowTaskMovePreviewOutcome string

const (
	WorkflowTaskMovePreviewOutcomeNoOp       WorkflowTaskMovePreviewOutcome = "no_op"
	WorkflowTaskMovePreviewOutcomeDirect     WorkflowTaskMovePreviewOutcome = "direct"
	WorkflowTaskMovePreviewOutcomeTransition WorkflowTaskMovePreviewOutcome = "transition"
	WorkflowTaskMovePreviewOutcomeBlocked    WorkflowTaskMovePreviewOutcome = "blocked"
)

type WorkflowTaskMovePreviewBlocker string

const (
	WorkflowTaskMovePreviewBlockerInvalidWorkflow              WorkflowTaskMovePreviewBlocker = "invalid_workflow"
	WorkflowTaskMovePreviewBlockerNoSourcePosition             WorkflowTaskMovePreviewBlocker = "no_source_position"
	WorkflowTaskMovePreviewBlockerUnsupportedDestination       WorkflowTaskMovePreviewBlocker = "unsupported_destination"
	WorkflowTaskMovePreviewBlockerWaitingQuestion              WorkflowTaskMovePreviewBlocker = "waiting_question"
	WorkflowTaskMovePreviewBlockerLifecycleConflict            WorkflowTaskMovePreviewBlocker = "lifecycle_conflict"
	WorkflowTaskMovePreviewBlockerContextSessionUnavailable    WorkflowTaskMovePreviewBlocker = "context_session_unavailable"
	WorkflowTaskMovePreviewBlockerNoUsableTransition           WorkflowTaskMovePreviewBlocker = "no_usable_transition"
	WorkflowTaskMovePreviewBlockerParallelBranchRequiresFanOut WorkflowTaskMovePreviewBlocker = "parallel_branch_requires_fan_out"
)

type WorkflowTaskMovePreviewRequest struct {
	TaskID       string `json:"task_id"`
	TargetNodeID string `json:"target_node_id"`
}

type WorkflowTaskMovePreviewResponse struct {
	Outcome    WorkflowTaskMovePreviewOutcome     `json:"outcome,omitempty"`
	NoOp       *WorkflowTaskMovePreviewNoOp       `json:"no_op,omitempty"`
	Direct     *WorkflowTaskMovePreviewDirect     `json:"direct,omitempty"`
	Transition *WorkflowTaskMovePreviewTransition `json:"transition,omitempty"`
	Blocked    *WorkflowTaskMovePreviewBlocked    `json:"blocked,omitempty"`
}

type WorkflowTaskMovePreviewNoOp struct {
	CurrentNodes []WorkflowTaskCurrentNode `json:"current_nodes"`
}

type WorkflowTaskMovePreviewDirect struct{}

type WorkflowTaskMovePreviewTransition struct {
	Choices []WorkflowTaskMovePreviewTransitionChoice `json:"choices"`
}

type WorkflowTaskMovePreviewTransitionChoice struct {
	TransitionKey         string                          `json:"transition_key"`
	Label                 string                          `json:"label"`
	SourceNodeDisplayName string                          `json:"source_node_display_name"`
	RequiredValues        []WorkflowTaskMoveRequiredValue `json:"required_values"`
}

type WorkflowTaskMoveRequiredValue struct {
	NodeKey       string  `json:"node_key"`
	OutputName    string  `json:"output_name"`
	Description   string  `json:"description"`
	ResolvedValue *string `json:"resolved_value,omitempty"`
}

type WorkflowTaskMovePreviewBlocked struct {
	Reason WorkflowTaskMovePreviewBlocker `json:"reason"`
}

func (r WorkflowTaskMovePreviewRequest) Validate() error {
	return validateRequiredFields(
		requiredField("task_id", r.TaskID),
		requiredField("target_node_id", r.TargetNodeID),
	)
}

func (r WorkflowTaskMovePreviewResponse) Validate() error {
	payloads := 0
	if r.NoOp != nil {
		payloads++
	}
	if r.Direct != nil {
		payloads++
	}
	if r.Transition != nil {
		payloads++
	}
	if r.Blocked != nil {
		payloads++
	}
	if payloads != 1 {
		return errors.New("manual move preview response requires exactly one outcome payload")
	}

	switch r.Outcome {
	case WorkflowTaskMovePreviewOutcomeNoOp:
		if r.NoOp == nil || r.Direct != nil || r.Transition != nil || r.Blocked != nil {
			return errors.New("manual move preview no_op outcome requires only no_op payload")
		}
		return validateWorkflowTaskMovePreviewNoOp(*r.NoOp)
	case WorkflowTaskMovePreviewOutcomeDirect:
		if r.Direct == nil || r.NoOp != nil || r.Transition != nil || r.Blocked != nil {
			return errors.New("manual move preview direct outcome requires only direct payload")
		}
		return nil
	case WorkflowTaskMovePreviewOutcomeTransition:
		if r.Transition == nil || r.NoOp != nil || r.Direct != nil || r.Blocked != nil {
			return errors.New("manual move preview transition outcome requires only transition payload")
		}
		return validateWorkflowTaskMovePreviewTransition(*r.Transition)
	case WorkflowTaskMovePreviewOutcomeBlocked:
		if r.Blocked == nil || r.NoOp != nil || r.Direct != nil || r.Transition != nil {
			return errors.New("manual move preview blocked outcome requires only blocked payload")
		}
		return r.Blocked.Validate()
	default:
		return errors.New("manual move preview outcome is invalid")
	}
}

func (b WorkflowTaskMovePreviewBlocked) Validate() error {
	switch b.Reason {
	case WorkflowTaskMovePreviewBlockerInvalidWorkflow,
		WorkflowTaskMovePreviewBlockerNoSourcePosition,
		WorkflowTaskMovePreviewBlockerUnsupportedDestination,
		WorkflowTaskMovePreviewBlockerWaitingQuestion,
		WorkflowTaskMovePreviewBlockerLifecycleConflict,
		WorkflowTaskMovePreviewBlockerContextSessionUnavailable,
		WorkflowTaskMovePreviewBlockerNoUsableTransition,
		WorkflowTaskMovePreviewBlockerParallelBranchRequiresFanOut:
		return nil
	default:
		return errors.New("manual move preview blocker reason is invalid")
	}
}

func validateWorkflowTaskMovePreviewNoOp(noOp WorkflowTaskMovePreviewNoOp) error {
	return validateWorkflowTaskCurrentNodes(noOp.CurrentNodes, "manual move preview no_op")
}

func validateWorkflowTaskMovePreviewTransition(transition WorkflowTaskMovePreviewTransition) error {
	if len(transition.Choices) == 0 {
		return errors.New("manual move preview transition requires choices")
	}
	for index, choice := range transition.Choices {
		if err := choice.Validate(); err != nil {
			return fmt.Errorf("manual move preview transition choice %d: %w", index, err)
		}
	}
	return nil
}

func (c WorkflowTaskMovePreviewTransitionChoice) Validate() error {
	for field, value := range map[string]string{
		"transition_key":           c.TransitionKey,
		"label":                    c.Label,
		"source_node_display_name": c.SourceNodeDisplayName,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", field)
		}
	}
	if c.RequiredValues == nil {
		return errors.New("required_values must be present")
	}
	for index, value := range c.RequiredValues {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("required_values[%d]: %w", index, err)
		}
	}
	return nil
}

func (v WorkflowTaskMoveRequiredValue) Validate() error {
	for field, value := range map[string]string{
		"node_key":    v.NodeKey,
		"output_name": v.OutputName,
		"description": v.Description,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", field)
		}
	}
	if v.ResolvedValue != nil && strings.TrimSpace(*v.ResolvedValue) == "" {
		return errors.New("resolved_value must be non-blank when present")
	}
	return nil
}
