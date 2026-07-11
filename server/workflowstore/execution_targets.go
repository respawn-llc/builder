package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/config"
)

func (s *Store) SaveTaskExecutionTarget(ctx context.Context, target workflow.ExecutionTarget) error {
	if err := target.Validate(); err != nil {
		return err
	}
	return insertValidatedTaskExecutionTarget(ctx, s.queries, target)
}

type BeginManagedExecutionTargetMaterializationRequest struct {
	Target              workflow.ExecutionTarget
	ExpectedNegotiation *workflow.ExecutionTargetNegotiation
}

type ExecutionTargetRecoveryContext struct {
	Target          workflow.ExecutionTarget
	TaskShortID     string
	ProjectID       string
	SourceWorkspace workflow.ExecutionWorkspace
}

func (s *Store) BeginManagedExecutionTargetMaterialization(ctx context.Context, req BeginManagedExecutionTargetMaterializationRequest) error {
	target := req.Target
	if err := target.Validate(); err != nil {
		return err
	}
	if target.Policy == workflow.ExecutionPolicyNone ||
		target.State != workflow.ExecutionTargetStateInitialProvisioning ||
		target.ActiveClaim == nil ||
		target.ActiveClaim.Phase != workflow.ExecutionTargetClaimMaterializing {
		return errors.New("managed execution target materialization requires an initial materializing claim")
	}
	if req.ExpectedNegotiation != nil {
		if err := req.ExpectedNegotiation.Validate(); err != nil {
			return err
		}
		if req.ExpectedNegotiation.TaskID != target.TaskID {
			return errors.New("managed execution target negotiation must belong to the target task")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	negotiationRow, err := q.GetTaskExecutionTargetNegotiation(ctx, string(target.TaskID))
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("get task execution target negotiation: %w", err)
		}
		if req.ExpectedNegotiation != nil {
			return ErrTaskExecutionTargetNegotiationChanged
		}
	} else {
		negotiation, decodeErr := taskExecutionTargetNegotiationFromRow(negotiationRow)
		if decodeErr != nil {
			return fmt.Errorf("decode task execution target negotiation: %w", decodeErr)
		}
		if req.ExpectedNegotiation == nil {
			return ErrTaskExecutionTargetNegotiationInProgress
		}
		if !executionTargetNegotiationsEqual(negotiation, *req.ExpectedNegotiation) {
			return ErrTaskExecutionTargetNegotiationChanged
		}
	}
	if _, err := q.DeleteTaskExecutionTargetNegotiation(ctx, string(target.TaskID)); err != nil {
		return err
	}
	if err := insertValidatedTaskExecutionTarget(ctx, q, target); err != nil {
		return err
	}
	return tx.Commit()
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
	lifecycle := executionTargetLifecycleFieldsFromTarget(target)
	return q.InsertTaskExecutionTarget(ctx, sqlitegen.InsertTaskExecutionTargetParams{
		TaskID:                      string(target.TaskID),
		Policy:                      string(target.Policy),
		RequestedCustomRef:          nullableExecutionTargetString(target.RequestedCustomRef),
		ResolvedSourceKind:          resolvedSourceKind,
		ResolvedSourceRef:           resolvedSourceRef,
		ResolvedCommit:              resolvedCommit,
		State:                       string(target.State),
		IntendedWorktreeRoot:        nullableExecutionTargetString(target.IntendedWorktreeRoot),
		ProvisioningGeneration:      lifecycle.provisioningGeneration,
		SetupProvisioningGeneration: lifecycle.setupProvisioningGeneration,
		SetupState:                  lifecycle.setupState,
		ActiveClaimGeneration:       lifecycle.activeClaimGeneration,
		ActiveClaimPhase:            lifecycle.activeClaimPhase,
		RecoveryDisposition:         lifecycle.recoveryDisposition,
		RecoveryCause:               lifecycle.recoveryCause,
		ExactBranchObservation:      lifecycle.exactBranchObservation,
		LinkedWorktreeCommonDir:     lifecycle.linkedWorktreeCommonDir,
		LinkedWorktreeAdminEntry:    lifecycle.linkedWorktreeAdminEntry,
		LinkedWorktreeGitdir:        lifecycle.linkedWorktreeGitdir,
		LinkedWorktreeHeadRef:       lifecycle.linkedWorktreeHeadRef,
		ExpectedDetachmentCommit:    lifecycle.expectedDetachmentCommit,
	})
}

type executionTargetLifecycleFields struct {
	provisioningGeneration      sql.NullString
	setupProvisioningGeneration sql.NullString
	setupState                  string
	activeClaimGeneration       sql.NullString
	activeClaimPhase            sql.NullString
	recoveryDisposition         string
	recoveryCause               sql.NullString
	exactBranchObservation      sql.NullString
	linkedWorktreeCommonDir     sql.NullString
	linkedWorktreeAdminEntry    sql.NullString
	linkedWorktreeGitdir        sql.NullString
	linkedWorktreeHeadRef       sql.NullString
	expectedDetachmentCommit    sql.NullString
}

func executionTargetLifecycleFieldsFromTarget(target workflow.ExecutionTarget) executionTargetLifecycleFields {
	fields := executionTargetLifecycleFields{
		provisioningGeneration:      nullableExecutionTargetString(target.ProvisioningGeneration),
		setupProvisioningGeneration: nullableExecutionTargetString(target.SetupProvisioningGeneration),
		setupState:                  string(target.SetupState),
		recoveryDisposition:         string(target.RecoveryDisposition),
		exactBranchObservation:      nullableExecutionTargetString(target.ExactBranchObservation),
		expectedDetachmentCommit:    nullableExecutionTargetString(target.ExpectedDetachmentCommit),
	}
	if target.ActiveClaim != nil {
		fields.activeClaimGeneration = sql.NullString{String: target.ActiveClaim.Generation, Valid: true}
		fields.activeClaimPhase = sql.NullString{String: string(target.ActiveClaim.Phase), Valid: true}
	}
	if target.RecoveryCause != nil {
		fields.recoveryCause = sql.NullString{String: string(*target.RecoveryCause), Valid: true}
	}
	if target.LinkedWorktreeOwnership != nil {
		fields.linkedWorktreeCommonDir = sql.NullString{String: target.LinkedWorktreeOwnership.CommonDir, Valid: true}
		fields.linkedWorktreeAdminEntry = sql.NullString{String: target.LinkedWorktreeOwnership.AdminEntry, Valid: true}
		fields.linkedWorktreeGitdir = sql.NullString{String: target.LinkedWorktreeOwnership.GitDir, Valid: true}
		fields.linkedWorktreeHeadRef = sql.NullString{String: target.LinkedWorktreeOwnership.HeadRef, Valid: true}
	}
	return fields
}

func (s *Store) UpdateTaskExecutionTargetLifecycle(ctx context.Context, target workflow.ExecutionTarget, expectedClaim workflow.ExecutionTargetClaim) error {
	return updateTaskExecutionTargetLifecycle(ctx, s.queries, target, expectedClaim)
}

// FenceExecutionTargetRecovery performs the database-only startup fence for
// orphaned target materialization. It never performs Git, setup, or initiating
// action work. Each returned task has a recovery-queued claim that a later
// recovery owner may safely acquire.
func (s *Store) FenceExecutionTargetRecovery(ctx context.Context, limit int) ([]workflow.TaskID, error) {
	if limit <= 0 {
		return nil, errors.New("execution target recovery fence limit must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	rows, err := q.ListTaskExecutionTargetsNeedingRecoveryFence(ctx, int64(limit))
	if err != nil {
		return nil, err
	}
	taskIDs := make([]workflow.TaskID, 0, len(rows))
	for _, row := range rows {
		target, err := taskExecutionTargetFromRow(row)
		if err != nil {
			return nil, fmt.Errorf("decode task execution target for recovery fence: %w", err)
		}
		if target.ActiveClaim == nil {
			return nil, errors.New("recovery fence selected target without an active claim")
		}
		expectedClaim := *target.ActiveClaim
		changed := false
		switch expectedClaim.Phase {
		case workflow.ExecutionTargetClaimMaterializing, workflow.ExecutionTargetClaimRecovering:
			target.ActiveClaim = &workflow.ExecutionTargetClaim{
				Generation: uuid.NewString(),
				Phase:      workflow.ExecutionTargetClaimRecoveryQueued,
			}
			changed = true
		case workflow.ExecutionTargetClaimRecoveryQueued:
		default:
			return nil, fmt.Errorf("recovery fence selected target with unsupported claim phase %q", expectedClaim.Phase)
		}
		if target.SetupState == workflow.ExecutionTargetSetupRunning {
			target.SetupState = workflow.ExecutionTargetSetupFailed
			changed = true
		}
		if !changed {
			continue
		}
		if err := updateTaskExecutionTargetLifecycle(ctx, q, target, expectedClaim); err != nil {
			if errors.Is(err, ErrTaskExecutionTargetClaimChanged) {
				continue
			}
			return nil, err
		}
		taskIDs = append(taskIDs, target.TaskID)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return taskIDs, nil
}

// ListQueuedExecutionTargetRecoveries returns one bounded page of targets
// whose durable recovery claim is awaiting a recovery worker.
func (s *Store) ListQueuedExecutionTargetRecoveries(ctx context.Context, limit int) ([]workflow.ExecutionTarget, error) {
	if limit <= 0 {
		return nil, errors.New("queued execution target recovery limit must be positive")
	}
	rows, err := s.queries.ListQueuedExecutionTargetRecoveries(ctx, int64(limit))
	if err != nil {
		return nil, err
	}
	targets := make([]workflow.ExecutionTarget, 0, len(rows))
	for _, row := range rows {
		target, err := taskExecutionTargetFromRow(row)
		if err != nil {
			return nil, fmt.Errorf("decode queued execution target recovery: %w", err)
		}
		if target.ActiveClaim == nil || target.ActiveClaim.Phase != workflow.ExecutionTargetClaimRecoveryQueued {
			return nil, errors.New("queued execution target recovery query returned a non-queued target")
		}
		targets = append(targets, target)
	}
	return targets, nil
}

// ClaimQueuedExecutionTargetRecovery atomically moves one durable queued claim
// into recovering ownership. The supplied queue claim is an ABA fence.
func (s *Store) ClaimQueuedExecutionTargetRecovery(ctx context.Context, taskID workflow.TaskID, expectedClaim workflow.ExecutionTargetClaim) (workflow.ExecutionTarget, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return workflow.ExecutionTarget{}, errors.New("task id is required")
	}
	if err := expectedClaim.Validate(); err != nil {
		return workflow.ExecutionTarget{}, err
	}
	if expectedClaim.Phase != workflow.ExecutionTargetClaimRecoveryQueued {
		return workflow.ExecutionTarget{}, errors.New("execution target recovery claim must be queued")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workflow.ExecutionTarget{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	row, err := q.GetTaskExecutionTarget(ctx, string(taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.ExecutionTarget{}, ErrTaskExecutionTargetClaimChanged
	}
	if err != nil {
		return workflow.ExecutionTarget{}, err
	}
	target, err := taskExecutionTargetFromRow(row)
	if err != nil {
		return workflow.ExecutionTarget{}, fmt.Errorf("decode queued execution target recovery: %w", err)
	}
	if target.ActiveClaim == nil || *target.ActiveClaim != expectedClaim {
		return workflow.ExecutionTarget{}, ErrTaskExecutionTargetClaimChanged
	}
	target.ActiveClaim = &workflow.ExecutionTargetClaim{
		Generation: uuid.NewString(),
		Phase:      workflow.ExecutionTargetClaimRecovering,
	}
	if err := updateTaskExecutionTargetLifecycle(ctx, q, target, expectedClaim); err != nil {
		return workflow.ExecutionTarget{}, err
	}
	if err := tx.Commit(); err != nil {
		return workflow.ExecutionTarget{}, err
	}
	return target, nil
}

// RequeueExecutionTargetRecovery releases a recovery worker's claim back to
// durable queue ownership. Its recovering claim is an ABA fence, so a stopped
// worker cannot requeue a replacement owner.
func (s *Store) RequeueExecutionTargetRecovery(ctx context.Context, taskID workflow.TaskID, expectedClaim workflow.ExecutionTargetClaim) (workflow.ExecutionTarget, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return workflow.ExecutionTarget{}, errors.New("task id is required")
	}
	if err := expectedClaim.Validate(); err != nil {
		return workflow.ExecutionTarget{}, err
	}
	if expectedClaim.Phase != workflow.ExecutionTargetClaimRecovering {
		return workflow.ExecutionTarget{}, errors.New("execution target recovery claim must be recovering")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workflow.ExecutionTarget{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	row, err := q.GetTaskExecutionTarget(ctx, string(taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.ExecutionTarget{}, ErrTaskExecutionTargetClaimChanged
	}
	if err != nil {
		return workflow.ExecutionTarget{}, err
	}
	target, err := taskExecutionTargetFromRow(row)
	if err != nil {
		return workflow.ExecutionTarget{}, fmt.Errorf("decode recovering execution target: %w", err)
	}
	if target.ActiveClaim == nil || *target.ActiveClaim != expectedClaim {
		return workflow.ExecutionTarget{}, ErrTaskExecutionTargetClaimChanged
	}
	target.ActiveClaim = &workflow.ExecutionTargetClaim{
		Generation: uuid.NewString(),
		Phase:      workflow.ExecutionTargetClaimRecoveryQueued,
	}
	if err := updateTaskExecutionTargetLifecycle(ctx, q, target, expectedClaim); err != nil {
		return workflow.ExecutionTarget{}, err
	}
	if err := tx.Commit(); err != nil {
		return workflow.ExecutionTarget{}, err
	}
	return target, nil
}

// DeleteInitialExecutionTargetRecovery clears an unprovisioned recovery claim
// only when recovery has proven that neither the intended root nor task branch
// exists. The next initiating action may therefore resolve current policy.
func (s *Store) DeleteInitialExecutionTargetRecovery(ctx context.Context, taskID workflow.TaskID, expectedClaim workflow.ExecutionTargetClaim) error {
	if strings.TrimSpace(string(taskID)) == "" {
		return errors.New("task id is required")
	}
	if err := expectedClaim.Validate(); err != nil {
		return err
	}
	if expectedClaim.Phase != workflow.ExecutionTargetClaimRecovering {
		return errors.New("execution target recovery claim must be recovering")
	}
	deleted, err := s.queries.DeleteInitialTaskExecutionTargetRecovery(ctx, sqlitegen.DeleteInitialTaskExecutionTargetRecoveryParams{
		TaskID:                  string(taskID),
		ExpectedClaimGeneration: sql.NullString{String: expectedClaim.Generation, Valid: true},
	})
	if err != nil {
		return err
	}
	if deleted != 1 {
		return ErrTaskExecutionTargetClaimChanged
	}
	return nil
}

// MarkExecutionTargetManualRecovery records a target-local recovery failure
// and releases the worker claim. It preserves immutable target facts for an
// explicit operator recovery selection or manual repair.
func (s *Store) MarkExecutionTargetManualRecovery(ctx context.Context, target workflow.ExecutionTarget, expectedClaim workflow.ExecutionTargetClaim, cause workflow.ExecutionTargetRecoveryCause) error {
	if err := expectedClaim.Validate(); err != nil {
		return err
	}
	if expectedClaim.Phase != workflow.ExecutionTargetClaimRecovering {
		return errors.New("execution target recovery claim must be recovering")
	}
	if strings.TrimSpace(string(cause)) == "" {
		return errors.New("execution target manual recovery cause is required")
	}
	target.ActiveClaim = nil
	target.RecoveryDisposition = workflow.ExecutionTargetRecoveryManualRecovery
	target.RecoveryCause = &cause
	return s.UpdateTaskExecutionTargetLifecycle(ctx, target, expectedClaim)
}

func updateTaskExecutionTargetLifecycle(ctx context.Context, q *sqlitegen.Queries, target workflow.ExecutionTarget, expectedClaim workflow.ExecutionTargetClaim) error {
	if err := target.Validate(); err != nil {
		return err
	}
	if err := expectedClaim.Validate(); err != nil {
		return err
	}
	lifecycle := executionTargetLifecycleFieldsFromTarget(target)
	updated, err := q.UpdateTaskExecutionTargetLifecycle(ctx, sqlitegen.UpdateTaskExecutionTargetLifecycleParams{
		State:                       string(target.State),
		IntendedWorktreeRoot:        nullableExecutionTargetString(target.IntendedWorktreeRoot),
		ProvisioningGeneration:      lifecycle.provisioningGeneration,
		SetupProvisioningGeneration: lifecycle.setupProvisioningGeneration,
		SetupState:                  lifecycle.setupState,
		ActiveClaimGeneration:       lifecycle.activeClaimGeneration,
		ActiveClaimPhase:            lifecycle.activeClaimPhase,
		RecoveryDisposition:         lifecycle.recoveryDisposition,
		RecoveryCause:               lifecycle.recoveryCause,
		ExactBranchObservation:      lifecycle.exactBranchObservation,
		LinkedWorktreeCommonDir:     lifecycle.linkedWorktreeCommonDir,
		LinkedWorktreeAdminEntry:    lifecycle.linkedWorktreeAdminEntry,
		LinkedWorktreeGitdir:        lifecycle.linkedWorktreeGitdir,
		LinkedWorktreeHeadRef:       lifecycle.linkedWorktreeHeadRef,
		ExpectedDetachmentCommit:    lifecycle.expectedDetachmentCommit,
		TaskID:                      string(target.TaskID),
		ExpectedClaimGeneration:     sql.NullString{String: expectedClaim.Generation, Valid: true},
		ExpectedClaimPhase:          sql.NullString{String: string(expectedClaim.Phase), Valid: true},
	})
	if err != nil {
		return err
	}
	if updated != 1 {
		return ErrTaskExecutionTargetClaimChanged
	}
	return nil
}

type AttachManagedExecutionTargetWorktreeRequest struct {
	Target        workflow.ExecutionTarget
	ExpectedClaim workflow.ExecutionTargetClaim
	WorkspaceID   string
	WorktreeRoot  string
	CreatedBranch bool
}

func (s *Store) AttachManagedExecutionTargetWorktree(ctx context.Context, req AttachManagedExecutionTargetWorktreeRequest) (workflow.ExecutionWorktree, error) {
	if err := req.Target.Validate(); err != nil {
		return workflow.ExecutionWorktree{}, err
	}
	if req.Target.Policy == workflow.ExecutionPolicyNone {
		return workflow.ExecutionWorktree{}, errors.New("none execution target cannot attach a managed worktree")
	}
	if err := req.ExpectedClaim.Validate(); err != nil {
		return workflow.ExecutionWorktree{}, err
	}
	workspaceID := strings.TrimSpace(req.WorkspaceID)
	if workspaceID == "" {
		return workflow.ExecutionWorktree{}, errors.New("workspace id is required")
	}
	worktreeRoot, err := config.CanonicalWorkspaceRoot(req.WorktreeRoot)
	if err != nil {
		return workflow.ExecutionWorktree{}, err
	}
	now := s.now().UnixMilli()
	worktreeID := prefixedID("worktree")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workflow.ExecutionWorktree{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	task, err := q.GetTask(ctx, string(req.Target.TaskID))
	if err != nil {
		return workflow.ExecutionWorktree{}, err
	}
	if !task.SourceWorkspaceID.Valid || strings.TrimSpace(task.SourceWorkspaceID.String) != workspaceID {
		return workflow.ExecutionWorktree{}, errors.New("managed worktree workspace must match the task source workspace")
	}
	if err := q.UpsertWorktree(ctx, sqlitegen.UpsertWorktreeParams{
		ID:                worktreeID,
		WorkspaceID:       workspaceID,
		CanonicalRootPath: worktreeRoot,
		Managed:           1,
		CreatedBranch:     boolToInt64(req.CreatedBranch),
		OriginSessionID:   "",
		GitMetadataJson:   "{}",
		CreatedAtUnixMs:   now,
		UpdatedAtUnixMs:   now,
	}); err != nil {
		return workflow.ExecutionWorktree{}, err
	}
	if updated, err := q.UpdateTaskManagedWorktree(ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
		ID:                string(req.Target.TaskID),
		ManagedWorktreeID: nullableString(worktreeID),
		UpdatedAtUnixMs:   now,
	}); err != nil {
		return workflow.ExecutionWorktree{}, err
	} else if updated != 1 {
		return workflow.ExecutionWorktree{}, sql.ErrNoRows
	}
	if err := updateTaskExecutionTargetLifecycle(ctx, q, req.Target, req.ExpectedClaim); err != nil {
		return workflow.ExecutionWorktree{}, err
	}
	if err := tx.Commit(); err != nil {
		return workflow.ExecutionWorktree{}, err
	}
	return workflow.ExecutionWorktree{ID: worktreeID, Root: worktreeRoot}, nil
}

func boolToInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
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

func (s *Store) ExecutionTargetRecoveryContext(ctx context.Context, taskID workflow.TaskID) (ExecutionTargetRecoveryContext, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return ExecutionTargetRecoveryContext{}, errors.New("task id is required")
	}
	task, err := s.queries.GetTask(ctx, string(taskID))
	if err != nil {
		return ExecutionTargetRecoveryContext{}, err
	}
	target, err := s.GetTaskExecutionTarget(ctx, taskID)
	if err != nil {
		return ExecutionTargetRecoveryContext{}, err
	}
	if target == nil {
		return ExecutionTargetRecoveryContext{}, ErrTaskExecutionTargetClaimChanged
	}
	workspaceID := strings.TrimSpace(task.SourceWorkspaceID.String)
	if workspaceID == "" {
		return ExecutionTargetRecoveryContext{}, errors.New("task source workspace is required for execution target recovery")
	}
	workspace, err := s.metadata.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		return ExecutionTargetRecoveryContext{}, err
	}
	return ExecutionTargetRecoveryContext{
		Target:          *target,
		TaskShortID:     task.ShortID,
		ProjectID:       task.ProjectID,
		SourceWorkspace: workflow.ExecutionWorkspace{ID: workspace.ID, Root: workspace.CanonicalRootPath},
	}, nil
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
		IntendedWorktreeRoot:        executionTargetOptionalString(row.IntendedWorktreeRoot),
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
