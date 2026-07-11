package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

// TaskExecutionTargetNegotiationPreparation is the immutable task/workflow
// context an initiating action needs before it can negotiate or materialize an
// execution target. It deliberately contains no mutable action result.
type TaskExecutionTargetNegotiationPreparation struct {
	TaskID              workflow.TaskID
	ProjectID           string
	WorkflowID          workflow.WorkflowID
	SourceWorkspace     workflow.ExecutionWorkspace
	ExecutionPolicy     workflow.ExecutionPolicy
	ExistingTarget      *workflow.ExecutionTarget
	ExistingNegotiation *workflow.ExecutionTargetNegotiation
	Action              workflow.ExecutionTargetNegotiationAction
}

func (s *Store) PrepareTaskStartExecutionTargetNegotiation(ctx context.Context, taskID workflow.TaskID) (TaskExecutionTargetNegotiationPreparation, error) {
	prepared, err := s.prepareTaskStart(ctx, taskID)
	if err != nil {
		return TaskExecutionTargetNegotiationPreparation{}, err
	}
	startPlacementID := workflow.PlacementID(prepared.startPlacement.ID)
	return s.taskExecutionTargetNegotiationPreparation(ctx, prepared.task, prepared.workflow, workflow.ExecutionTargetNegotiationAction{
		Kind:             workflow.ExecutionTargetNegotiationActionStart,
		StartPlacementID: &startPlacementID,
	})
}

func (s *Store) PrepareManualMoveExecutionTargetNegotiation(ctx context.Context, req ManualMoveRequest) (TaskExecutionTargetNegotiationPreparation, bool, error) {
	prepared, err := s.prepareManualMove(ctx, req)
	if err != nil {
		return TaskExecutionTargetNegotiationPreparation{}, false, err
	}
	if !executableNodeKind(prepared.targetNode.Kind()) {
		return TaskExecutionTargetNegotiationPreparation{}, false, nil
	}
	sourcePlacementID := prepared.sourcePlacement
	targetNodeID := workflow.NodeIDOf(prepared.targetNode)
	preparation, err := s.taskExecutionTargetNegotiationPreparation(ctx, prepared.task, prepared.workflow, workflow.ExecutionTargetNegotiationAction{
		Kind:                  workflow.ExecutionTargetNegotiationActionManualMove,
		MoveSourcePlacementID: &sourcePlacementID,
		MoveTargetNodeID:      &targetNodeID,
	})
	if err != nil {
		return TaskExecutionTargetNegotiationPreparation{}, false, err
	}
	return preparation, true, nil
}

func (s *Store) PrepareApprovalExecutionTargetNegotiation(ctx context.Context, transitionID workflow.TransitionID) (TaskExecutionTargetNegotiationPreparation, bool, error) {
	id := strings.TrimSpace(string(transitionID))
	if id == "" {
		return TaskExecutionTargetNegotiationPreparation{}, false, ErrTransitionIDRequired
	}
	transition, err := s.queries.GetTransitionApprovalState(ctx, id)
	if err != nil {
		return TaskExecutionTargetNegotiationPreparation{}, false, err
	}
	if transition.State == "approved" || transition.State == "applied" {
		return TaskExecutionTargetNegotiationPreparation{}, false, nil
	}
	if transition.State != "pending_approval" {
		return TaskExecutionTargetNegotiationPreparation{}, false, fmt.Errorf("transition %s is not pending approval", id)
	}
	edges, err := s.queries.ListTaskTransitionEdges(ctx, id)
	if err != nil {
		return TaskExecutionTargetNegotiationPreparation{}, false, err
	}
	if len(edges) == 0 {
		return TaskExecutionTargetNegotiationPreparation{}, false, errors.New("pending approval has no edge snapshots")
	}
	requiresExecutionTarget := false
	for _, edge := range edges {
		if edge.State == "pending" && executableNodeKind(workflow.NodeKind(edge.TargetNodeKind)) {
			requiresExecutionTarget = true
			break
		}
	}
	if !requiresExecutionTarget {
		return TaskExecutionTargetNegotiationPreparation{}, false, nil
	}
	task, err := s.queries.GetTask(ctx, transition.TaskID)
	if err != nil {
		return TaskExecutionTargetNegotiationPreparation{}, false, err
	}
	_, workflowRecord, err := s.GetDefinition(ctx, workflow.WorkflowID(task.WorkflowID))
	if err != nil {
		return TaskExecutionTargetNegotiationPreparation{}, false, err
	}
	approvalID := workflow.TransitionID(id)
	preparation, err := s.taskExecutionTargetNegotiationPreparation(ctx, task, workflowRecord, workflow.ExecutionTargetNegotiationAction{
		Kind:                 workflow.ExecutionTargetNegotiationActionApproval,
		ApprovalTransitionID: &approvalID,
	})
	if err != nil {
		return TaskExecutionTargetNegotiationPreparation{}, false, err
	}
	return preparation, true, nil
}

func (s *Store) taskExecutionTargetNegotiationPreparation(ctx context.Context, task sqlitegen.TaskRecord, definition WorkflowRecord, action workflow.ExecutionTargetNegotiationAction) (TaskExecutionTargetNegotiationPreparation, error) {
	if err := action.Validate(); err != nil {
		return TaskExecutionTargetNegotiationPreparation{}, err
	}
	workspaceID := strings.TrimSpace(task.SourceWorkspaceID.String)
	if workspaceID == "" {
		return TaskExecutionTargetNegotiationPreparation{}, errors.New("task source workspace is required for execution target negotiation")
	}
	workspace, err := s.metadata.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		return TaskExecutionTargetNegotiationPreparation{}, err
	}
	target, err := s.GetTaskExecutionTarget(ctx, workflow.TaskID(task.ID))
	if err != nil {
		return TaskExecutionTargetNegotiationPreparation{}, err
	}
	negotiation, err := s.GetTaskExecutionTargetNegotiation(ctx, workflow.TaskID(task.ID))
	if err != nil {
		return TaskExecutionTargetNegotiationPreparation{}, err
	}
	return TaskExecutionTargetNegotiationPreparation{
		TaskID:              workflow.TaskID(task.ID),
		ProjectID:           task.ProjectID,
		WorkflowID:          workflow.WorkflowID(task.WorkflowID),
		SourceWorkspace:     workflow.ExecutionWorkspace{ID: workspace.ID, Root: workspace.CanonicalRootPath},
		ExecutionPolicy:     definition.ExecutionPolicy,
		ExistingTarget:      target,
		ExistingNegotiation: negotiation,
		Action:              action,
	}, nil
}

func (s *Store) SaveTaskExecutionTargetNegotiation(ctx context.Context, negotiation workflow.ExecutionTargetNegotiation) error {
	if err := negotiation.Validate(); err != nil {
		return err
	}
	return s.queries.UpsertTaskExecutionTargetNegotiation(ctx, sqlitegen.UpsertTaskExecutionTargetNegotiationParams{
		TaskID:                string(negotiation.TaskID),
		Generation:            negotiation.Generation,
		WorkflowID:            string(negotiation.WorkflowID),
		SourceWorkspaceID:     negotiation.SourceWorkspaceID,
		SourceKind:            string(negotiation.Source.Kind),
		SourceNamedRef:        nullableExecutionTargetNegotiationString(negotiation.Source.NamedRef),
		SourceCommit:          nullableExecutionTargetNegotiationString(negotiation.Source.Commit),
		RecoveryCause:         nullableExecutionTargetNegotiationRecoveryCause(negotiation.RecoveryCause),
		ActionKind:            string(negotiation.Action.Kind),
		StartPlacementID:      nullableExecutionTargetNegotiationPlacementID(negotiation.Action.StartPlacementID),
		MoveSourcePlacementID: nullableExecutionTargetNegotiationPlacementID(negotiation.Action.MoveSourcePlacementID),
		MoveTargetNodeID:      nullableExecutionTargetNegotiationNodeID(negotiation.Action.MoveTargetNodeID),
		ApprovalTransitionID:  nullableExecutionTargetNegotiationTransitionID(negotiation.Action.ApprovalTransitionID),
	})
}

func (s *Store) GetTaskExecutionTargetNegotiation(ctx context.Context, taskID workflow.TaskID) (*workflow.ExecutionTargetNegotiation, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return nil, errors.New("task id is required")
	}
	row, err := s.queries.GetTaskExecutionTargetNegotiation(ctx, string(taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get task execution target negotiation: %w", err)
	}
	negotiation, err := taskExecutionTargetNegotiationFromRow(row)
	if err != nil {
		return nil, fmt.Errorf("decode task execution target negotiation: %w", err)
	}
	return &negotiation, nil
}

func (s *Store) ValidateTaskExecutionTargetNegotiation(ctx context.Context, expected workflow.ExecutionTargetNegotiation) error {
	if err := expected.Validate(); err != nil {
		return err
	}
	actual, err := s.GetTaskExecutionTargetNegotiation(ctx, expected.TaskID)
	if err != nil {
		return err
	}
	if actual == nil {
		return ErrTaskExecutionTargetNegotiationRequired
	}
	if !executionTargetNegotiationsEqual(*actual, expected) {
		return ErrTaskExecutionTargetNegotiationChanged
	}
	return nil
}

func (s *Store) ClearTaskExecutionTargetNegotiation(ctx context.Context, taskID workflow.TaskID) error {
	if strings.TrimSpace(string(taskID)) == "" {
		return errors.New("task id is required")
	}
	_, err := s.queries.DeleteTaskExecutionTargetNegotiation(ctx, string(taskID))
	return err
}

func (s *Store) clearNoneExecutionTargetNegotiationAfterValidationFailure(ctx context.Context, taskID workflow.TaskID, validationErr error) error {
	if clearErr := s.ClearTaskExecutionTargetNegotiation(ctx, taskID); clearErr != nil {
		return errors.Join(validationErr, clearErr)
	}
	return validationErr
}

func ensureTaskExecutionTargetNegotiationIsNotActive(ctx context.Context, q *sqlitegen.Queries, taskID workflow.TaskID) error {
	_, err := q.GetTaskExecutionTargetNegotiation(ctx, string(taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get task execution target negotiation: %w", err)
	}
	return ErrTaskExecutionTargetNegotiationInProgress
}

func taskExecutionTargetNegotiationFromRow(row sqlitegen.TaskExecutionTargetNegotiation) (workflow.ExecutionTargetNegotiation, error) {
	namedRef := executionTargetOptionalString(row.SourceNamedRef)
	commit := executionTargetOptionalString(row.SourceCommit)
	recoveryCause := executionTargetOptionalString(row.RecoveryCause)
	var typedCause *workflow.ExecutionTargetRecoveryCause
	if recoveryCause != nil {
		cause := workflow.ExecutionTargetRecoveryCause(*recoveryCause)
		typedCause = &cause
	}
	startPlacementID := executionTargetNegotiationPlacementID(row.StartPlacementID)
	moveSourcePlacementID := executionTargetNegotiationPlacementID(row.MoveSourcePlacementID)
	moveTargetNodeID := executionTargetNegotiationNodeID(row.MoveTargetNodeID)
	approvalTransitionID := executionTargetNegotiationTransitionID(row.ApprovalTransitionID)
	negotiation := workflow.ExecutionTargetNegotiation{
		TaskID:            workflow.TaskID(row.TaskID),
		Generation:        row.Generation,
		WorkflowID:        workflow.WorkflowID(row.WorkflowID),
		SourceWorkspaceID: row.SourceWorkspaceID,
		Source: workflow.ExecutionTargetNegotiationSource{
			Kind:     workflow.ExecutionTargetNegotiationSourceKind(row.SourceKind),
			NamedRef: namedRef,
			Commit:   commit,
		},
		RecoveryCause: typedCause,
		Action: workflow.ExecutionTargetNegotiationAction{
			Kind:                  workflow.ExecutionTargetNegotiationActionKind(row.ActionKind),
			StartPlacementID:      startPlacementID,
			MoveSourcePlacementID: moveSourcePlacementID,
			MoveTargetNodeID:      moveTargetNodeID,
			ApprovalTransitionID:  approvalTransitionID,
		},
	}
	if err := negotiation.Validate(); err != nil {
		return workflow.ExecutionTargetNegotiation{}, err
	}
	return negotiation, nil
}

func executionTargetNegotiationsEqual(left workflow.ExecutionTargetNegotiation, right workflow.ExecutionTargetNegotiation) bool {
	return left.TaskID == right.TaskID &&
		left.Generation == right.Generation &&
		left.WorkflowID == right.WorkflowID &&
		left.SourceWorkspaceID == right.SourceWorkspaceID &&
		left.Source.Kind == right.Source.Kind &&
		optionalExecutionTargetNegotiationValueEqual(left.Source.NamedRef, right.Source.NamedRef) &&
		optionalExecutionTargetNegotiationValueEqual(left.Source.Commit, right.Source.Commit) &&
		optionalExecutionTargetNegotiationValueEqual(left.RecoveryCause, right.RecoveryCause) &&
		left.Action.Kind == right.Action.Kind &&
		optionalExecutionTargetNegotiationValueEqual(left.Action.StartPlacementID, right.Action.StartPlacementID) &&
		optionalExecutionTargetNegotiationValueEqual(left.Action.MoveSourcePlacementID, right.Action.MoveSourcePlacementID) &&
		optionalExecutionTargetNegotiationValueEqual(left.Action.MoveTargetNodeID, right.Action.MoveTargetNodeID) &&
		optionalExecutionTargetNegotiationValueEqual(left.Action.ApprovalTransitionID, right.Action.ApprovalTransitionID)
}

func nullableExecutionTargetNegotiationString(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func nullableExecutionTargetNegotiationRecoveryCause(value *workflow.ExecutionTargetRecoveryCause) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(*value), Valid: true}
}

func nullableExecutionTargetNegotiationPlacementID(value *workflow.PlacementID) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(*value), Valid: true}
}

func nullableExecutionTargetNegotiationNodeID(value *workflow.NodeID) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(*value), Valid: true}
}

func nullableExecutionTargetNegotiationTransitionID(value *workflow.TransitionID) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(*value), Valid: true}
}

func executionTargetNegotiationPlacementID(value sql.NullString) *workflow.PlacementID {
	if !value.Valid {
		return nil
	}
	id := workflow.PlacementID(value.String)
	return &id
}

func executionTargetNegotiationNodeID(value sql.NullString) *workflow.NodeID {
	if !value.Valid {
		return nil
	}
	id := workflow.NodeID(value.String)
	return &id
}

func executionTargetNegotiationTransitionID(value sql.NullString) *workflow.TransitionID {
	if !value.Valid {
		return nil
	}
	id := workflow.TransitionID(value.String)
	return &id
}

func optionalExecutionTargetNegotiationValueEqual[T comparable](left *T, right *T) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}
