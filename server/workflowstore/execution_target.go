package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

type ExecutionTargetProvenance string

const (
	ExecutionTargetProvenanceResolved       ExecutionTargetProvenance = "resolved"
	ExecutionTargetProvenanceLegacyObserved ExecutionTargetProvenance = "legacy_observed"
)

type ExecutionTargetSnapshot struct {
	Mode         workflow.ExecutionTargetMode
	RequestedRef *string
	ResolvedRef  *string
	CommitOID    *string
	Provenance   ExecutionTargetProvenance
}

type ExecutionTargetCandidate struct {
	Snapshot ExecutionTargetSnapshot
	Root     ExecutionRoot
}

type TaskExecutionTargetContext struct {
	Task                TaskRecord
	Policy              workflow.ExecutionTargetPolicy
	SourceWorkspaceID   string
	SourceWorkspaceRoot string
}

var (
	ErrExecutionTargetRequired      = errors.New("execution target is required")
	ErrExecutionTargetAlreadyLocked = errors.New("execution target is already locked")
)

func (s *Store) GetTaskExecutionTargetContext(ctx context.Context, taskID workflow.TaskID) (TaskExecutionTargetContext, error) {
	task, err := s.queries.GetTask(ctx, strings.TrimSpace(string(taskID)))
	if err != nil {
		return TaskExecutionTargetContext{}, err
	}
	taskRecord, err := taskRecordFromTask(task)
	if err != nil {
		return TaskExecutionTargetContext{}, err
	}
	workflowRecord, err := s.queries.GetWorkflow(ctx, task.WorkflowID)
	if err != nil {
		return TaskExecutionTargetContext{}, err
	}
	sourceWorkspace, err := taskSourceWorkspaceForExecution(ctx, s.queries, task)
	if err != nil {
		return TaskExecutionTargetContext{}, err
	}
	return TaskExecutionTargetContext{
		Task: taskRecord,
		Policy: normalizeWorkflowExecutionTargetPolicy(workflow.ExecutionTargetPolicy{
			Mode:      workflow.ExecutionTargetMode(workflowRecord.ExecutionTargetPolicy),
			CustomRef: workflowCustomRefFromRow(workflowRecord.ExecutionTargetCustomRef),
		}),
		SourceWorkspaceID:   sourceWorkspace.ID,
		SourceWorkspaceRoot: sourceWorkspace.CanonicalRootPath,
	}, nil
}

type preparedExecutionTargetMutation struct {
	executionRoot   ExecutionRoot
	candidateToLock *ExecutionTargetCandidate
}

func (s *Store) prepareExecutionTargetMutation(ctx context.Context, task sqlitegen.TaskRecord, candidate *ExecutionTargetCandidate) (preparedExecutionTargetMutation, error) {
	snapshot, err := executionTargetSnapshotFromTask(task)
	if err != nil {
		return preparedExecutionTargetMutation{}, err
	}
	if snapshot == nil {
		if candidate == nil {
			return preparedExecutionTargetMutation{}, ErrExecutionTargetRequired
		}
		if err := validateExecutionTargetCandidateForTask(ctx, s.queries, task, *candidate); err != nil {
			return preparedExecutionTargetMutation{}, err
		}
		return preparedExecutionTargetMutation{
			executionRoot:   candidate.Root,
			candidateToLock: candidate,
		}, nil
	}
	if candidate != nil {
		return preparedExecutionTargetMutation{}, ErrExecutionTargetAlreadyLocked
	}
	root, err := executionRootForTask(ctx, s.queries, task)
	if err != nil {
		return preparedExecutionTargetMutation{}, err
	}
	return preparedExecutionTargetMutation{executionRoot: root}, nil
}

func applyPreparedExecutionTargetMutation(ctx context.Context, q *sqlitegen.Queries, task sqlitegen.TaskRecord, prepared preparedExecutionTargetMutation, now int64) error {
	if prepared.candidateToLock != nil {
		locked, err := q.LockTaskExecutionTarget(ctx, executionTargetLockParams(task, *prepared.candidateToLock, now))
		if err != nil {
			return err
		}
		if locked != 1 {
			return sql.ErrNoRows
		}
	}
	return nil
}

func (c ExecutionTargetCandidate) Validate() error {
	if err := c.Snapshot.Validate(); err != nil {
		return err
	}
	if err := c.Root.Validate(); err != nil {
		return err
	}
	if c.Snapshot.Mode == workflow.ExecutionTargetModeNone {
		if c.Root.Managed != nil {
			return errors.New("none execution target candidate has a managed execution root")
		}
		return nil
	}
	if c.Root.Managed == nil {
		return errors.New("managed execution target candidate has no managed execution root")
	}
	return nil
}

type ExecutionRoot struct {
	SourceWorkspaceID   string
	SourceWorkspaceRoot string
	Managed             *ManagedExecutionRoot
}

type ManagedExecutionRoot struct {
	WorktreeID string
	Root       string
}

func (r ExecutionRoot) EffectiveRoot() string {
	if r.Managed != nil {
		return r.Managed.Root
	}
	return r.SourceWorkspaceRoot
}

type ExecutionRootErrorKind string

const (
	ExecutionRootErrorSnapshotMissing          ExecutionRootErrorKind = "snapshot_missing"
	ExecutionRootErrorSourceWorkspaceMissing   ExecutionRootErrorKind = "source_workspace_missing"
	ExecutionRootErrorSourceWorkspaceOwnership ExecutionRootErrorKind = "source_workspace_ownership"
	ExecutionRootErrorManagedRelationMissing   ExecutionRootErrorKind = "managed_relation_missing"
	ExecutionRootErrorManagedRecordMissing     ExecutionRootErrorKind = "managed_record_missing"
)

type ExecutionRootError struct {
	Kind  ExecutionRootErrorKind
	Cause error
}

func (e *ExecutionRootError) Error() string {
	if e == nil {
		return "execution root materialization failed"
	}
	return "execution root materialization failed: " + string(e.Kind)
}

func (e *ExecutionRootError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (s ExecutionTargetSnapshot) Validate() error {
	if s.Mode == workflow.ExecutionTargetModeNone {
		if s.RequestedRef != nil || s.ResolvedRef != nil || s.CommitOID != nil || !validExecutionTargetProvenance(s.Provenance) {
			return errors.New("none execution target snapshot has managed facts")
		}
		return nil
	}
	if s.Mode != workflow.ExecutionTargetModeHead && s.Mode != workflow.ExecutionTargetModeDefaultBranch && s.Mode != workflow.ExecutionTargetModeCustomRef {
		return errors.New("execution target snapshot mode is invalid")
	}
	if s.RequestedRef == nil || strings.TrimSpace(*s.RequestedRef) == "" || s.CommitOID == nil || strings.TrimSpace(*s.CommitOID) == "" || !validExecutionTargetProvenance(s.Provenance) {
		return errors.New("managed execution target snapshot is incomplete")
	}
	if s.ResolvedRef != nil && strings.TrimSpace(*s.ResolvedRef) == "" {
		return errors.New("execution target snapshot resolved ref is blank")
	}
	return nil
}

func (r ExecutionRoot) Validate() error {
	if strings.TrimSpace(r.SourceWorkspaceID) == "" || strings.TrimSpace(r.SourceWorkspaceRoot) == "" {
		return errors.New("execution root source workspace is required")
	}
	if r.Managed == nil {
		return nil
	}
	if strings.TrimSpace(r.Managed.WorktreeID) == "" || strings.TrimSpace(r.Managed.Root) == "" {
		return errors.New("managed execution root is incomplete")
	}
	return nil
}

func validExecutionTargetProvenance(value ExecutionTargetProvenance) bool {
	return value == ExecutionTargetProvenanceResolved || value == ExecutionTargetProvenanceLegacyObserved
}

func executionTargetSnapshotFromFields(mode *string, requestedRef *string, resolvedRef *string, commitOID *string, provenance *string) (*ExecutionTargetSnapshot, error) {
	if mode == nil {
		if requestedRef != nil || resolvedRef != nil || commitOID != nil || provenance != nil {
			return nil, errors.New("unlocked task has execution target facts")
		}
		return nil, nil
	}
	snapshot := ExecutionTargetSnapshot{
		Mode:         workflow.ExecutionTargetMode(*mode),
		RequestedRef: requestedRef,
		ResolvedRef:  resolvedRef,
		CommitOID:    commitOID,
	}
	if provenance != nil {
		snapshot.Provenance = ExecutionTargetProvenance(*provenance)
	}
	if err := snapshot.Validate(); err != nil {
		return nil, fmt.Errorf("decode execution target snapshot: %w", err)
	}
	return &snapshot, nil
}

func executionTargetSnapshotFromTask(row sqlitegen.TaskRecord) (*ExecutionTargetSnapshot, error) {
	return executionTargetSnapshotFromFields(
		metadata.OptionalString(row.ExecutionTargetMode),
		metadata.OptionalString(row.ExecutionTargetRequestedRef),
		metadata.OptionalString(row.ExecutionTargetResolvedRef),
		metadata.OptionalString(row.ExecutionTargetCommitOid),
		metadata.OptionalString(row.ExecutionTargetProvenance),
	)
}

func executionRootForTask(ctx context.Context, q *sqlitegen.Queries, task sqlitegen.TaskRecord) (ExecutionRoot, error) {
	snapshot, err := executionTargetSnapshotFromTask(task)
	if err != nil {
		return ExecutionRoot{}, err
	}
	if snapshot == nil {
		return ExecutionRoot{}, &ExecutionRootError{Kind: ExecutionRootErrorSnapshotMissing}
	}
	sourceWorkspace, err := taskSourceWorkspaceForExecution(ctx, q, task)
	if err != nil {
		return ExecutionRoot{}, err
	}
	root := ExecutionRoot{
		SourceWorkspaceID:   sourceWorkspace.ID,
		SourceWorkspaceRoot: sourceWorkspace.CanonicalRootPath,
	}
	if snapshot.Mode == workflow.ExecutionTargetModeNone {
		return root, root.Validate()
	}
	worktreeID := strings.TrimSpace(task.ManagedWorktreeID.String)
	if !task.ManagedWorktreeID.Valid || worktreeID == "" {
		return ExecutionRoot{}, &ExecutionRootError{Kind: ExecutionRootErrorManagedRelationMissing}
	}
	return executionRootForManagedWorktree(ctx, q, sourceWorkspace, worktreeID)
}

// executionRootForLockedTaskIfPresent returns the derived execution root only
// after target selection has been locked to the task.
func executionRootForLockedTaskIfPresent(ctx context.Context, q *sqlitegen.Queries, task sqlitegen.TaskRecord) (*ExecutionRoot, error) {
	snapshot, err := executionTargetSnapshotFromTask(task)
	if err != nil {
		return nil, err
	}
	if snapshot == nil {
		return nil, nil
	}
	root, err := executionRootForTask(ctx, q, task)
	if err != nil {
		return nil, err
	}
	return &root, nil
}

func executionRootForManagedWorktree(ctx context.Context, q *sqlitegen.Queries, sourceWorkspace sqlitegen.Workspace, managedWorktreeID string) (ExecutionRoot, error) {
	worktreeID := strings.TrimSpace(managedWorktreeID)
	if worktreeID == "" {
		return ExecutionRoot{}, &ExecutionRootError{Kind: ExecutionRootErrorManagedRelationMissing}
	}
	worktree, err := q.GetWorktreeByID(ctx, worktreeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ExecutionRoot{}, &ExecutionRootError{Kind: ExecutionRootErrorManagedRecordMissing, Cause: err}
		}
		return ExecutionRoot{}, err
	}
	if strings.TrimSpace(worktree.WorkspaceID) != sourceWorkspace.ID {
		return ExecutionRoot{}, &ExecutionRootError{
			Kind:  ExecutionRootErrorSourceWorkspaceOwnership,
			Cause: fmt.Errorf("managed worktree workspace %q does not match task source workspace %q", worktree.WorkspaceID, sourceWorkspace.ID),
		}
	}
	worktreeID = strings.TrimSpace(worktree.ID)
	worktreeRoot := strings.TrimSpace(worktree.CanonicalRootPath)
	root := ExecutionRoot{
		SourceWorkspaceID:   sourceWorkspace.ID,
		SourceWorkspaceRoot: sourceWorkspace.CanonicalRootPath,
		Managed: &ManagedExecutionRoot{
			WorktreeID: worktreeID,
			Root:       worktreeRoot,
		},
	}
	if err := root.Validate(); err != nil {
		return ExecutionRoot{}, err
	}
	return root, nil
}

func validateExecutionTargetCandidateForTask(ctx context.Context, q *sqlitegen.Queries, task sqlitegen.TaskRecord, candidate ExecutionTargetCandidate) error {
	if err := candidate.Validate(); err != nil {
		return fmt.Errorf("invalid execution target candidate: %w", err)
	}
	sourceWorkspace, err := taskSourceWorkspaceForExecution(ctx, q, task)
	if err != nil {
		return err
	}
	if candidate.Root.SourceWorkspaceID != sourceWorkspace.ID || candidate.Root.SourceWorkspaceRoot != sourceWorkspace.CanonicalRootPath {
		return errors.New("execution target candidate source workspace does not match task source workspace")
	}
	if candidate.Snapshot.Mode == workflow.ExecutionTargetModeNone {
		return nil
	}
	if !task.ManagedWorktreeID.Valid || strings.TrimSpace(task.ManagedWorktreeID.String) == "" || candidate.Root.Managed == nil || task.ManagedWorktreeID.String != candidate.Root.Managed.WorktreeID {
		return errors.New("managed execution target candidate does not match task provisional worktree")
	}
	return nil
}

func executionTargetLockParams(task sqlitegen.TaskRecord, candidate ExecutionTargetCandidate, updatedAtUnixMs int64) sqlitegen.LockTaskExecutionTargetParams {
	snapshot := candidate.Snapshot
	managedWorktreeID := sql.NullString{}
	if candidate.Root.Managed != nil {
		managedWorktreeID = nullableString(candidate.Root.Managed.WorktreeID)
	}
	return sqlitegen.LockTaskExecutionTargetParams{
		ManagedWorktreeID:           managedWorktreeID,
		ExecutionTargetMode:         nullableString(string(snapshot.Mode)),
		ExecutionTargetRequestedRef: nullableStringPointer(snapshot.RequestedRef),
		ExecutionTargetResolvedRef:  nullableStringPointer(snapshot.ResolvedRef),
		ExecutionTargetCommitOid:    nullableStringPointer(snapshot.CommitOID),
		ExecutionTargetProvenance:   nullableString(string(snapshot.Provenance)),
		UpdatedAtUnixMs:             updatedAtUnixMs,
		TaskID:                      task.ID,
		ExpectedManagedWorktreeID:   task.ManagedWorktreeID,
	}
}

func nullableStringPointer(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return nullableString(*value)
}

func taskSourceWorkspaceForExecution(ctx context.Context, q *sqlitegen.Queries, task sqlitegen.TaskRecord) (sqlitegen.Workspace, error) {
	sourceWorkspaceID := strings.TrimSpace(task.SourceWorkspaceID.String)
	if sourceWorkspaceID == "" {
		workspaces, err := q.ListProjectWorkspaces(ctx, task.ProjectID)
		if err != nil {
			return sqlitegen.Workspace{}, err
		}
		for _, workspace := range workspaces {
			if workspace.IsPrimary != 0 {
				sourceWorkspaceID = workspace.ID
				break
			}
		}
		if sourceWorkspaceID == "" && len(workspaces) > 0 {
			sourceWorkspaceID = workspaces[0].ID
		}
	}
	if sourceWorkspaceID == "" {
		return sqlitegen.Workspace{}, &ExecutionRootError{Kind: ExecutionRootErrorSourceWorkspaceMissing}
	}
	workspace, err := q.GetWorkspaceByID(ctx, sourceWorkspaceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sqlitegen.Workspace{}, &ExecutionRootError{Kind: ExecutionRootErrorSourceWorkspaceMissing, Cause: err}
		}
		return sqlitegen.Workspace{}, err
	}
	if strings.TrimSpace(workspace.ProjectID) != strings.TrimSpace(task.ProjectID) {
		return sqlitegen.Workspace{}, &ExecutionRootError{
			Kind:  ExecutionRootErrorSourceWorkspaceOwnership,
			Cause: fmt.Errorf("source workspace %q does not belong to task project %q", workspace.ID, task.ProjectID),
		}
	}
	return workspace, nil
}
