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

func (s *Store) SaveTaskExecutionTarget(ctx context.Context, target workflow.ExecutionTarget) error {
	if err := target.Validate(); err != nil {
		return err
	}
	return insertValidatedTaskExecutionTarget(ctx, s.queries, target)
}

func insertValidatedTaskExecutionTarget(ctx context.Context, q *sqlitegen.Queries, target workflow.ExecutionTarget) error {
	var resolvedSourceKind sql.NullString
	var resolvedSourceRef sql.NullString
	var resolvedCommit sql.NullString
	if target.ResolvedSource != nil {
		resolvedSourceKind = sql.NullString{String: string(target.ResolvedSource.Kind), Valid: true}
		resolvedSourceRef = nullableExecutionTargetString(target.ResolvedSource.NamedRef)
		resolvedCommit = sql.NullString{String: target.ResolvedSource.Commit, Valid: true}
	}
	var claimGeneration sql.NullString
	var claimPhase sql.NullString
	if target.ActiveClaim != nil {
		claimGeneration = sql.NullString{String: target.ActiveClaim.Generation, Valid: true}
		claimPhase = sql.NullString{String: string(target.ActiveClaim.Phase), Valid: true}
	}
	var recoveryCause sql.NullString
	if target.RecoveryCause != nil {
		recoveryCause = sql.NullString{String: string(*target.RecoveryCause), Valid: true}
	}
	var commonDir sql.NullString
	var adminEntry sql.NullString
	var gitDir sql.NullString
	var headRef sql.NullString
	if target.LinkedWorktreeOwnership != nil {
		commonDir = sql.NullString{String: target.LinkedWorktreeOwnership.CommonDir, Valid: true}
		adminEntry = sql.NullString{String: target.LinkedWorktreeOwnership.AdminEntry, Valid: true}
		gitDir = sql.NullString{String: target.LinkedWorktreeOwnership.GitDir, Valid: true}
		headRef = sql.NullString{String: target.LinkedWorktreeOwnership.HeadRef, Valid: true}
	}
	return q.InsertTaskExecutionTarget(ctx, sqlitegen.InsertTaskExecutionTargetParams{
		TaskID:                      string(target.TaskID),
		Policy:                      string(target.Policy),
		RequestedCustomRef:          nullableExecutionTargetString(target.RequestedCustomRef),
		ResolvedSourceKind:          resolvedSourceKind,
		ResolvedSourceRef:           resolvedSourceRef,
		ResolvedCommit:              resolvedCommit,
		State:                       string(target.State),
		ProvisioningGeneration:      nullableExecutionTargetString(target.ProvisioningGeneration),
		SetupProvisioningGeneration: nullableExecutionTargetString(target.SetupProvisioningGeneration),
		SetupState:                  string(target.SetupState),
		ActiveClaimGeneration:       claimGeneration,
		ActiveClaimPhase:            claimPhase,
		RecoveryDisposition:         string(target.RecoveryDisposition),
		RecoveryCause:               recoveryCause,
		ExactBranchObservation:      nullableExecutionTargetString(target.ExactBranchObservation),
		LinkedWorktreeCommonDir:     commonDir,
		LinkedWorktreeAdminEntry:    adminEntry,
		LinkedWorktreeGitdir:        gitDir,
		LinkedWorktreeHeadRef:       headRef,
		ExpectedDetachmentCommit:    nullableExecutionTargetString(target.ExpectedDetachmentCommit),
	})
}

func (s *Store) GetTaskExecutionTarget(ctx context.Context, taskID workflow.TaskID) (*workflow.ExecutionTarget, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return nil, errors.New("task id is required")
	}
	row, err := s.queries.GetTaskExecutionTarget(ctx, string(taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get task execution target: %w", err)
	}
	target, err := taskExecutionTargetFromRow(row)
	if err != nil {
		return nil, fmt.Errorf("decode task execution target: %w", err)
	}
	return &target, nil
}

func nullableExecutionTargetString(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func (s *Store) ResolveTaskExecutionRoot(ctx context.Context, taskID workflow.TaskID) (workflow.ExecutionRoot, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return workflow.ExecutionRoot{}, errors.New("task id is required")
	}
	task, err := s.queries.GetTask(ctx, string(taskID))
	if err != nil {
		return workflow.ExecutionRoot{}, err
	}
	target, err := s.GetTaskExecutionTarget(ctx, taskID)
	if err != nil {
		return workflow.ExecutionRoot{}, err
	}
	if target == nil {
		return workflow.ExecutionRoot{}, ErrTaskExecutionTargetNotMaterialized
	}
	workspaceID := strings.TrimSpace(task.SourceWorkspaceID.String)
	if workspaceID == "" {
		return workflow.ExecutionRoot{}, errors.New("task source workspace is required for execution root")
	}
	workspace, err := s.metadata.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		return workflow.ExecutionRoot{}, err
	}
	root := workflow.ExecutionRoot{
		SourceWorkspace: workflow.ExecutionWorkspace{
			ID:   workspace.ID,
			Root: workspace.CanonicalRootPath,
		},
	}
	if target.Policy == workflow.ExecutionPolicyNone {
		root.EffectiveRoot = root.SourceWorkspace.Root
	} else {
		worktreeID := strings.TrimSpace(task.ManagedWorktreeID.String)
		if worktreeID == "" {
			return workflow.ExecutionRoot{}, ErrTaskExecutionRootUnavailable
		}
		worktree, err := s.metadata.GetWorktreeRecordByID(ctx, worktreeID)
		if err != nil {
			return workflow.ExecutionRoot{}, err
		}
		root.ManagedWorktree = &workflow.ExecutionWorktree{ID: worktree.ID, Root: worktree.CanonicalRoot}
		root.EffectiveRoot = worktree.CanonicalRoot
	}
	if err := root.Validate(); err != nil {
		return workflow.ExecutionRoot{}, fmt.Errorf("invalid task execution root: %w", err)
	}
	return root, nil
}

func taskExecutionTargetFromRow(row sqlitegen.TaskExecutionTarget) (workflow.ExecutionTarget, error) {
	requestedCustomRef := executionTargetOptionalString(row.RequestedCustomRef)
	resolvedSourceKind := executionTargetOptionalString(row.ResolvedSourceKind)
	resolvedSourceRef := executionTargetOptionalString(row.ResolvedSourceRef)
	resolvedCommit := executionTargetOptionalString(row.ResolvedCommit)
	if (resolvedSourceKind == nil) != (resolvedCommit == nil) || (resolvedSourceKind == nil && resolvedSourceRef != nil) {
		return workflow.ExecutionTarget{}, errors.New("resolved source facts are incomplete")
	}
	var resolvedSource *workflow.ExecutionTargetResolvedSource
	if resolvedSourceKind != nil {
		resolvedSource = &workflow.ExecutionTargetResolvedSource{
			Kind:     workflow.ExecutionTargetSourceKind(*resolvedSourceKind),
			NamedRef: resolvedSourceRef,
		}
		if resolvedCommit != nil {
			resolvedSource.Commit = *resolvedCommit
		}
	}
	claimGeneration := executionTargetOptionalString(row.ActiveClaimGeneration)
	claimPhase := executionTargetOptionalString(row.ActiveClaimPhase)
	if err := requireAllOrNoneExecutionTargetValues("active claim", claimGeneration, claimPhase); err != nil {
		return workflow.ExecutionTarget{}, err
	}
	var claim *workflow.ExecutionTargetClaim
	if claimGeneration != nil && claimPhase != nil {
		claim = &workflow.ExecutionTargetClaim{
			Generation: *claimGeneration,
			Phase:      workflow.ExecutionTargetClaimPhase(*claimPhase),
		}
	}
	commonDir := executionTargetOptionalString(row.LinkedWorktreeCommonDir)
	adminEntry := executionTargetOptionalString(row.LinkedWorktreeAdminEntry)
	gitDir := executionTargetOptionalString(row.LinkedWorktreeGitdir)
	headRef := executionTargetOptionalString(row.LinkedWorktreeHeadRef)
	if err := requireAllOrNoneExecutionTargetValues("linked worktree ownership", commonDir, adminEntry, gitDir, headRef); err != nil {
		return workflow.ExecutionTarget{}, err
	}
	var ownership *workflow.ExecutionTargetLinkedWorktreeOwnership
	if commonDir != nil && adminEntry != nil && gitDir != nil && headRef != nil {
		ownership = &workflow.ExecutionTargetLinkedWorktreeOwnership{
			CommonDir:  *commonDir,
			AdminEntry: *adminEntry,
			GitDir:     *gitDir,
			HeadRef:    *headRef,
		}
	}
	recoveryCause := executionTargetOptionalString(row.RecoveryCause)
	var typedRecoveryCause *workflow.ExecutionTargetRecoveryCause
	if recoveryCause != nil {
		value := workflow.ExecutionTargetRecoveryCause(*recoveryCause)
		typedRecoveryCause = &value
	}
	target := workflow.ExecutionTarget{
		TaskID:                      workflow.TaskID(row.TaskID),
		Policy:                      workflow.ExecutionPolicyMode(row.Policy),
		RequestedCustomRef:          requestedCustomRef,
		ResolvedSource:              resolvedSource,
		State:                       workflow.ExecutionTargetState(row.State),
		ProvisioningGeneration:      executionTargetOptionalString(row.ProvisioningGeneration),
		SetupProvisioningGeneration: executionTargetOptionalString(row.SetupProvisioningGeneration),
		SetupState:                  workflow.ExecutionTargetSetupState(row.SetupState),
		ActiveClaim:                 claim,
		RecoveryDisposition:         workflow.ExecutionTargetRecoveryDisposition(row.RecoveryDisposition),
		RecoveryCause:               typedRecoveryCause,
		ExactBranchObservation:      executionTargetOptionalString(row.ExactBranchObservation),
		LinkedWorktreeOwnership:     ownership,
		ExpectedDetachmentCommit:    executionTargetOptionalString(row.ExpectedDetachmentCommit),
	}
	if err := target.Validate(); err != nil {
		return workflow.ExecutionTarget{}, err
	}
	return target, nil
}

func executionTargetOptionalString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func requireAllOrNoneExecutionTargetValues(name string, values ...*string) error {
	present := 0
	for _, value := range values {
		if value != nil {
			present++
		}
	}
	if present != 0 && present != len(values) {
		return fmt.Errorf("%s facts are incomplete", name)
	}
	return nil
}
