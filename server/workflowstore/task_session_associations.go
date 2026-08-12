package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"core/server/metadata/sqlitegen"
	"core/server/metadata/sqlitelifecyclegen"
	"core/server/workflow"
	"core/shared/runtimeids"
)

type TaskSessionAssociationRequest struct {
	SessionID    runtimeids.SessionID
	CurrentNode  workflow.CurrentNodeReference
	AssociatedAt time.Time
}

// CurrentNodeSessionBindingRequest compare-and-swaps the exact Current Node's
// retained Session while recording the resulting Task Session association.
type CurrentNodeSessionBindingRequest struct {
	Association              TaskSessionAssociationRequest
	ExpectedCurrentSessionID *runtimeids.SessionID
}

type TaskSessionAssociation struct {
	SessionID       runtimeids.SessionID
	SourceSessionID runtimeids.SessionID
	CurrentNode     workflow.CurrentNodeReference
	AssociatedAt    time.Time
}

// ErrSessionNotCurrentWorkflowNode means that a retained Task-owned Session is
// not the Session of a currently executable workflow node. It is an ordinary
// retained-session state, not a data-integrity failure.
var ErrSessionNotCurrentWorkflowNode = errors.New("session is not bound to a current workflow node")

type CurrentNodeSessionBindingAuthorityKind string

const (
	CurrentNodeSessionBindingAuthorityExactCurrent     CurrentNodeSessionBindingAuthorityKind = "exact_current"
	CurrentNodeSessionBindingAuthorityLegacyHistorical CurrentNodeSessionBindingAuthorityKind = "legacy_historical"
)

type CurrentNodeSessionBindingAuthority struct {
	kind        CurrentNodeSessionBindingAuthorityKind
	association *TaskSessionAssociation
}

func (a CurrentNodeSessionBindingAuthority) Kind() CurrentNodeSessionBindingAuthorityKind {
	return a.kind
}

func (a CurrentNodeSessionBindingAuthority) CurrentAssociation() (TaskSessionAssociation, bool) {
	if a.kind != CurrentNodeSessionBindingAuthorityExactCurrent || a.association == nil {
		return TaskSessionAssociation{}, false
	}
	return *a.association, true
}

// BindSessionToCurrentNode atomically establishes the live Agent Session
// binding and the exact current retained-Session tuple for one Current Node.
func (s *Store) BindSessionToCurrentNode(ctx context.Context, req CurrentNodeSessionBindingRequest) (CurrentNodeSessionBindingAuthority, error) {
	normalized, err := normalizeTaskSessionAssociationRequest(req.Association)
	if err != nil {
		return CurrentNodeSessionBindingAuthority{}, err
	}
	if req.ExpectedCurrentSessionID != nil && req.ExpectedCurrentSessionID.IsZero() {
		return CurrentNodeSessionBindingAuthority{}, errors.New("expected current Session id is invalid")
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return CurrentNodeSessionBindingAuthority{}, err
	}
	defer func() { _ = connection.Close() }()
	lifecycle := sqlitelifecyclegen.New(connection)
	if err := lifecycle.SetBusyTimeout15Seconds(ctx); err != nil {
		return CurrentNodeSessionBindingAuthority{}, err
	}
	defer func() { _ = lifecycle.SetBusyTimeout5Seconds(context.Background()) }()
	if err := lifecycle.BeginImmediate(ctx); err != nil {
		return CurrentNodeSessionBindingAuthority{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = lifecycle.Rollback(context.Background())
		}
	}()
	commit := func() error {
		if err := lifecycle.Commit(ctx); err != nil {
			return err
		}
		committed = true
		return nil
	}
	q := sqlitegen.New(connection)
	currentNode, err := s.currentNodeForReference(ctx, q, normalized.CurrentNode)
	if err != nil {
		return CurrentNodeSessionBindingAuthority{}, err
	}
	if currentNode.ContinuationSource.Kind() == workflow.MaterializedContinuationSourceLegacy {
		return bindLegacySessionToCurrentNode(
			ctx,
			q,
			currentNode,
			normalized,
			req.ExpectedCurrentSessionID,
			commit,
		)
	}
	previousAssociation, hasPreviousAssociation, err := currentTaskSessionAssociationBeforeBinding(ctx, q, normalized.CurrentNode)
	if err != nil {
		return CurrentNodeSessionBindingAuthority{}, err
	}
	sourceSessionID, err := bindingSourceSessionID(currentNode, normalized.SessionID)
	if err != nil {
		return CurrentNodeSessionBindingAuthority{}, err
	}
	if err := bindSessionToTask(ctx, q, normalized); err != nil {
		return CurrentNodeSessionBindingAuthority{}, err
	}
	if err := bindSessionToExactCurrentNode(ctx, q, normalized, sourceSessionID, req.ExpectedCurrentSessionID); err != nil {
		return CurrentNodeSessionBindingAuthority{}, err
	}
	if currentNode.ContinuationSource.Kind() == workflow.MaterializedContinuationSourceDeferredSelf {
		if err := resolveDeferredSelfFanoutBranchSource(ctx, q, normalized.CurrentNode, sourceSessionID); err != nil {
			return CurrentNodeSessionBindingAuthority{}, err
		}
	}
	if hasPreviousAssociation && previousAssociation.SessionID != normalized.SessionID {
		if err := retireDependentCurrentTaskSessionAssociations(
			ctx,
			q,
			normalized.CurrentNode,
			previousAssociation.SessionID,
			sourceSessionID,
		); err != nil {
			return CurrentNodeSessionBindingAuthority{}, err
		}
	}
	if err := designateCurrentTaskSessionAssociation(ctx, q, normalized, sourceSessionID); err != nil {
		return CurrentNodeSessionBindingAuthority{}, err
	}
	if err := commit(); err != nil {
		return CurrentNodeSessionBindingAuthority{}, err
	}
	association := taskSessionAssociationFromRequest(normalized, sourceSessionID)
	return CurrentNodeSessionBindingAuthority{
		kind:        CurrentNodeSessionBindingAuthorityExactCurrent,
		association: &association,
	}, nil
}

func bindLegacySessionToCurrentNode(
	ctx context.Context,
	q *sqlitegen.Queries,
	currentNode workflow.CurrentNode,
	req TaskSessionAssociationRequest,
	expectedCurrentSessionID *runtimeids.SessionID,
	commit func() error,
) (CurrentNodeSessionBindingAuthority, error) {
	if err := bindSessionToTask(ctx, q, req); err != nil {
		return CurrentNodeSessionBindingAuthority{}, err
	}
	if err := bindSessionToLegacyCurrentNode(ctx, q, req, expectedCurrentSessionID); err != nil {
		return CurrentNodeSessionBindingAuthority{}, err
	}
	if err := appendLegacyTaskSessionHistory(ctx, q, req); err != nil {
		return CurrentNodeSessionBindingAuthority{}, err
	}
	if err := commit(); err != nil {
		return CurrentNodeSessionBindingAuthority{}, err
	}
	return CurrentNodeSessionBindingAuthority{
		kind: CurrentNodeSessionBindingAuthorityLegacyHistorical,
	}, nil
}

func bindSessionToLegacyCurrentNode(
	ctx context.Context,
	q *sqlitegen.Queries,
	req TaskSessionAssociationRequest,
	expectedCurrentSessionIDValue *runtimeids.SessionID,
) error {
	var expectedCurrentSessionID sql.NullString
	if expectedCurrentSessionIDValue != nil {
		expectedCurrentSessionID = sql.NullString{
			String: expectedCurrentSessionIDValue.String(),
			Valid:  true,
		}
	}
	var (
		bound int64
		err   error
	)
	if branchKey, branchScoped := req.CurrentNode.TransitionBranchKey(); branchScoped {
		bound, err = q.BindLegacySessionToBranchCurrentNode(ctx, sqlitegen.BindLegacySessionToBranchCurrentNodeParams{
			SessionID:                sql.NullString{String: req.SessionID.String(), Valid: true},
			TaskID:                   string(req.CurrentNode.TaskID),
			NodeID:                   string(req.CurrentNode.NodeID),
			TransitionBranchKey:      sql.NullString{String: string(branchKey), Valid: true},
			ExpectedCurrentSessionID: expectedCurrentSessionID,
		})
	} else {
		bound, err = q.BindLegacySessionToSerialCurrentNode(ctx, sqlitegen.BindLegacySessionToSerialCurrentNodeParams{
			SessionID:                sql.NullString{String: req.SessionID.String(), Valid: true},
			TaskID:                   string(req.CurrentNode.TaskID),
			NodeID:                   string(req.CurrentNode.NodeID),
			ExpectedCurrentSessionID: expectedCurrentSessionID,
		})
	}
	if err != nil {
		return err
	}
	if bound != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func appendLegacyTaskSessionHistory(
	ctx context.Context,
	q *sqlitegen.Queries,
	req TaskSessionAssociationRequest,
) error {
	var (
		appended int64
		err      error
	)
	if branchKey, branchScoped := req.CurrentNode.TransitionBranchKey(); branchScoped {
		appended, err = q.AppendLegacyBranchSessionWorkflowNodeHistory(
			ctx,
			sqlitegen.AppendLegacyBranchSessionWorkflowNodeHistoryParams{
				TaskID:              string(req.CurrentNode.TaskID),
				SessionID:           req.SessionID.String(),
				NodeID:              string(req.CurrentNode.NodeID),
				TransitionBranchKey: sql.NullString{String: string(branchKey), Valid: true},
				AssociatedAtUnixMs:  req.AssociatedAt.UnixMilli(),
			},
		)
	} else {
		appended, err = q.AppendLegacySerialSessionWorkflowNodeHistory(
			ctx,
			sqlitegen.AppendLegacySerialSessionWorkflowNodeHistoryParams{
				TaskID:             string(req.CurrentNode.TaskID),
				SessionID:          req.SessionID.String(),
				NodeID:             string(req.CurrentNode.NodeID),
				AssociatedAtUnixMs: req.AssociatedAt.UnixMilli(),
			},
		)
	}
	if err != nil {
		return err
	}
	if appended != 1 {
		return errors.New("legacy Session history conflicts with current association authority")
	}
	return nil
}

func resolveDeferredSelfFanoutBranchSource(
	ctx context.Context,
	q *sqlitegen.Queries,
	currentNode workflow.CurrentNodeReference,
	sourceSessionID runtimeids.SessionID,
) error {
	branchKey, branchScoped := currentNode.TransitionBranchKey()
	if !branchScoped {
		return nil
	}
	updated, err := q.ResolveDeferredSelfTaskActiveFanoutBranchSource(
		ctx,
		sqlitegen.ResolveDeferredSelfTaskActiveFanoutBranchSourceParams{
			SourceSessionID:     sql.NullString{String: sourceSessionID.String(), Valid: true},
			TaskID:              string(currentNode.TaskID),
			TransitionBranchKey: string(branchKey),
		},
	)
	if err != nil {
		return err
	}
	if updated != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func currentTaskSessionAssociationBeforeBinding(
	ctx context.Context,
	q *sqlitegen.Queries,
	currentNode workflow.CurrentNodeReference,
) (TaskSessionAssociation, bool, error) {
	association, err := currentTaskSessionForNode(sqlitegen.WithExpectedNoRows(ctx), q, currentNode)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskSessionAssociation{}, false, nil
	}
	if err != nil {
		return TaskSessionAssociation{}, false, err
	}
	return association, true, nil
}

func retireDependentCurrentTaskSessionAssociations(
	ctx context.Context,
	q *sqlitegen.Queries,
	currentNode workflow.CurrentNodeReference,
	obsoleteSessionID runtimeids.SessionID,
	preservedSourceSessionID runtimeids.SessionID,
) error {
	var transitionBranchKey sql.NullString
	if branchKey, branchScoped := currentNode.TransitionBranchKey(); branchScoped {
		transitionBranchKey = sql.NullString{String: string(branchKey), Valid: true}
	}
	return q.RetireDependentCurrentSessionWorkflowNodeAssociations(
		ctx,
		sqlitegen.RetireDependentCurrentSessionWorkflowNodeAssociationsParams{
			TaskID:                   string(currentNode.TaskID),
			ObsoleteSessionID:        obsoleteSessionID.String(),
			PreservedSourceSessionID: preservedSourceSessionID.String(),
			TransitionBranchKey:      transitionBranchKey,
		},
	)
}

func bindingSourceSessionID(currentNode workflow.CurrentNode, boundSessionID runtimeids.SessionID) (runtimeids.SessionID, error) {
	switch currentNode.ContinuationSource.Kind() {
	case workflow.MaterializedContinuationSourceExact:
		sourceSessionID, ok := currentNode.ContinuationSource.ExactSessionID()
		if !ok {
			return runtimeids.SessionID{}, errors.New("exact Current Node continuation source omitted Session ID")
		}
		return sourceSessionID, nil
	case workflow.MaterializedContinuationSourceDeferredSelf:
		return boundSessionID, nil
	case workflow.MaterializedContinuationSourceLegacy:
		return runtimeids.SessionID{}, errors.New("legacy Current Node binding is not implemented")
	case workflow.MaterializedContinuationSourceAbsent:
		return runtimeids.SessionID{}, errors.New("Current Node has no continuation source")
	default:
		return runtimeids.SessionID{}, fmt.Errorf("Current Node continuation source kind %q is invalid", currentNode.ContinuationSource.Kind())
	}
}

func bindSessionToTask(ctx context.Context, q *sqlitegen.Queries, req TaskSessionAssociationRequest) error {
	bound, err := q.BindSessionToTask(ctx, sqlitegen.BindSessionToTaskParams{
		TaskID:    sql.NullString{String: string(req.CurrentNode.TaskID), Valid: true},
		SessionID: req.SessionID.String(),
	})
	if err != nil {
		return err
	}
	if bound != 1 {
		return errors.New("session cannot be bound to task")
	}
	return nil
}

func bindSessionToExactCurrentNode(
	ctx context.Context,
	q *sqlitegen.Queries,
	req TaskSessionAssociationRequest,
	sourceSessionID runtimeids.SessionID,
	expectedCurrentSessionIDValue *runtimeids.SessionID,
) error {
	var (
		expectedCurrentSessionID sql.NullString
		bound                    int64
		err                      error
	)
	if expectedCurrentSessionIDValue != nil {
		expectedCurrentSessionID = sql.NullString{
			String: expectedCurrentSessionIDValue.String(),
			Valid:  true,
		}
	}
	if branchKey, branchScoped := req.CurrentNode.TransitionBranchKey(); branchScoped {
		bound, err = q.BindSessionToBranchCurrentNode(ctx, sqlitegen.BindSessionToBranchCurrentNodeParams{
			SessionID:                sql.NullString{String: req.SessionID.String(), Valid: true},
			SourceSessionID:          sql.NullString{String: sourceSessionID.String(), Valid: true},
			TaskID:                   string(req.CurrentNode.TaskID),
			NodeID:                   string(req.CurrentNode.NodeID),
			TransitionBranchKey:      sql.NullString{String: string(branchKey), Valid: true},
			ExpectedCurrentSessionID: expectedCurrentSessionID,
		})
	} else {
		bound, err = q.BindSessionToSerialCurrentNode(ctx, sqlitegen.BindSessionToSerialCurrentNodeParams{
			SessionID:                sql.NullString{String: req.SessionID.String(), Valid: true},
			SourceSessionID:          sql.NullString{String: sourceSessionID.String(), Valid: true},
			TaskID:                   string(req.CurrentNode.TaskID),
			NodeID:                   string(req.CurrentNode.NodeID),
			ExpectedCurrentSessionID: expectedCurrentSessionID,
		})
	}
	if err != nil {
		return err
	}
	if bound != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func designateCurrentTaskSessionAssociation(
	ctx context.Context,
	q *sqlitegen.Queries,
	req TaskSessionAssociationRequest,
	sourceSessionID runtimeids.SessionID,
) error {
	if branchKey, branchScoped := req.CurrentNode.TransitionBranchKey(); branchScoped {
		params := sqlitegen.RetireBranchCurrentSessionWorkflowNodeAssociationParams{
			TaskID:              string(req.CurrentNode.TaskID),
			NodeID:              string(req.CurrentNode.NodeID),
			TransitionBranchKey: sql.NullString{String: string(branchKey), Valid: true},
			SessionID:           req.SessionID.String(),
		}
		if err := q.RetireBranchCurrentSessionWorkflowNodeAssociation(ctx, params); err != nil {
			return err
		}
		return q.DesignateBranchCurrentSessionWorkflowNodeAssociation(ctx, sqlitegen.DesignateBranchCurrentSessionWorkflowNodeAssociationParams{
			TaskID:              string(req.CurrentNode.TaskID),
			SessionID:           req.SessionID.String(),
			NodeID:              string(req.CurrentNode.NodeID),
			TransitionBranchKey: sql.NullString{String: string(branchKey), Valid: true},
			SourceSessionID:     sql.NullString{String: sourceSessionID.String(), Valid: true},
			AssociatedAtUnixMs:  req.AssociatedAt.UnixMilli(),
		})
	}
	if err := q.RetireSerialCurrentSessionWorkflowNodeAssociation(ctx, sqlitegen.RetireSerialCurrentSessionWorkflowNodeAssociationParams{
		TaskID:    string(req.CurrentNode.TaskID),
		NodeID:    string(req.CurrentNode.NodeID),
		SessionID: req.SessionID.String(),
	}); err != nil {
		return err
	}
	return q.DesignateSerialCurrentSessionWorkflowNodeAssociation(ctx, sqlitegen.DesignateSerialCurrentSessionWorkflowNodeAssociationParams{
		TaskID:             string(req.CurrentNode.TaskID),
		SessionID:          req.SessionID.String(),
		NodeID:             string(req.CurrentNode.NodeID),
		SourceSessionID:    sql.NullString{String: sourceSessionID.String(), Valid: true},
		AssociatedAtUnixMs: req.AssociatedAt.UnixMilli(),
	})
}

func taskSessionAssociationFromRequest(req TaskSessionAssociationRequest, sourceSessionID runtimeids.SessionID) TaskSessionAssociation {
	return TaskSessionAssociation{
		SessionID:       req.SessionID,
		SourceSessionID: sourceSessionID,
		CurrentNode:     req.CurrentNode,
		AssociatedAt:    req.AssociatedAt,
	}
}

func (s *Store) CountTaskSessions(ctx context.Context, taskID workflow.TaskID) (int64, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return 0, errors.New("task id is required")
	}
	return s.queries.CountTaskSessions(ctx, sql.NullString{String: string(taskID), Valid: true})
}

func (s *Store) TaskIDForSession(ctx context.Context, sessionID runtimeids.SessionID) (*workflow.TaskID, error) {
	if sessionID.IsZero() {
		return nil, errors.New("session id is required")
	}
	if _, err := s.metadata.ResolvePersistedSession(ctx, sessionID.String()); err != nil {
		return nil, fmt.Errorf("resolve persisted Session %q: %w", sessionID, err)
	}
	return s.taskIDForSessionOwnership(ctx, sessionID)
}

func (s *Store) taskIDForSessionOwnership(ctx context.Context, sessionID runtimeids.SessionID) (*workflow.TaskID, error) {
	rawTaskID, err := s.metadata.WorkflowTaskIDForSession(ctx, sessionID.String())
	if err != nil {
		return nil, err
	}
	if rawTaskID == nil {
		return nil, nil
	}
	taskID := workflow.TaskID(*rawTaskID)
	return &taskID, nil
}

func (s *Store) CurrentTaskSessionForNode(ctx context.Context, currentNode workflow.CurrentNodeReference) (TaskSessionAssociation, error) {
	return currentTaskSessionForNode(ctx, s.queries, currentNode)
}

func (s *Store) HasHistoricalTaskSessionForNode(ctx context.Context, currentNode workflow.CurrentNodeReference) (bool, error) {
	return hasHistoricalTaskSessionForNode(ctx, s.queries, currentNode)
}

func hasHistoricalTaskSessionForNode(
	ctx context.Context,
	q *sqlitegen.Queries,
	currentNode workflow.CurrentNodeReference,
) (bool, error) {
	if err := currentNode.Validate(); err != nil {
		return false, err
	}
	if branchKey, branchScoped := currentNode.TransitionBranchKey(); branchScoped {
		return q.HasHistoricalBranchTaskSessionAssociationForNode(
			ctx,
			sqlitegen.HasHistoricalBranchTaskSessionAssociationForNodeParams{
				TaskID:              string(currentNode.TaskID),
				NodeID:              string(currentNode.NodeID),
				TransitionBranchKey: sql.NullString{String: string(branchKey), Valid: true},
			},
		)
	}
	return q.HasHistoricalSerialTaskSessionAssociationForNode(
		ctx,
		sqlitegen.HasHistoricalSerialTaskSessionAssociationForNodeParams{
			TaskID: string(currentNode.TaskID),
			NodeID: string(currentNode.NodeID),
		},
	)
}

// LoadSessionReuseAssociations resolves the bounded Current Node references
// selected by Workflow execution through explicit current associations.
// Missing references are omitted because context sources can
// distinguish an absent retained Session from a selected one.
func (s *Store) LoadSessionReuseAssociations(
	ctx context.Context,
	references []workflow.CurrentNodeReference,
) ([]workflow.SessionReuseAssociation, error) {
	associations := make([]workflow.SessionReuseAssociation, 0, len(references))
	seen := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(references))
	lookupCtx := sqlitegen.WithExpectedNoRows(ctx)
	for _, reference := range references {
		key, err := reference.Key()
		if err != nil {
			return nil, err
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		association, err := s.CurrentTaskSessionForNode(lookupCtx, reference)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		associations = append(associations, workflow.SessionReuseAssociation{
			SessionID:       association.SessionID,
			SourceSessionID: association.SourceSessionID,
			CurrentNode:     association.CurrentNode,
		})
	}
	return associations, nil
}

// ResolveCurrentSessionStartContext resolves prompt state only from direct
// Session ownership, the matching Current Node, and the latest definition.
// Discarded execution history is not an input.
func (s *Store) ResolveCurrentSessionStartContext(ctx context.Context, sessionID runtimeids.SessionID) (CurrentNodeStartContext, error) {
	if sessionID.IsZero() {
		return CurrentNodeStartContext{}, errors.New("session id is required")
	}
	taskID, err := s.taskIDForSessionOwnership(ctx, sessionID)
	if err != nil {
		return CurrentNodeStartContext{}, err
	}
	if taskID == nil {
		return CurrentNodeStartContext{}, ErrSessionNotCurrentWorkflowNode
	}
	currentNodes, err := s.ListCurrentNodes(ctx, *taskID)
	if err != nil {
		return CurrentNodeStartContext{}, err
	}
	var currentNode *workflow.CurrentNode
	for i := range currentNodes {
		if currentNodes[i].SessionID == nil || *currentNodes[i].SessionID != sessionID {
			continue
		}
		if currentNode != nil {
			return CurrentNodeStartContext{}, fmt.Errorf("session %q is bound to multiple current nodes for task %q", sessionID, *taskID)
		}
		currentNode = &currentNodes[i]
	}
	if currentNode == nil {
		return CurrentNodeStartContext{}, ErrSessionNotCurrentWorkflowNode
	}
	if _, err := s.resolveCurrentNodeSessionBindingAuthority(ctx, sessionID, *currentNode); err != nil {
		return CurrentNodeStartContext{}, err
	}
	return s.resolveCurrentNodeStartContext(ctx, *currentNode)
}

func (s *Store) ResolveCurrentNodeSessionBindingAuthority(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	reference workflow.CurrentNodeReference,
) (CurrentNodeSessionBindingAuthority, error) {
	if sessionID.IsZero() {
		return CurrentNodeSessionBindingAuthority{}, errors.New("session id is required")
	}
	if err := reference.Validate(); err != nil {
		return CurrentNodeSessionBindingAuthority{}, err
	}
	taskID, err := s.taskIDForSessionOwnership(ctx, sessionID)
	if err != nil {
		return CurrentNodeSessionBindingAuthority{}, err
	}
	if taskID == nil || *taskID != reference.TaskID {
		return CurrentNodeSessionBindingAuthority{}, ErrSessionNotCurrentWorkflowNode
	}
	currentNode, err := s.currentNodeForReference(ctx, s.queries, reference)
	if errors.Is(err, sql.ErrNoRows) {
		return CurrentNodeSessionBindingAuthority{}, ErrSessionNotCurrentWorkflowNode
	}
	if err != nil {
		return CurrentNodeSessionBindingAuthority{}, err
	}
	return s.resolveCurrentNodeSessionBindingAuthority(ctx, sessionID, currentNode)
}

func (s *Store) resolveCurrentNodeSessionBindingAuthority(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	currentNode workflow.CurrentNode,
) (CurrentNodeSessionBindingAuthority, error) {
	if currentNode.SessionID == nil || *currentNode.SessionID != sessionID {
		return CurrentNodeSessionBindingAuthority{}, ErrSessionNotCurrentWorkflowNode
	}
	switch currentNode.ContinuationSource.Kind() {
	case workflow.MaterializedContinuationSourceLegacy:
		return CurrentNodeSessionBindingAuthority{kind: CurrentNodeSessionBindingAuthorityLegacyHistorical}, nil
	case workflow.MaterializedContinuationSourceExact:
		sourceSessionID, exact := currentNode.ContinuationSource.ExactSessionID()
		if !exact {
			return CurrentNodeSessionBindingAuthority{}, errors.New("exact Current Node continuation source omitted Session ID")
		}
		association, err := s.CurrentTaskSessionForNode(ctx, currentNode.Reference)
		if errors.Is(err, sql.ErrNoRows) {
			return CurrentNodeSessionBindingAuthority{}, ErrSessionNotCurrentWorkflowNode
		}
		if err != nil {
			return CurrentNodeSessionBindingAuthority{}, err
		}
		if association.SessionID != sessionID || association.SourceSessionID != sourceSessionID {
			return CurrentNodeSessionBindingAuthority{}, ErrSessionNotCurrentWorkflowNode
		}
		return CurrentNodeSessionBindingAuthority{
			kind:        CurrentNodeSessionBindingAuthorityExactCurrent,
			association: &association,
		}, nil
	case workflow.MaterializedContinuationSourceDeferredSelf,
		workflow.MaterializedContinuationSourceAbsent:
		return CurrentNodeSessionBindingAuthority{}, ErrSessionNotCurrentWorkflowNode
	default:
		return CurrentNodeSessionBindingAuthority{}, fmt.Errorf(
			"current node %v has invalid continuation source kind %q",
			currentNode.Reference,
			currentNode.ContinuationSource.Kind(),
		)
	}
}

// ValidateCurrentNodeSessionBinding proves that a volatile exact workflow
// scope still belongs to the Task-owned Session and its exact Current Node.
// It intentionally reads no Run history or execution snapshot.
func (s *Store) ValidateCurrentNodeSessionBinding(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	reference workflow.CurrentNodeReference,
) error {
	_, err := s.ResolveCurrentNodeSessionBindingAuthority(ctx, sessionID, reference)
	return err
}

// RepairCurrentNodeSessionProvenanceForResume is the temporary KENT-534
// compatibility path for directly consistent retained Current Nodes created
// before provenance became atomic with insertion.
func (s *Store) RepairCurrentNodeSessionProvenanceForResume(
	ctx context.Context,
	currentNode workflow.CurrentNode,
) error {
	if currentNode.SessionID == nil {
		return nil
	}
	if currentNode.AgentExecutionSelection == nil {
		return errors.New("retained Resume Current Node must be an Agent")
	}
	if currentNode.Scheduling == nil ||
		currentNode.Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
		return errors.New("retained Resume Current Node must be interrupted")
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	lifecycle := sqlitelifecyclegen.New(connection)
	if err := lifecycle.SetBusyTimeout15Seconds(ctx); err != nil {
		return err
	}
	defer func() { _ = lifecycle.SetBusyTimeout5Seconds(context.Background()) }()
	if err := lifecycle.BeginImmediate(ctx); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = lifecycle.Rollback(context.Background())
		}
	}()
	q := sqlitegen.New(connection)
	persisted, err := s.currentNodeForReference(ctx, q, currentNode.Reference)
	if err != nil {
		return err
	}
	if persisted.SessionID == nil ||
		*persisted.SessionID != *currentNode.SessionID ||
		persisted.AgentExecutionSelection == nil ||
		persisted.Scheduling == nil ||
		persisted.Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
		return ErrSessionNotCurrentWorkflowNode
	}
	if persisted.ContinuationSource.Kind() == workflow.MaterializedContinuationSourceLegacy {
		if err := lifecycle.Commit(ctx); err != nil {
			return err
		}
		committed = true
		return nil
	}
	currentNodes, err := s.listTaskCurrentNodes(ctx, q, currentNode.Reference.TaskID)
	if err != nil {
		return err
	}
	sessionOwners := 0
	for _, candidate := range currentNodes {
		if candidate.SessionID != nil && *candidate.SessionID == *currentNode.SessionID {
			sessionOwners++
			if !candidate.Reference.Equal(currentNode.Reference) {
				return ErrSessionNotCurrentWorkflowNode
			}
		}
	}
	if sessionOwners != 1 {
		return ErrSessionNotCurrentWorkflowNode
	}
	taskIDs, err := q.ListSessionWorkflowTaskIDs(ctx, currentNode.SessionID.String())
	if err != nil {
		return err
	}
	if len(taskIDs) != 1 ||
		!taskIDs[0].Valid ||
		workflow.TaskID(taskIDs[0].String) != currentNode.Reference.TaskID {
		return ErrSessionNotCurrentWorkflowNode
	}
	association, err := currentTaskSessionForNode(
		sqlitegen.WithExpectedNoRows(ctx),
		q,
		currentNode.Reference,
	)
	sourceSessionID, exact := currentNode.ContinuationSource.ExactSessionID()
	if !exact {
		return ErrSessionNotCurrentWorkflowNode
	}
	if err == nil &&
		association.SessionID == *currentNode.SessionID &&
		association.SourceSessionID == sourceSessionID {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	associatedAt := s.now().UTC()
	if err == nil && !associatedAt.After(association.AssociatedAt) {
		associatedAt = association.AssociatedAt.Add(time.Millisecond)
	}
	normalized, err := normalizeTaskSessionAssociationRequest(TaskSessionAssociationRequest{
		SessionID:    *currentNode.SessionID,
		CurrentNode:  currentNode.Reference,
		AssociatedAt: associatedAt,
	})
	if err != nil {
		return err
	}
	if err := designateCurrentTaskSessionAssociation(ctx, q, normalized, sourceSessionID); err != nil {
		return err
	}
	if err := lifecycle.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

// ResolveCurrentNodeStartContext prepares an admitted executable Current Node
// from its own materialized values and the latest definition. It never
// reconstructs discarded execution history.
func (s *Store) ResolveCurrentNodeStartContext(ctx context.Context, reference workflow.CurrentNodeReference) (CurrentNodeStartContext, error) {
	if err := reference.Validate(); err != nil {
		return CurrentNodeStartContext{}, err
	}
	currentNode, err := s.currentNodeForReference(ctx, s.queries, reference)
	if err != nil {
		return CurrentNodeStartContext{}, err
	}
	return s.resolveCurrentNodeStartContext(ctx, currentNode)
}

func (s *Store) resolveCurrentNodeStartContext(ctx context.Context, currentNode workflow.CurrentNode) (CurrentNodeStartContext, error) {
	taskRow, err := s.queries.GetTask(ctx, string(currentNode.Reference.TaskID))
	if err != nil {
		return CurrentNodeStartContext{}, err
	}
	task, err := taskRecordFromTask(taskRow)
	if err != nil {
		return CurrentNodeStartContext{}, err
	}
	definition, workflowRecord, err := s.GetDefinition(ctx, task.WorkflowID)
	if err != nil {
		return CurrentNodeStartContext{}, err
	}
	node, err := currentNodeDefinitionNode(definition, currentNode.Reference.NodeID)
	if err != nil {
		return CurrentNodeStartContext{}, err
	}
	if !executableNodeKind(node.Kind()) {
		return CurrentNodeStartContext{}, fmt.Errorf("current node %v is not executable", currentNode.Reference)
	}
	enteringEdge, err := currentNodeDefinitionEnteringEdge(definition, currentNode)
	if err != nil {
		return CurrentNodeStartContext{}, err
	}
	transitionIDs, transitionOptions, err := s.currentNodeTransitionOptions(ctx, s.queries, definition, currentNode, node)
	if err != nil {
		return CurrentNodeStartContext{}, err
	}
	var executionRoot *ExecutionRoot
	if task.ExecutionTarget != nil {
		root, err := executionRootForTask(ctx, s.queries, taskRow)
		if err != nil {
			return CurrentNodeStartContext{}, err
		}
		executionRoot = &root
	}
	values := cloneCurrentNodeOutputValues(currentNode.CurrentInputValues)
	if _, ok := values[workflow.RuntimePromptParameterCommentary]; !ok {
		values[workflow.RuntimePromptParameterCommentary] = ""
	}
	effectiveContextMode := enteringEdge.ContextMode
	var sourceSessionID *runtimeids.SessionID
	if enteringEdge.ContextMode != workflow.ContextModeNewSession {
		if currentNode.SessionID == nil {
			contextSource := workflow.CanonicalContextSource(enteringEdge.ContextSource).Kind
			if contextSource == workflow.ContextSourcePreviousTargetOrNew ||
				contextSource == workflow.ContextSourcePreviousTarget {
				effectiveContextMode = workflow.ContextModeNewSession
			} else {
				return CurrentNodeStartContext{}, fmt.Errorf("continuation current node %v has no retained session", currentNode.Reference)
			}
		} else {
			value := *currentNode.SessionID
			sourceSessionID = &value
		}
	}
	sourceNode, err := currentNodeDefinitionNode(definition, transitionGroupSourceNodeID(definition, enteringEdge.TransitionGroupID))
	if err != nil {
		return CurrentNodeStartContext{}, fmt.Errorf("resolve entering source node for current node %v: %w", currentNode.Reference, err)
	}
	nodeRecord, err := nodeRecordFromCurrentDefinition(node)
	if err != nil {
		return CurrentNodeStartContext{}, err
	}
	return CurrentNodeStartContext{
		Task:            task,
		Workflow:        workflowRecord,
		Node:            nodeRecord,
		CurrentNode:     currentNode,
		EnteringEdge:    enteringEdge,
		ContextMode:     effectiveContextMode,
		SourceSessionID: sourceSessionID,
		IsFanoutBranch:  currentNode.Reference.IsBranchScoped(),
		AcceptedTransitionPath: AcceptedTransitionPath{
			SourceNodeDisplayName: workflow.NodeDisplayName(sourceNode),
			TargetNodeDisplayName: workflow.NodeDisplayName(node),
		},
		TransitionIDs:                  transitionIDs,
		TransitionOptions:              transitionOptions,
		HasContinueSessionOutgoingEdge: currentNodeHasContinueSessionOutgoingEdge(definition, workflow.NodeIDOf(node)),
		TransitionPrompt:               strings.TrimSpace(enteringEdge.PromptTemplate),
		ParameterValues:                values,
		ExecutionRoot:                  executionRoot,
	}, nil
}

func currentNodeHasContinueSessionOutgoingEdge(definition workflow.Definition, nodeID workflow.NodeID) bool {
	for _, edge := range definition.Edges {
		if edge.ContextMode != workflow.ContextModeContinueSession && edge.ContextMode != workflow.ContextModeCompactAndContinueSession {
			continue
		}
		if transitionGroupSourceNodeID(definition, edge.TransitionGroupID) == nodeID {
			return true
		}
	}
	return false
}

func transitionGroupSourceNodeID(definition workflow.Definition, groupID workflow.TransitionGroupID) workflow.NodeID {
	for _, group := range definition.TransitionGroups {
		if group.ID == groupID {
			return group.SourceNodeID
		}
	}
	return ""
}

func (s *Store) currentNodeTransitionOptions(
	ctx context.Context,
	q *sqlitegen.Queries,
	definition workflow.Definition,
	currentSource workflow.CurrentNode,
	source workflow.Node,
) ([]string, []TransitionOption, error) {
	transitionIDs := make([]string, 0)
	options := make([]TransitionOption, 0)
	for _, group := range definition.TransitionGroups {
		if group.SourceNodeID != workflow.NodeIDOf(source) {
			continue
		}
		transitionID := strings.TrimSpace(string(group.TransitionID))
		if transitionID == "" {
			return nil, nil, fmt.Errorf("workflow %q has a blank transition id for current node %q", definition.ID, workflow.NodeIDOf(source))
		}
		parameters, err := s.currentNodeTransitionParameters(ctx, q, definition, group, currentSource, source)
		if err != nil {
			return nil, nil, err
		}
		transitionIDs = append(transitionIDs, transitionID)
		options = append(options, TransitionOption{
			ID:          transitionID,
			DisplayName: strings.TrimSpace(group.DisplayName),
			Description: strings.TrimSpace(group.Description),
			Parameters:  parameters,
		})
	}
	return transitionIDs, options, nil
}

func (s *Store) currentNodeTransitionParameters(
	ctx context.Context,
	q *sqlitegen.Queries,
	definition workflow.Definition,
	group workflow.TransitionGroup,
	currentSource workflow.CurrentNode,
	source workflow.Node,
) ([]workflow.Parameter, error) {
	wiring := workflow.DeriveWiring(definition)
	if source.Kind() == workflow.NodeKindJoin {
		return parametersFromOutputFields(wiring.JoinOutputFieldsForNode(group.SourceNodeID)), nil
	}
	var parameters []workflow.Parameter
	for _, edge := range definition.Edges {
		if edge.TransitionGroupID == group.ID {
			target, err := currentNodeDefinitionNode(definition, edge.TargetNodeID)
			if err != nil {
				return nil, err
			}
			planned, err := s.planTransitionParameterContract(
				ctx,
				q,
				definition,
				edge,
				source,
				target,
				&currentSource,
				transitionBranchKeyForCurrentNode(currentSource.Reference),
				false,
				true,
				transitionContractContextResolutionDeferred,
			)
			if err != nil {
				return nil, err
			}
			for _, parameter := range planned.Parameters {
				if _, exists := parameterByKey(parameters, parameter.Key); !exists {
					parameters = append(parameters, parameter)
				}
			}
		}
	}
	return parameters, nil
}

func parametersFromOutputFields(fields []workflow.OutputField) []workflow.Parameter {
	out := make([]workflow.Parameter, 0, len(fields))
	for _, field := range fields {
		out = append(out, workflow.Parameter{Key: field.Name, Description: field.Description, Purpose: workflow.ParameterPurposeOrdinary})
	}
	return out
}

func parameterByKey(parameters []workflow.Parameter, key string) (workflow.Parameter, bool) {
	for _, parameter := range parameters {
		if parameter.Key == key {
			return parameter, true
		}
	}
	return workflow.Parameter{}, false
}

func nodeRecordFromCurrentDefinition(node workflow.Node) (NodeRecord, error) {
	workflowID := workflow.NodeWorkflowID(node)
	if workflowID == nil || workflowID.IsZero() {
		return NodeRecord{}, errors.New("current definition node workflow id is required")
	}
	scriptPath := ""
	if path := workflow.NodeScriptPath(node); path.IsPresent() {
		scriptPath = path.String()
	}
	groupID, hasGroup := workflow.NodeGroupID(node)
	var groupIDPointer *string
	if hasGroup {
		groupIDPointer = &groupID
	}
	return NodeRecord{
		ID:                 workflow.NodeIDOf(node),
		WorkflowID:         *workflowID,
		Key:                workflow.NodeKey(node),
		Kind:               node.Kind(),
		DisplayName:        workflow.NodeDisplayName(node),
		GroupID:            groupIDPointer,
		SubagentRole:       workflow.NodeSubagentRole(node),
		CompletionMode:     workflow.NodeCompletionMode(node),
		ScriptPath:         scriptPath,
		JoinInputProviders: workflow.NodeJoinInputProviders(node),
	}, nil
}

func currentTaskSessionForNode(ctx context.Context, q *sqlitegen.Queries, currentNode workflow.CurrentNodeReference) (TaskSessionAssociation, error) {
	if err := currentNode.Validate(); err != nil {
		return TaskSessionAssociation{}, err
	}
	var sessionIDRaw string
	var sourceSessionIDRaw string
	var associatedAtUnixMs int64
	if branchKey, branchScoped := currentNode.TransitionBranchKey(); branchScoped {
		row, err := q.GetCurrentBranchTaskSessionAssociationForNode(ctx, sqlitegen.GetCurrentBranchTaskSessionAssociationForNodeParams{
			TaskID:              string(currentNode.TaskID),
			NodeID:              string(currentNode.NodeID),
			TransitionBranchKey: sql.NullString{String: string(branchKey), Valid: true},
		})
		if err != nil {
			return TaskSessionAssociation{}, err
		}
		sessionIDRaw = row.SessionID
		sourceSessionIDRaw = row.SourceSessionID.String
		associatedAtUnixMs = row.AssociatedAtUnixMs
	} else {
		row, err := q.GetCurrentSerialTaskSessionAssociationForNode(ctx, sqlitegen.GetCurrentSerialTaskSessionAssociationForNodeParams{
			TaskID: string(currentNode.TaskID),
			NodeID: string(currentNode.NodeID),
		})
		if err != nil {
			return TaskSessionAssociation{}, err
		}
		sessionIDRaw = row.SessionID
		sourceSessionIDRaw = row.SourceSessionID.String
		associatedAtUnixMs = row.AssociatedAtUnixMs
	}
	sessionID, err := runtimeids.ParseSessionID(sessionIDRaw)
	if err != nil {
		return TaskSessionAssociation{}, fmt.Errorf("decode associated session id: %w", err)
	}
	sourceSessionID, err := runtimeids.ParseSessionID(sourceSessionIDRaw)
	if err != nil {
		return TaskSessionAssociation{}, fmt.Errorf("decode associated source Session id: %w", err)
	}
	return TaskSessionAssociation{
		SessionID:       sessionID,
		SourceSessionID: sourceSessionID,
		CurrentNode:     currentNode,
		AssociatedAt:    time.UnixMilli(associatedAtUnixMs).UTC(),
	}, nil
}

func normalizeTaskSessionAssociationRequest(req TaskSessionAssociationRequest) (TaskSessionAssociationRequest, error) {
	if req.SessionID.IsZero() {
		return TaskSessionAssociationRequest{}, errors.New("session id is required")
	}
	if err := req.CurrentNode.Validate(); err != nil {
		return TaskSessionAssociationRequest{}, err
	}
	if req.AssociatedAt.IsZero() || req.AssociatedAt.UnixMilli() <= 0 {
		return TaskSessionAssociationRequest{}, errors.New("association time is required")
	}
	req.AssociatedAt = req.AssociatedAt.UTC().Truncate(time.Millisecond)
	return req, nil
}
