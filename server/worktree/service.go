package worktree

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/session"
	shelltool "core/server/tools/shell"
	"core/server/workflow"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"github.com/google/uuid"
)

const setupDiagnosticLimitBytes = 16 * 1024

const rollbackSessionTargetTimeout = 5 * time.Second

type runtimeController interface {
	SyncExecutionTarget(ctx context.Context, sessionID string, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error
	ClearWorktreeReminder(ctx context.Context, sessionID string) error
	HasBlockingRuntimeActivity(ctx context.Context, sessionID string) (bool, error)
}

type activeRuntimeSource interface {
	IsSessionRuntimeActive(sessionID string) bool
	BlockSessionRuns(sessionIDs []string) func()
}

type processSource interface {
	List() []shelltool.Snapshot
}

type localEntryAppender interface {
	AppendCommittedEntry(ctx context.Context, req serverapi.RuntimeAppendCommittedEntryRequest) error
	AppendSessionEntry(ctx context.Context, sessionID string, role string, text string) error
}

type ServiceOptions struct {
	BaseDir             string
	SetupScript         string
	SetupTimeoutSeconds int
}

type Service struct {
	metadata            *metadata.Store
	git                 *GitInspector
	runtime             runtimeController
	active              activeRuntimeSource
	processes           processSource
	localNotes          localEntryAppender
	baseDir             string
	setupScript         string
	setupTimeoutSeconds int
	setupBroker         *setupEventBroker

	workspaceMu    sync.Mutex
	workspaceLocks map[string]*workspaceMutationLock
}

type workspaceMutationLock struct {
	mu   sync.Mutex
	refs int
}

type syncedWorktree struct {
	record metadata.WorktreeRecord
	git    GitWorktree
}

type sessionWorkspaceContext struct {
	target        clientui.SessionExecutionTarget
	projectID     string
	workspaceID   string
	workspaceRoot string
	sessionID     string
}

type failedCreateCleanup struct {
	active        bool
	workspaceID   string
	workspaceRoot string
	worktreeRoot  string
	worktreeID    string
	branchName    string
	createdBranch bool
}

type setupScriptPayload struct {
	SourceWorkspaceRoot string  `json:"source_workspace_root"`
	BranchName          string  `json:"branch_name"`
	WorktreeRoot        string  `json:"worktree_root"`
	SessionID           *string `json:"session_id"`
	ProjectID           string  `json:"project_id"`
	WorkspaceID         string  `json:"workspace_id"`
	WorktreeID          string  `json:"worktree_id"`
	CreatedBranch       bool    `json:"created_branch"`
}

func normalizeSetupSessionID(sessionID *string) (*string, error) {
	if sessionID == nil {
		return nil, nil
	}
	normalized := strings.TrimSpace(*sessionID)
	if normalized == "" {
		return nil, errors.New("setup session_id must be non-empty when present")
	}
	return &normalized, nil
}

type InitialTaskWorktreeMaterializationRequest struct {
	TaskID           workflow.TaskID
	SetupOperationID serverapi.WorktreeSetupOperationID
	ResolvedTarget   GitRevision
}

type LockedTaskWorktreeRestoreRequest struct {
	TaskID           workflow.TaskID
	SetupOperationID serverapi.WorktreeSetupOperationID
}

type LockedTaskWorktreeCause string

const (
	LockedTaskWorktreeCauseDetachedHead     LockedTaskWorktreeCause = "detached_head"
	LockedTaskWorktreeCauseInvalidRoot      LockedTaskWorktreeCause = "invalid_root"
	LockedTaskWorktreeCauseRootInaccessible LockedTaskWorktreeCause = "root_inaccessible"
	LockedTaskWorktreeCauseMissingBranch    LockedTaskWorktreeCause = "missing_branch"
	LockedTaskWorktreeCauseConflict         LockedTaskWorktreeCause = "conflict"
	LockedTaskWorktreeCauseGitFailure       LockedTaskWorktreeCause = "git_failure"
)

type LockedTaskWorktreeError struct {
	Cause LockedTaskWorktreeCause
	Err   error
}

func (e *LockedTaskWorktreeError) Error() string {
	if e == nil {
		return "locked task worktree is unavailable"
	}
	return "locked task worktree is unavailable: " + string(e.Cause)
}

func (e *LockedTaskWorktreeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type TaskWorktreeMaterialization struct {
	Worktree      serverapi.WorktreeView
	Created       bool
	CreatedBranch bool
}

type TaskWorktreeBaseCommitMismatchError struct {
	WorktreeID            string
	RequestedCommitOID    string
	CreationBaseCommitOID *string
}

func (e *TaskWorktreeBaseCommitMismatchError) Error() string {
	if e == nil {
		return "task worktree creation base commit is incompatible"
	}
	if e.CreationBaseCommitOID == nil {
		return fmt.Sprintf("task worktree %q has no immutable creation base commit", e.WorktreeID)
	}
	return fmt.Sprintf("task worktree %q was created from %q, not requested commit %q", e.WorktreeID, *e.CreationBaseCommitOID, e.RequestedCommitOID)
}

type DeleteTaskWorktreeRequest struct {
	TaskID string
}

type DeleteTaskWorktreeResponse struct {
	Deleted       bool
	WorktreeID    string
	BranchDeleted bool
}

func NewService(metadataStore *metadata.Store, gitInspector *GitInspector, active activeRuntimeSource, runtime runtimeController, processes processSource, localNotes localEntryAppender, opts ServiceOptions) *Service {
	if gitInspector == nil {
		gitInspector = NewGitInspector(nil)
	}
	if active == nil {
		if source, ok := runtime.(activeRuntimeSource); ok {
			active = source
		}
	}
	return &Service{
		metadata:            metadataStore,
		git:                 gitInspector,
		runtime:             runtime,
		active:              active,
		processes:           processes,
		localNotes:          localNotes,
		baseDir:             strings.TrimSpace(opts.BaseDir),
		setupScript:         strings.TrimSpace(opts.SetupScript),
		setupTimeoutSeconds: opts.SetupTimeoutSeconds,
		setupBroker:         newSetupEventBroker(),
		workspaceLocks:      make(map[string]*workspaceMutationLock),
	}
}

func (s *Service) MaterializeInitialTaskWorktree(ctx context.Context, req InitialTaskWorktreeMaterializationRequest) (TaskWorktreeMaterialization, error) {
	resolvedTarget, err := validateResolvedTaskWorktreeTarget(req.ResolvedTarget)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	return s.materializeInitialTaskWorktree(ctx, strings.TrimSpace(string(req.TaskID)), req.SetupOperationID, resolvedTarget)
}

func (s *Service) materializeInitialTaskWorktree(ctx context.Context, taskID string, setupOperationID serverapi.WorktreeSetupOperationID, resolvedTarget GitRevision) (TaskWorktreeMaterialization, error) {
	if s == nil || s.metadata == nil || s.git == nil {
		return TaskWorktreeMaterialization{}, errors.New("worktree service dependencies are required")
	}
	if taskID == "" {
		return TaskWorktreeMaterialization{}, errors.New("task_id is required")
	}
	task, err := s.metadata.Queries().GetTask(ctx, taskID)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	if task.ExecutionTargetMode.Valid {
		return TaskWorktreeMaterialization{}, errors.New("initial task worktree materialization requires an unlocked task")
	}
	workspace, err := s.taskSourceWorkspace(ctx, task.ProjectID, task.SourceWorkspaceID.String)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	release := s.acquireWorkspaceMutationLock(workspace.WorkspaceID)
	defer release()
	task, err = s.metadata.Queries().GetTask(ctx, taskID)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	if task.ExecutionTargetMode.Valid {
		return TaskWorktreeMaterialization{}, errors.New("initial task worktree materialization requires an unlocked task")
	}
	var existingRecord *metadata.WorktreeRecord
	if task.ManagedWorktreeID.Valid && strings.TrimSpace(task.ManagedWorktreeID.String) != "" {
		record, err := s.metadata.GetWorktreeRecordByID(ctx, strings.TrimSpace(task.ManagedWorktreeID.String))
		if err != nil {
			return TaskWorktreeMaterialization{}, err
		}
		existingRecord = &record
		if !sameCreationBaseCommit(record.CreationBaseCommitOID, resolvedTarget.CommitOID) {
			return TaskWorktreeMaterialization{}, &TaskWorktreeBaseCommitMismatchError{
				WorktreeID:            record.ID,
				RequestedCommitOID:    resolvedTarget.CommitOID,
				CreationBaseCommitOID: record.CreationBaseCommitOID,
			}
		}
		identity, identityErr := s.git.ValidateManagedWorktreeIdentity(ctx, ManagedWorktreeIdentitySpec{
			SourceWorkspaceRoot:  workspace.RootPath,
			ExpectedWorktreeRoot: record.CanonicalRoot,
		})
		if identityErr == nil {
			reused, err := s.rebindHealthyManagedTaskWorktree(ctx, task, workspace, record, identity)
			if err != nil {
				return TaskWorktreeMaterialization{}, err
			}
			return reused, nil
		}
		var typedIdentityErr *ManagedWorktreeIdentityError
		if !errors.As(identityErr, &typedIdentityErr) || typedIdentityErr.Kind != ManagedWorktreeIdentityErrorRootMissing {
			return TaskWorktreeMaterialization{}, identityErr
		}
	}
	createSpec := CreateSpec{BaseRef: resolvedTarget.CommitOID, CreateBranch: true, BranchName: task.ShortID}
	resolution, err := s.git.ResolveCreateTarget(ctx, workspace.RootPath, task.ShortID)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	if resolution.Kind != CreateTargetResolutionKindNewBranch {
		if existingRecord != nil && resolution.Kind == CreateTargetResolutionKindExistingBranch {
			createSpec = CreateSpec{BaseRef: task.ShortID}
		} else {
			return TaskWorktreeMaterialization{}, &TaskBranchCollisionError{BranchName: task.ShortID, ResolvedRef: resolution.ResolvedRef}
		}
	}
	creationBaseOID := resolvedTarget.CommitOID
	materialized, err := s.createManagedTaskWorktree(ctx, managedTaskWorktreeCreationRequest{
		Task:             task,
		Workspace:        workspace,
		CreateSpec:       createSpec,
		SetupOperationID: setupOperationID,
		ExistingRecord:   existingRecord,
		CreationBaseOID:  &creationBaseOID,
	})
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	return materialized, nil
}

func (s *Service) RestoreLockedTaskWorktree(ctx context.Context, req LockedTaskWorktreeRestoreRequest) (TaskWorktreeMaterialization, error) {
	if s == nil || s.metadata == nil || s.git == nil {
		return TaskWorktreeMaterialization{}, errors.New("worktree service dependencies are required")
	}
	taskID := strings.TrimSpace(string(req.TaskID))
	if taskID == "" {
		return TaskWorktreeMaterialization{}, errors.New("task_id is required")
	}
	task, err := s.metadata.Queries().GetTask(ctx, taskID)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	if !task.ExecutionTargetMode.Valid || workflow.ExecutionTargetMode(task.ExecutionTargetMode.String) == workflow.ExecutionTargetModeNone {
		return TaskWorktreeMaterialization{}, errors.New("task does not have a locked managed execution target")
	}
	workspace, err := s.taskSourceWorkspace(ctx, task.ProjectID, task.SourceWorkspaceID.String)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	release := s.acquireWorkspaceMutationLock(workspace.WorkspaceID)
	defer release()
	task, err = s.metadata.Queries().GetTask(ctx, taskID)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	if !task.ManagedWorktreeID.Valid || strings.TrimSpace(task.ManagedWorktreeID.String) == "" {
		return s.restoreUnboundLockedTaskWorktree(ctx, req, task, workspace)
	}
	worktreeID := strings.TrimSpace(task.ManagedWorktreeID.String)
	record, err := s.metadata.GetWorktreeRecordByID(ctx, worktreeID)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	identity, err := s.git.ValidateManagedWorktreeIdentity(ctx, ManagedWorktreeIdentitySpec{
		SourceWorkspaceRoot:  workspace.RootPath,
		ExpectedWorktreeRoot: record.CanonicalRoot,
	})
	if err != nil {
		var identityErr *ManagedWorktreeIdentityError
		if errors.As(err, &identityErr) && identityErr.Kind == ManagedWorktreeIdentityErrorRootMissing {
			return s.restoreMissingLockedTaskWorktree(ctx, req, task, workspace, record)
		}
		return TaskWorktreeMaterialization{}, lockedTaskWorktreeIdentityError(err)
	}
	return s.rebindHealthyManagedTaskWorktree(ctx, task, workspace, record, identity)
}

func (s *Service) restoreUnboundLockedTaskWorktree(ctx context.Context, req LockedTaskWorktreeRestoreRequest, task sqlitegen.TaskRecord, workspace taskSourceWorkspace) (TaskWorktreeMaterialization, error) {
	expectedRoot, err := defaultWorktreeRoot(s.baseDir, workspace.WorkspaceID, task.ShortID)
	if err != nil {
		return TaskWorktreeMaterialization{}, &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseGitFailure, Err: err}
	}
	info, err := os.Stat(expectedRoot)
	if err == nil {
		if !info.IsDir() {
			return TaskWorktreeMaterialization{}, &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseInvalidRoot}
		}
		record, recordErr := s.metadata.GetWorktreeRecordByCanonicalRoot(ctx, expectedRoot)
		if errors.Is(recordErr, sql.ErrNoRows) {
			record = metadata.WorktreeRecord{
				ID:            "worktree-" + uuid.NewString(),
				WorkspaceID:   workspace.WorkspaceID,
				CanonicalRoot: expectedRoot,
				Managed:       true,
			}
		} else if recordErr != nil {
			return TaskWorktreeMaterialization{}, recordErr
		}
		if strings.TrimSpace(record.WorkspaceID) != workspace.WorkspaceID || !record.Managed {
			return TaskWorktreeMaterialization{}, &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseConflict}
		}
		identity, identityErr := s.git.ValidateManagedWorktreeIdentity(ctx, ManagedWorktreeIdentitySpec{
			SourceWorkspaceRoot:  workspace.RootPath,
			ExpectedWorktreeRoot: expectedRoot,
		})
		if identityErr != nil {
			return TaskWorktreeMaterialization{}, lockedTaskWorktreeIdentityError(identityErr)
		}
		return s.rebindHealthyManagedTaskWorktree(ctx, task, workspace, record, identity)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return TaskWorktreeMaterialization{}, &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseRootInaccessible, Err: err}
	}
	record, recordErr := s.metadata.GetWorktreeRecordByCanonicalRoot(ctx, expectedRoot)
	if errors.Is(recordErr, sql.ErrNoRows) {
		return TaskWorktreeMaterialization{}, &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseMissingBranch}
	}
	if recordErr != nil {
		return TaskWorktreeMaterialization{}, recordErr
	}
	if strings.TrimSpace(record.WorkspaceID) != workspace.WorkspaceID || !record.Managed {
		return TaskWorktreeMaterialization{}, &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseConflict}
	}
	return s.restoreMissingLockedTaskWorktree(ctx, req, task, workspace, record)
}

func (s *Service) rebindHealthyManagedTaskWorktree(ctx context.Context, task sqlitegen.TaskRecord, workspace taskSourceWorkspace, record metadata.WorktreeRecord, identity ManagedWorktreeIdentity) (TaskWorktreeMaterialization, error) {
	revision, err := s.git.ResolveHEAD(ctx, record.CanonicalRoot)
	if err != nil {
		return TaskWorktreeMaterialization{}, &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseGitFailure, Err: err}
	}
	branchRef := strings.TrimSpace(identity.SymbolicHead)
	branchName, ok := identity.NamedBranch()
	if !ok {
		return TaskWorktreeMaterialization{}, &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseDetachedHead}
	}
	gitMetadata := GitWorktree{
		Root:       record.CanonicalRoot,
		HeadOID:    revision.CommitOID,
		BranchRef:  branchRef,
		BranchName: branchName,
	}
	record.GitMetadataJSON, err = marshalGitMetadata(gitMetadata)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	record.UpdatedAt = time.Now().UTC()
	record.WorkspaceID = workspace.WorkspaceID
	record.Managed = true
	if err := s.metadata.UpsertWorktreeRecord(ctx, record); err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	updated, err := s.metadata.Queries().UpdateTaskManagedWorktree(ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
		ID:                task.ID,
		ManagedWorktreeID: sql.NullString{String: record.ID, Valid: true},
		UpdatedAtUnixMs:   record.UpdatedAt.UnixMilli(),
	})
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	if updated != 1 {
		return TaskWorktreeMaterialization{}, sql.ErrNoRows
	}
	view, err := worktreeViewFromSynced(syncedWorktree{record: record, git: gitMetadata}, clientui.SessionExecutionTarget{})
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	return TaskWorktreeMaterialization{Worktree: view}, nil
}

type managedTaskWorktreeCreationRequest struct {
	Task             sqlitegen.TaskRecord
	Workspace        taskSourceWorkspace
	CreateSpec       CreateSpec
	RequestedRoot    *string
	SetupOperationID serverapi.WorktreeSetupOperationID
	ExistingRecord   *metadata.WorktreeRecord
	CreationBaseOID  *string
}

func (s *Service) restoreMissingLockedTaskWorktree(ctx context.Context, req LockedTaskWorktreeRestoreRequest, task sqlitegen.TaskRecord, workspace taskSourceWorkspace, record metadata.WorktreeRecord) (TaskWorktreeMaterialization, error) {
	gitMetadata, err := worktreeGitMetadataFromRecord(record)
	if err != nil {
		return TaskWorktreeMaterialization{}, &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseInvalidRoot, Err: err}
	}
	branchName := strings.TrimSpace(gitMetadata.BranchName)
	if gitMetadata.Detached || branchName == "" {
		return TaskWorktreeMaterialization{}, &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseMissingBranch}
	}
	exists, err := s.git.BranchExists(ctx, workspace.RootPath, branchName)
	if err != nil {
		return TaskWorktreeMaterialization{}, &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseGitFailure, Err: err}
	}
	if !exists {
		return TaskWorktreeMaterialization{}, &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseMissingBranch}
	}
	registered, err := s.registeredWorktreeRoot(ctx, workspace.RootPath, record.CanonicalRoot)
	if err != nil {
		return TaskWorktreeMaterialization{}, &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseGitFailure, Err: err}
	}
	if registered {
		return TaskWorktreeMaterialization{}, &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseConflict}
	}
	materialized, err := s.createManagedTaskWorktree(ctx, managedTaskWorktreeCreationRequest{
		Task:             task,
		Workspace:        workspace,
		CreateSpec:       CreateSpec{BaseRef: branchName},
		RequestedRoot:    &record.CanonicalRoot,
		SetupOperationID: req.SetupOperationID,
		ExistingRecord:   &record,
		CreationBaseOID:  record.CreationBaseCommitOID,
	})
	if err != nil {
		if errors.Is(err, ErrWorktreeRootCollisionCap) {
			return TaskWorktreeMaterialization{}, &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseConflict, Err: err}
		}
		return TaskWorktreeMaterialization{}, err
	}
	return materialized, nil
}

func (s *Service) registeredWorktreeRoot(ctx context.Context, workspaceRoot string, root string) (bool, error) {
	canonicalRoot, err := config.CanonicalWorkspaceRoot(root)
	if err != nil {
		return false, err
	}
	worktrees, err := s.git.List(ctx, workspaceRoot)
	if err != nil {
		return false, err
	}
	for _, worktree := range worktrees {
		if worktree.Root == canonicalRoot {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) createManagedTaskWorktree(ctx context.Context, req managedTaskWorktreeCreationRequest) (resp TaskWorktreeMaterialization, err error) {
	createSpec, err := normalizeCreateSpec(req.CreateSpec)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	var worktreeRoot string
	if req.RequestedRoot == nil {
		worktreeRoot, err = s.resolveRequestedWorktreeRoot("", req.Workspace.WorkspaceID, createSpec)
		if err != nil {
			return TaskWorktreeMaterialization{}, err
		}
	} else {
		requestedRoot := strings.TrimSpace(*req.RequestedRoot)
		if requestedRoot == "" {
			return TaskWorktreeMaterialization{}, errors.New("requested managed worktree root is required")
		}
		worktreeRoot, err = config.CanonicalWorkspaceRoot(requestedRoot)
		if err != nil {
			return TaskWorktreeMaterialization{}, err
		}
	}
	cleanup := failedCreateCleanup{
		workspaceID:   req.Workspace.WorkspaceID,
		workspaceRoot: req.Workspace.RootPath,
		worktreeRoot:  worktreeRoot,
		branchName:    createSpec.BranchName,
		createdBranch: createSpec.CreateBranch,
	}
	defer func() {
		if err == nil || !cleanup.active {
			return
		}
		if cleanupErr := s.cleanupFailedCreate(ctx, cleanup); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()
	createdBranch, err := s.git.Add(ctx, req.Workspace.RootPath, worktreeRoot, createSpec)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	cleanup.active = true
	cleanup.createdBranch = createdBranch
	worktreeRoot, err = config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	cleanup.worktreeRoot = worktreeRoot
	identity, err := s.git.ValidateManagedWorktreeIdentity(ctx, ManagedWorktreeIdentitySpec{
		SourceWorkspaceRoot:  req.Workspace.RootPath,
		ExpectedWorktreeRoot: worktreeRoot,
	})
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	revision, err := s.git.ResolveHEAD(ctx, worktreeRoot)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	branchRef := strings.TrimSpace(identity.SymbolicHead)
	branchName, ok := identity.NamedBranch()
	if !ok {
		return TaskWorktreeMaterialization{}, errors.New("created managed worktree does not have a named branch")
	}
	gitEntry := GitWorktree{
		Root:       worktreeRoot,
		HeadOID:    revision.CommitOID,
		BranchRef:  branchRef,
		BranchName: branchName,
	}
	gitMetadataJSON, err := marshalGitMetadata(gitEntry)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	now := time.Now().UTC()
	record := metadata.WorktreeRecord{
		ID:                    "worktree-" + uuid.NewString(),
		WorkspaceID:           req.Workspace.WorkspaceID,
		CanonicalRoot:         worktreeRoot,
		Managed:               true,
		CreatedBranch:         createdBranch,
		GitMetadataJSON:       gitMetadataJSON,
		CreationBaseCommitOID: req.CreationBaseOID,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if req.ExistingRecord != nil {
		record.ID = req.ExistingRecord.ID
		record.CreatedBranch = req.ExistingRecord.CreatedBranch || createdBranch
		record.OriginSessionID = req.ExistingRecord.OriginSessionID
		record.CreatedAt = req.ExistingRecord.CreatedAt
	}
	cleanup.worktreeID = record.ID
	if err := s.metadata.UpsertWorktreeRecord(ctx, record); err != nil {
		return TaskWorktreeMaterialization{}, fmt.Errorf("persist managed task worktree: %w", err)
	}
	record, err = s.metadata.GetWorktreeRecordByCanonicalRoot(ctx, worktreeRoot)
	if err != nil {
		return TaskWorktreeMaterialization{}, fmt.Errorf("reload persisted managed task worktree: %w", err)
	}
	cleanup.worktreeID = record.ID
	updated, err := s.metadata.Queries().UpdateTaskManagedWorktree(ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
		ID:                req.Task.ID,
		ManagedWorktreeID: sql.NullString{String: record.ID, Valid: true},
		UpdatedAtUnixMs:   now.UnixMilli(),
	})
	if err != nil {
		return TaskWorktreeMaterialization{}, fmt.Errorf(
			"bind managed worktree %q (workspace %q) to task %q (source workspace %q): %w",
			record.ID,
			record.WorkspaceID,
			req.Task.ID,
			req.Task.SourceWorkspaceID.String,
			err,
		)
	}
	if updated != 1 {
		return TaskWorktreeMaterialization{}, sql.ErrNoRows
	}
	cleanup.active = false
	if err := s.runSetupForWorktree(ctx, setupExecutionRequest{
		SetupOperationID:    req.SetupOperationID,
		SourceWorkspaceRoot: req.Workspace.RootPath,
		BranchName:          branchName,
		WorktreeRoot:        worktreeRoot,
		ScriptPayload: setupScriptPayload{
			ProjectID:   req.Task.ProjectID,
			WorkspaceID: req.Workspace.WorkspaceID,
			WorktreeID:  record.ID,
		},
		CreatedBranch: createdBranch,
	}); err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	view, err := worktreeViewFromSynced(syncedWorktree{record: record, git: gitEntry}, clientui.SessionExecutionTarget{})
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	return TaskWorktreeMaterialization{
		Worktree:      view,
		Created:       true,
		CreatedBranch: createdBranch,
	}, nil
}

func lockedTaskWorktreeIdentityError(err error) error {
	var identityErr *ManagedWorktreeIdentityError
	if !errors.As(err, &identityErr) {
		return &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseGitFailure, Err: err}
	}
	cause := LockedTaskWorktreeCauseInvalidRoot
	switch identityErr.Kind {
	case ManagedWorktreeIdentityErrorDetachedHead:
		cause = LockedTaskWorktreeCauseDetachedHead
	case ManagedWorktreeIdentityErrorRootInaccessible:
		cause = LockedTaskWorktreeCauseRootInaccessible
	case ManagedWorktreeIdentityErrorGitInspectionFailed:
		cause = LockedTaskWorktreeCauseGitFailure
	}
	return &LockedTaskWorktreeError{Cause: cause, Err: err}
}

func validateResolvedTaskWorktreeTarget(resolvedTarget GitRevision) (GitRevision, error) {
	requestedRef := strings.TrimSpace(resolvedTarget.RequestedRef)
	commitOID := strings.TrimSpace(resolvedTarget.CommitOID)
	if requestedRef == "" {
		return GitRevision{}, errors.New("resolved task worktree target requested ref is required")
	}
	if commitOID == "" {
		return GitRevision{}, errors.New("resolved task worktree target commit oid is required")
	}
	var canonicalRef *string
	if resolvedTarget.CanonicalRef != nil {
		value := strings.TrimSpace(*resolvedTarget.CanonicalRef)
		if value == "" {
			return GitRevision{}, errors.New("resolved task worktree target canonical ref is blank")
		}
		canonicalRef = &value
	}
	return GitRevision{
		RequestedRef: requestedRef,
		CommitOID:    commitOID,
		CanonicalRef: canonicalRef,
	}, nil
}

func sameCreationBaseCommit(creationBaseCommitOID *string, requestedCommitOID string) bool {
	return creationBaseCommitOID != nil && strings.TrimSpace(*creationBaseCommitOID) == strings.TrimSpace(requestedCommitOID)
}

func (s *Service) DeleteTaskWorktree(ctx context.Context, req DeleteTaskWorktreeRequest) (DeleteTaskWorktreeResponse, error) {
	if s == nil || s.metadata == nil || s.git == nil {
		return DeleteTaskWorktreeResponse{}, errors.New("worktree service dependencies are required")
	}
	taskID := strings.TrimSpace(req.TaskID)
	if taskID == "" {
		return DeleteTaskWorktreeResponse{}, errors.New("task_id is required")
	}
	task, err := s.metadata.Queries().GetTask(ctx, taskID)
	if err != nil {
		return DeleteTaskWorktreeResponse{}, err
	}
	worktreeID := strings.TrimSpace(task.ManagedWorktreeID.String)
	if !task.ManagedWorktreeID.Valid || worktreeID == "" {
		return DeleteTaskWorktreeResponse{}, nil
	}
	record, err := s.metadata.GetWorktreeRecordByID(ctx, worktreeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DeleteTaskWorktreeResponse{}, nil
		}
		return DeleteTaskWorktreeResponse{}, err
	}
	workspace, err := s.metadata.GetWorkspaceByID(ctx, record.WorkspaceID)
	if err != nil {
		return DeleteTaskWorktreeResponse{}, err
	}
	workspaceRoot := strings.TrimSpace(workspace.CanonicalRootPath)
	if workspaceRoot == "" {
		return DeleteTaskWorktreeResponse{}, fmt.Errorf("workspace %q has no root path", strings.TrimSpace(record.WorkspaceID))
	}
	release := s.acquireWorkspaceMutationLock(record.WorkspaceID)
	defer release()
	task, err = s.metadata.Queries().GetTask(ctx, taskID)
	if err != nil {
		return DeleteTaskWorktreeResponse{}, err
	}
	if !task.ManagedWorktreeID.Valid || strings.TrimSpace(task.ManagedWorktreeID.String) != worktreeID {
		return DeleteTaskWorktreeResponse{}, nil
	}
	record, err = s.metadata.GetWorktreeRecordByID(ctx, worktreeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DeleteTaskWorktreeResponse{}, nil
		}
		return DeleteTaskWorktreeResponse{}, err
	}
	if record.IsMain {
		return DeleteTaskWorktreeResponse{}, fmt.Errorf("cannot delete main workspace worktree: %w", serverapi.ErrWorktreeBlocked)
	}
	releaseDeletionSessionLeases, err := s.ensureTaskWorktreeDeletionUnblocked(ctx, taskID, record)
	if err != nil {
		return DeleteTaskWorktreeResponse{}, err
	}
	defer releaseDeletionSessionLeases()
	if err := s.git.Prune(ctx, workspaceRoot); err != nil {
		return DeleteTaskWorktreeResponse{}, err
	}
	synced, err := s.syncWorkspace(ctx, record.WorkspaceID, workspaceRoot, false)
	if err != nil {
		return DeleteTaskWorktreeResponse{}, err
	}
	target, found := findSyncedWorktreeByID(synced, worktreeID)
	if found {
		if err := s.retargetActiveSessionsFromDeletedWorktree(ctx, record.WorkspaceID, workspaceRoot, target.record, ""); err != nil {
			return DeleteTaskWorktreeResponse{}, err
		}
		dirtyCount, dirtyErr := s.git.DirtyFileCount(ctx, target.record.CanonicalRoot)
		force := dirtyCount > 0 || dirtyErr != nil
		if err := s.git.Remove(ctx, workspaceRoot, target.record.CanonicalRoot, force); err != nil {
			return DeleteTaskWorktreeResponse{}, err
		}
	} else if err := s.retargetActiveSessionsFromDeletedWorktree(ctx, record.WorkspaceID, workspaceRoot, record, ""); err != nil {
		return DeleteTaskWorktreeResponse{}, err
	}
	// The worktree itself is already removed by this point, so a branch-cleanup
	// failure must not abort the remaining metadata cleanup; otherwise the record
	// is left pointing at a removed worktree. Treat branch deletion as best-effort
	// and report the outcome via BranchDeleted.
	branchDeleted, branchErr := s.deleteTaskWorktreeBranch(ctx, workspaceRoot, record, target, found)
	if branchErr != nil {
		branchDeleted = false
	}
	if _, err := s.syncWorkspace(ctx, record.WorkspaceID, workspaceRoot, false); err != nil {
		return DeleteTaskWorktreeResponse{}, err
	}
	if err := s.metadata.DeleteWorktreeRecordByID(ctx, worktreeID); err != nil {
		return DeleteTaskWorktreeResponse{}, err
	}
	return DeleteTaskWorktreeResponse{Deleted: true, WorktreeID: worktreeID, BranchDeleted: branchDeleted}, nil
}

// EnsureTaskWorktreeDeletable preflights the blockers that canceling a task's own
// runs cannot clear (another non-terminal task sharing the managed worktree), so
// callers can refuse a delete before interrupting automation. It is read-only and
// acquires no locks; DeleteTaskWorktree remains the authoritative, locked check.
// A task with no managed worktree (or a missing record) is reported as deletable.
func (s *Service) EnsureTaskWorktreeDeletable(ctx context.Context, taskID string) error {
	if s == nil || s.metadata == nil {
		return errors.New("worktree service dependencies are required")
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return errors.New("task_id is required")
	}
	task, err := s.metadata.Queries().GetTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	worktreeID := strings.TrimSpace(task.ManagedWorktreeID.String)
	if !task.ManagedWorktreeID.Valid || worktreeID == "" {
		return nil
	}
	record, err := s.metadata.GetWorktreeRecordByID(ctx, worktreeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if record.IsMain {
		return fmt.Errorf("cannot delete main workspace worktree: %w", serverapi.ErrWorktreeBlocked)
	}
	return s.ensureNoOtherNonTerminalTasksManageWorktree(ctx, taskID, record)
}

func (s *Service) ensureNoOtherNonTerminalTasksManageWorktree(ctx context.Context, taskID string, record metadata.WorktreeRecord) error {
	otherNonTerminalTasks, err := s.metadata.Queries().CountOtherNonTerminalTasksByManagedWorktree(ctx, sqlitegen.CountOtherNonTerminalTasksByManagedWorktreeParams{
		ManagedWorktreeID: sql.NullString{String: strings.TrimSpace(record.ID), Valid: strings.TrimSpace(record.ID) != ""},
		TaskID:            strings.TrimSpace(taskID),
	})
	if err != nil {
		return err
	}
	if otherNonTerminalTasks > 0 {
		return errors.Join(serverapi.ErrWorktreeBlocked, fmt.Errorf("worktree is still managed by %d other non-terminal workflow task(s)", otherNonTerminalTasks))
	}
	return nil
}

func (s *Service) ensureTaskWorktreeDeletionUnblocked(ctx context.Context, taskID string, record metadata.WorktreeRecord) (func(), error) {
	if err := s.ensureNoOtherNonTerminalTasksManageWorktree(ctx, taskID, record); err != nil {
		return func() {}, err
	}
	return s.ensureDeletionSessionAndProcessUnblocked(ctx, "", record.ID, record.CanonicalRoot)
}

func (s *Service) deleteTaskWorktreeBranch(ctx context.Context, workspaceRoot string, record metadata.WorktreeRecord, target syncedWorktree, found bool) (bool, error) {
	if !record.Managed || !record.CreatedBranch {
		return false, nil
	}
	branchName := ""
	if found {
		branchName = strings.TrimSpace(target.git.BranchName)
	}
	if branchName == "" {
		gitMetadata, err := worktreeGitMetadataFromRecord(record)
		if err != nil {
			return false, err
		}
		branchName = strings.TrimSpace(gitMetadata.BranchName)
	}
	if branchName == "" {
		return false, nil
	}
	if err := s.git.deleteBranch(ctx, workspaceRoot, branchName, true); err != nil {
		return false, fmt.Errorf("delete task worktree branch %q: %w", branchName, err)
	}
	return true, nil
}

type taskSourceWorkspace struct {
	WorkspaceID string
	RootPath    string
}

func (s *Service) taskSourceWorkspace(ctx context.Context, projectID string, sourceWorkspaceID string) (taskSourceWorkspace, error) {
	trimmedSourceWorkspaceID := strings.TrimSpace(sourceWorkspaceID)
	if trimmedSourceWorkspaceID != "" {
		workspace, err := s.metadata.GetWorkspaceByID(ctx, trimmedSourceWorkspaceID)
		if err != nil {
			return taskSourceWorkspace{}, err
		}
		if strings.TrimSpace(workspace.ProjectID) != strings.TrimSpace(projectID) {
			return taskSourceWorkspace{}, fmt.Errorf("task source workspace %q does not belong to project %q", trimmedSourceWorkspaceID, strings.TrimSpace(projectID))
		}
		if strings.TrimSpace(workspace.CanonicalRootPath) == "" {
			return taskSourceWorkspace{}, fmt.Errorf("task source workspace %q has no root path", trimmedSourceWorkspaceID)
		}
		return taskSourceWorkspace{WorkspaceID: workspace.ID, RootPath: workspace.CanonicalRootPath}, nil
	}
	workspaces, err := s.metadata.ListProjectWorkspaces(ctx, projectID)
	if err != nil {
		return taskSourceWorkspace{}, err
	}
	for _, workspace := range workspaces {
		if workspace.IsPrimary && strings.TrimSpace(workspace.RootPath) != "" {
			return taskSourceWorkspace{WorkspaceID: workspace.WorkspaceID, RootPath: workspace.RootPath}, nil
		}
	}
	for _, workspace := range workspaces {
		if strings.TrimSpace(workspace.RootPath) != "" {
			return taskSourceWorkspace{WorkspaceID: workspace.WorkspaceID, RootPath: workspace.RootPath}, nil
		}
	}
	return taskSourceWorkspace{}, fmt.Errorf("project %q has no workspace for task worktree", strings.TrimSpace(projectID))
}

func (s *Service) taskManagedWorktreeView(ctx context.Context, worktreeID string) (serverapi.WorktreeView, error) {
	record, err := s.metadata.GetWorktreeRecordByID(ctx, worktreeID)
	if err != nil {
		return serverapi.WorktreeView{}, err
	}
	if strings.TrimSpace(record.CanonicalRoot) == "" {
		return serverapi.WorktreeView{}, fmt.Errorf("managed worktree %q has no canonical root: %w", worktreeID, serverapi.ErrWorktreeNotFound)
	}
	if _, err := os.Stat(record.CanonicalRoot); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return serverapi.WorktreeView{}, fmt.Errorf("managed worktree %q root %q is missing: %w", worktreeID, record.CanonicalRoot, serverapi.ErrWorktreeNotFound)
		}
		return serverapi.WorktreeView{}, err
	}
	gitMetadata, err := worktreeGitMetadataFromRecord(record)
	if err != nil {
		return serverapi.WorktreeView{}, err
	}
	return worktreeViewFromSynced(syncedWorktree{record: record, git: gitMetadata}, clientui.SessionExecutionTarget{})
}

func (s *Service) ListWorktrees(ctx context.Context, req serverapi.WorktreeListRequest) (serverapi.WorktreeListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorktreeListResponse{}, err
	}
	release, workspaceCtx, err := s.beginMutation(ctx, req.SessionID)
	if err != nil {
		return serverapi.WorktreeListResponse{}, err
	}
	defer release()
	synced, err := s.syncWorkspace(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot, req.IncludeDirtyCount)
	if err != nil {
		return serverapi.WorktreeListResponse{}, err
	}
	workspaceCtx.target, err = s.metadata.ResolveSessionExecutionTarget(ctx, req.SessionID)
	if err != nil {
		return serverapi.WorktreeListResponse{}, err
	}
	views, err := mapSyncedWorktrees(synced, workspaceCtx.target)
	if err != nil {
		return serverapi.WorktreeListResponse{}, err
	}
	return serverapi.WorktreeListResponse{Target: workspaceCtx.target, Worktrees: views}, nil
}

func (s *Service) ResolveWorktreeCreateTarget(ctx context.Context, req serverapi.WorktreeCreateTargetResolveRequest) (serverapi.WorktreeCreateTargetResolveResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorktreeCreateTargetResolveResponse{}, err
	}
	workspaceCtx, err := s.resolveSessionWorkspaceContext(ctx, req.SessionID)
	if err != nil {
		return serverapi.WorktreeCreateTargetResolveResponse{}, err
	}
	resolution, err := s.git.ResolveCreateTarget(ctx, workspaceCtx.workspaceRoot, req.Target)
	if err != nil {
		return serverapi.WorktreeCreateTargetResolveResponse{}, err
	}
	return serverapi.WorktreeCreateTargetResolveResponse{Resolution: serverapi.WorktreeCreateTargetResolution{
		Input:       resolution.Input,
		Kind:        serverapi.WorktreeCreateTargetResolutionKind(resolution.Kind),
		ResolvedRef: resolution.ResolvedRef,
	}}, nil
}

func (s *Service) CreateWorktree(ctx context.Context, req serverapi.WorktreeCreateRequest) (resp serverapi.WorktreeCreateResponse, err error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorktreeCreateResponse{}, err
	}
	createSpec, err := normalizeCreateSpec(CreateSpec{BaseRef: req.BaseRef, CreateBranch: req.CreateBranch, BranchName: req.BranchName})
	if err != nil {
		return serverapi.WorktreeCreateResponse{}, err
	}
	release, workspaceCtx, err := s.beginMutation(ctx, req.SessionID)
	if err != nil {
		return serverapi.WorktreeCreateResponse{}, err
	}
	defer release()
	cleanup := failedCreateCleanup{
		workspaceID:   workspaceCtx.workspaceID,
		workspaceRoot: workspaceCtx.workspaceRoot,
		branchName:    strings.TrimSpace(createSpec.BranchName),
	}
	defer func() {
		if err == nil || !cleanup.active {
			return
		}
		if cleanupErr := s.cleanupFailedCreate(ctx, cleanup); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()
	worktreeRoot, err := s.resolveRequestedWorktreeRoot(req.RootPath, workspaceCtx.workspaceID, createSpec)
	if err != nil {
		return serverapi.WorktreeCreateResponse{}, err
	}
	createdBranch, err := s.git.Add(ctx, workspaceCtx.workspaceRoot, worktreeRoot, createSpec)
	if err != nil {
		return serverapi.WorktreeCreateResponse{}, err
	}
	cleanup.active = true
	cleanup.worktreeRoot = strings.TrimSpace(worktreeRoot)
	cleanup.createdBranch = createdBranch
	// Re-canonicalize after creation because the now-existing path may resolve symlinked
	// parent segments differently than the pre-create non-existent target path.
	worktreeRoot, err = config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		return serverapi.WorktreeCreateResponse{}, err
	}
	cleanup.worktreeRoot = strings.TrimSpace(worktreeRoot)
	synced, err := s.syncWorkspace(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot, false)
	if err != nil {
		return serverapi.WorktreeCreateResponse{}, err
	}
	created, ok := findSyncedWorktreeByRoot(synced, worktreeRoot)
	if !ok {
		return serverapi.WorktreeCreateResponse{}, fmt.Errorf("created worktree %q was not discovered after git sync: %w", worktreeRoot, serverapi.ErrWorktreeNotFound)
	}
	created.record.Managed = true
	created.record.CreatedBranch = createdBranch
	created.record.OriginSessionID = workspaceCtx.sessionID
	created.record.UpdatedAt = time.Now().UTC()
	cleanup.worktreeID = strings.TrimSpace(created.record.ID)
	if err := s.metadata.UpsertWorktreeRecord(ctx, created.record); err != nil {
		return serverapi.WorktreeCreateResponse{}, err
	}
	setupSessionID, err := normalizeSetupSessionID(&workspaceCtx.sessionID)
	if err != nil {
		return serverapi.WorktreeCreateResponse{}, err
	}
	cleanup.active = false
	if err := s.runSetupForWorktree(ctx, setupExecutionRequest{
		SetupOperationID:    req.SetupOperationID,
		SourceWorkspaceRoot: workspaceCtx.workspaceRoot,
		BranchName:          strings.TrimSpace(created.git.BranchName),
		WorktreeRoot:        created.record.CanonicalRoot,
		ScriptPayload: setupScriptPayload{
			SessionID:   setupSessionID,
			ProjectID:   workspaceCtx.projectID,
			WorkspaceID: workspaceCtx.workspaceID,
			WorktreeID:  created.record.ID,
		},
		CreatedBranch: createdBranch,
	}); err != nil {
		return serverapi.WorktreeCreateResponse{}, err
	}
	previous, err := currentSyncedWorktree(synced, workspaceCtx.target)
	if err != nil {
		return serverapi.WorktreeCreateResponse{}, err
	}
	nextTarget, err := s.switchSessionTarget(ctx, workspaceCtx, previous, created)
	if err != nil {
		return serverapi.WorktreeCreateResponse{}, err
	}
	createdView, err := worktreeViewFromSynced(created, nextTarget)
	if err != nil {
		return serverapi.WorktreeCreateResponse{}, err
	}
	createdView.Managed = true
	createdView.CreatedBranch = createdBranch
	createdView.OriginSessionID = workspaceCtx.sessionID
	return serverapi.WorktreeCreateResponse{Target: nextTarget, Worktree: createdView, CreatedBranch: createdBranch}, nil
}

func (s *Service) cleanupFailedCreate(ctx context.Context, cleanup failedCreateCleanup) error {
	if s == nil || s.metadata == nil || s.git == nil || !cleanup.active {
		return nil
	}
	cleanupCtx, cancel := liveRollbackContext(ctx)
	defer cancel()
	var collected []error
	if strings.TrimSpace(cleanup.worktreeRoot) != "" {
		if err := s.git.Remove(cleanupCtx, cleanup.workspaceRoot, cleanup.worktreeRoot, false); err != nil {
			collected = append(collected, fmt.Errorf("remove failed worktree %q: %w", cleanup.worktreeRoot, err))
		}
	}
	if err := s.deleteWorktreeRecordForCleanup(cleanupCtx, cleanup.workspaceID, cleanup.worktreeID, cleanup.worktreeRoot); err != nil {
		collected = append(collected, err)
	}
	if cleanup.createdBranch && strings.TrimSpace(cleanup.branchName) != "" {
		if err := s.git.deleteBranch(cleanupCtx, cleanup.workspaceRoot, cleanup.branchName, false); err != nil {
			collected = append(collected, fmt.Errorf("delete created branch %q for failed worktree create: %w", cleanup.branchName, err))
		}
	}
	return errors.Join(collected...)
}

func (s *Service) deleteWorktreeRecordForCleanup(ctx context.Context, workspaceID string, worktreeID string, worktreeRoot string) error {
	if s == nil || s.metadata == nil {
		return nil
	}
	trimmedID := strings.TrimSpace(worktreeID)
	if trimmedID != "" {
		if err := s.metadata.DeleteWorktreeRecordByID(ctx, trimmedID); err != nil {
			return fmt.Errorf("delete failed worktree record %q: %w", trimmedID, err)
		}
		return nil
	}
	trimmedWorkspaceID := strings.TrimSpace(workspaceID)
	trimmedWorktreeRoot := strings.TrimSpace(worktreeRoot)
	if trimmedWorkspaceID == "" || trimmedWorktreeRoot == "" {
		return nil
	}
	records, err := s.metadata.ListWorktreeRecordsByWorkspaceID(ctx, trimmedWorkspaceID)
	if err != nil {
		return fmt.Errorf("list worktree records for failed create cleanup: %w", err)
	}
	var collected []error
	for _, record := range records {
		if strings.TrimSpace(record.CanonicalRoot) != trimmedWorktreeRoot {
			continue
		}
		if err := s.metadata.DeleteWorktreeRecordByID(ctx, record.ID); err != nil {
			collected = append(collected, fmt.Errorf("delete failed worktree record %q: %w", record.ID, err))
		}
	}
	return errors.Join(collected...)
}

func (s *Service) SwitchWorktree(ctx context.Context, req serverapi.WorktreeSwitchRequest) (serverapi.WorktreeSwitchResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorktreeSwitchResponse{}, err
	}
	release, workspaceCtx, err := s.beginMutation(ctx, req.SessionID)
	if err != nil {
		return serverapi.WorktreeSwitchResponse{}, err
	}
	defer release()
	synced, err := s.syncWorkspace(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot, false)
	if err != nil {
		return serverapi.WorktreeSwitchResponse{}, err
	}
	targetWorktree, ok := findSyncedWorktreeByID(synced, req.WorktreeID)
	if !ok {
		return serverapi.WorktreeSwitchResponse{}, serverapi.ErrWorktreeNotFound
	}
	previous, err := currentSyncedWorktree(synced, workspaceCtx.target)
	if err != nil {
		return serverapi.WorktreeSwitchResponse{}, err
	}
	nextTarget, err := s.switchSessionTarget(ctx, workspaceCtx, previous, targetWorktree)
	if err != nil {
		return serverapi.WorktreeSwitchResponse{}, err
	}
	view, err := worktreeViewFromSynced(targetWorktree, nextTarget)
	if err != nil {
		return serverapi.WorktreeSwitchResponse{}, err
	}
	return serverapi.WorktreeSwitchResponse{Target: nextTarget, Worktree: view}, nil
}

func (s *Service) DeleteWorktree(ctx context.Context, req serverapi.WorktreeDeleteRequest) (serverapi.WorktreeDeleteResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorktreeDeleteResponse{}, err
	}
	release, workspaceCtx, err := s.beginMutation(ctx, req.SessionID)
	if err != nil {
		return serverapi.WorktreeDeleteResponse{}, err
	}
	defer release()
	synced, err := s.syncWorkspace(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot, false)
	if err != nil {
		return serverapi.WorktreeDeleteResponse{}, err
	}
	targetWorktree, ok := findSyncedWorktreeByID(synced, req.WorktreeID)
	if !ok {
		return serverapi.WorktreeDeleteResponse{}, serverapi.ErrWorktreeNotFound
	}
	if targetWorktree.git.IsMain {
		return serverapi.WorktreeDeleteResponse{}, fmt.Errorf("cannot delete main workspace worktree: %w", serverapi.ErrWorktreeBlocked)
	}
	releaseDeletionSessionLeases, err := s.ensureDeletionUnblocked(ctx, workspaceCtx.sessionID, targetWorktree.record.ID, targetWorktree.record.CanonicalRoot)
	if err != nil {
		return serverapi.WorktreeDeleteResponse{}, err
	}
	defer releaseDeletionSessionLeases()
	if workspaceCtx.target.Worktree != nil && workspaceCtx.target.Worktree.ID == targetWorktree.record.ID {
		mainWorktree, mainFound := findMainWorktree(synced)
		if !mainFound {
			return serverapi.WorktreeDeleteResponse{}, fmt.Errorf("main worktree not found for workspace %q", workspaceCtx.workspaceID)
		}
		if _, err := s.switchSessionTarget(ctx, workspaceCtx, &targetWorktree, mainWorktree); err != nil {
			return serverapi.WorktreeDeleteResponse{}, err
		}
		workspaceCtx, err = s.resolveSessionWorkspaceContext(ctx, workspaceCtx.sessionID)
		if err != nil {
			return serverapi.WorktreeDeleteResponse{}, err
		}
	}
	if err := s.retargetActiveSessionsFromDeletedWorktree(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot, targetWorktree.record, workspaceCtx.sessionID); err != nil {
		return serverapi.WorktreeDeleteResponse{}, err
	}
	if err := s.git.Prune(ctx, workspaceCtx.workspaceRoot); err != nil {
		return serverapi.WorktreeDeleteResponse{}, err
	}
	synced, err = s.syncWorkspace(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot, false)
	if err != nil {
		return serverapi.WorktreeDeleteResponse{}, err
	}
	if registeredTarget, ok := findSyncedWorktreeByID(synced, req.WorktreeID); ok {
		dirtyCount, dirtyErr := s.git.DirtyFileCount(ctx, registeredTarget.record.CanonicalRoot)
		force := dirtyCount > 0 || dirtyErr != nil
		if err := s.git.Remove(ctx, workspaceCtx.workspaceRoot, registeredTarget.record.CanonicalRoot, force); err != nil {
			return serverapi.WorktreeDeleteResponse{}, err
		}
		synced, err = s.syncWorkspace(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot, false)
		if err != nil {
			return serverapi.WorktreeDeleteResponse{}, err
		}
	}
	branchDeleted := false
	branchCleanupMessage := s.branchCleanupSkippedMessage(targetWorktree, req.DeleteBranch)
	if s.shouldAttemptBranchCleanup(targetWorktree, req.DeleteBranch) {
		if err := s.git.deleteBranch(ctx, workspaceCtx.workspaceRoot, targetWorktree.git.BranchName, false); err != nil {
			branchCleanupMessage = fmt.Sprintf("Kept branch %s: %v", targetWorktree.git.BranchName, err)
		} else {
			branchDeleted = true
			branchCleanupMessage = fmt.Sprintf("Deleted branch %s", targetWorktree.git.BranchName)
		}
	}
	finalTarget, err := s.metadata.ResolveSessionExecutionTarget(ctx, workspaceCtx.sessionID)
	if err != nil {
		return serverapi.WorktreeDeleteResponse{}, err
	}
	view, err := worktreeViewFromSynced(targetWorktree, finalTarget)
	if err != nil {
		return serverapi.WorktreeDeleteResponse{}, err
	}
	return serverapi.WorktreeDeleteResponse{Target: finalTarget, Worktree: view, BranchDeleted: branchDeleted, BranchCleanupMessage: branchCleanupMessage}, nil
}

func (s *Service) beginMutation(ctx context.Context, sessionID string) (func(), sessionWorkspaceContext, error) {
	if s == nil || s.metadata == nil {
		return nil, sessionWorkspaceContext{}, errors.New("worktree service metadata store is required")
	}
	if s.runtime == nil {
		return nil, sessionWorkspaceContext{}, errors.New("worktree service runtime controller is required")
	}
	for {
		workspaceCtx, err := s.resolveSessionWorkspaceContext(ctx, sessionID)
		if err != nil {
			return nil, sessionWorkspaceContext{}, err
		}
		workspaceLease := s.acquireWorkspaceMutationLock(workspaceCtx.workspaceID)
		lockedWorkspaceCtx, err := s.resolveSessionWorkspaceContext(ctx, sessionID)
		if err != nil {
			workspaceLease()
			return nil, sessionWorkspaceContext{}, err
		}
		if strings.TrimSpace(lockedWorkspaceCtx.workspaceID) == strings.TrimSpace(workspaceCtx.workspaceID) {
			return workspaceLease, lockedWorkspaceCtx, nil
		}
		workspaceLease()
	}
}

func (s *Service) acquireWorkspaceMutationLock(workspaceID string) func() {
	trimmedWorkspaceID := strings.TrimSpace(workspaceID)
	if s == nil || trimmedWorkspaceID == "" {
		return func() {}
	}
	s.workspaceMu.Lock()
	if s.workspaceLocks == nil {
		s.workspaceLocks = make(map[string]*workspaceMutationLock)
	}
	lock := s.workspaceLocks[trimmedWorkspaceID]
	if lock == nil {
		lock = &workspaceMutationLock{}
		s.workspaceLocks[trimmedWorkspaceID] = lock
	}
	lock.refs++
	s.workspaceMu.Unlock()
	lock.mu.Lock()
	var once sync.Once
	return func() {
		once.Do(func() {
			lock.mu.Unlock()
			s.workspaceMu.Lock()
			defer s.workspaceMu.Unlock()
			lock.refs--
			if lock.refs == 0 {
				delete(s.workspaceLocks, trimmedWorkspaceID)
			}
		})
	}
}

func (s *Service) resolveSessionWorkspaceContext(ctx context.Context, sessionID string) (sessionWorkspaceContext, error) {
	if s == nil || s.metadata == nil {
		return sessionWorkspaceContext{}, errors.New("worktree service metadata store is required")
	}
	target, err := s.metadata.ResolveSessionExecutionTarget(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return sessionWorkspaceContext{}, err
	}
	binding, err := s.metadata.LookupWorkspaceBindingByID(ctx, strings.TrimSpace(target.WorkspaceID))
	if err != nil {
		return sessionWorkspaceContext{}, err
	}
	return sessionWorkspaceContext{
		target:        target,
		projectID:     strings.TrimSpace(binding.ProjectID),
		workspaceID:   strings.TrimSpace(target.WorkspaceID),
		workspaceRoot: strings.TrimSpace(target.WorkspaceRoot),
		sessionID:     strings.TrimSpace(sessionID),
	}, nil
}

func (s *Service) syncWorkspace(ctx context.Context, workspaceID string, workspaceRoot string, includeDirtyCount bool) ([]syncedWorktree, error) {
	return s.syncWorkspaceWithCreationBases(ctx, workspaceID, workspaceRoot, includeDirtyCount, nil)
}

func (s *Service) syncWorkspaceWithCreationBases(ctx context.Context, workspaceID string, workspaceRoot string, includeDirtyCount bool, creationBasesByRoot map[string]string) ([]syncedWorktree, error) {
	if s == nil || s.metadata == nil || s.git == nil {
		return nil, errors.New("worktree service dependencies are required")
	}
	gitEntries, err := s.git.List(ctx, workspaceRoot)
	if err != nil {
		return nil, err
	}
	existing, err := s.metadata.ListWorktreeRecordsByWorkspaceID(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	existingByRoot := make(map[string]metadata.WorktreeRecord, len(existing))
	for _, record := range existing {
		existingByRoot[strings.TrimSpace(record.CanonicalRoot)] = record
	}
	seenRoots := make(map[string]struct{}, len(gitEntries))
	now := time.Now().UTC()
	for _, gitEntry := range gitEntries {
		canonicalRoot := strings.TrimSpace(gitEntry.Root)
		seenRoots[canonicalRoot] = struct{}{}
		record, found := existingByRoot[canonicalRoot]
		if !found {
			record = metadata.WorktreeRecord{ID: "worktree-" + uuid.NewString(), WorkspaceID: strings.TrimSpace(workspaceID), CreatedAt: now}
			if creationBaseCommitOID, ok := creationBasesByRoot[canonicalRoot]; ok {
				commitOID := strings.TrimSpace(creationBaseCommitOID)
				if commitOID == "" {
					return nil, errors.New("worktree creation base commit oid is required")
				}
				record.CreationBaseCommitOID = &commitOID
			}
		} else if shouldResetWorktreeProvenance(record, gitEntry) {
			record.Managed = false
			record.CreatedBranch = false
			record.OriginSessionID = ""
		}
		record.WorkspaceID = strings.TrimSpace(workspaceID)
		record.CanonicalRoot = canonicalRoot
		record.DisplayName = filepath.Base(canonicalRoot)
		record.Availability = pathAvailability(canonicalRoot)
		record.IsMain = gitEntry.IsMain
		record.GitMetadataJSON, err = marshalGitMetadata(gitEntry)
		if err != nil {
			return nil, err
		}
		record.UpdatedAt = now
		if err := s.metadata.UpsertWorktreeRecord(ctx, record); err != nil {
			return nil, err
		}
	}
	for _, record := range existing {
		if _, ok := seenRoots[strings.TrimSpace(record.CanonicalRoot)]; ok {
			continue
		}
		if err := s.retargetSessionsFromWorktree(ctx, strings.TrimSpace(workspaceID), strings.TrimSpace(workspaceRoot), record, worktreeSessionRetargetOptions{reminder: worktreeReminderStateForExitedWorktree}); err != nil {
			return nil, err
		}
		if err := s.metadata.DeleteWorktreeRecordByID(ctx, record.ID); err != nil {
			return nil, err
		}
	}
	refreshed, err := s.metadata.ListWorktreeRecordsByWorkspaceID(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	refreshedByRoot := make(map[string]metadata.WorktreeRecord, len(refreshed))
	for _, record := range refreshed {
		refreshedByRoot[strings.TrimSpace(record.CanonicalRoot)] = record
	}
	synced := make([]syncedWorktree, 0, len(gitEntries))
	for _, gitEntry := range gitEntries {
		if includeDirtyCount && pathAvailability(gitEntry.Root) == "available" {
			dirtyCount, dirtyErr := s.git.DirtyFileCount(ctx, gitEntry.Root)
			if dirtyErr != nil {
				gitEntry.DirtyFileCount = -1
			} else {
				gitEntry.DirtyFileCount = dirtyCount
			}
		}
		record, ok := refreshedByRoot[strings.TrimSpace(gitEntry.Root)]
		if !ok {
			return nil, fmt.Errorf("synced worktree record missing for %q", gitEntry.Root)
		}
		synced = append(synced, syncedWorktree{record: record, git: gitEntry})
	}
	return synced, nil
}

func (s *Service) ensureDeletionUnblocked(ctx context.Context, currentSessionID string, worktreeID string, worktreeRoot string) (func(), error) {
	taskBlockers, err := s.metadata.Queries().CountNonTerminalTasksByManagedWorktree(ctx, sql.NullString{String: strings.TrimSpace(worktreeID), Valid: true})
	if err != nil {
		return func() {}, err
	}
	if taskBlockers > 0 {
		return func() {}, errors.Join(serverapi.ErrWorktreeBlocked, fmt.Errorf("worktree is still managed by %d non-terminal workflow task(s)", taskBlockers))
	}
	return s.ensureDeletionSessionAndProcessUnblocked(ctx, currentSessionID, worktreeID, worktreeRoot)
}

func (s *Service) ensureDeletionSessionAndProcessUnblocked(ctx context.Context, currentSessionID string, worktreeID string, worktreeRoot string) (func(), error) {
	blockers, err := s.metadata.ListSessionsTargetingWorktree(ctx, worktreeID)
	if err != nil {
		return func() {}, err
	}
	targetSessionIDs := make([]string, 0, len(blockers))
	for _, blocker := range blockers {
		sessionID := strings.TrimSpace(blocker.SessionID)
		if sessionID == "" || sessionID == strings.TrimSpace(currentSessionID) {
			continue
		}
		targetSessionIDs = append(targetSessionIDs, sessionID)
	}
	release := func() {}
	if s.active != nil && len(targetSessionIDs) > 0 {
		release = s.active.BlockSessionRuns(targetSessionIDs)
	}
	otherSessions := make([]metadata.WorktreeSessionBlocker, 0, len(blockers))
	for _, blocker := range blockers {
		sessionID := strings.TrimSpace(blocker.SessionID)
		if sessionID == "" || sessionID == strings.TrimSpace(currentSessionID) {
			continue
		}
		if s.active == nil {
			otherSessions = append(otherSessions, blocker)
			continue
		}
		if !s.active.IsSessionRuntimeActive(sessionID) {
			continue
		}
		active, err := s.runtime.HasBlockingRuntimeActivity(ctx, sessionID)
		if err != nil {
			release()
			return func() {}, err
		}
		if active {
			otherSessions = append(otherSessions, blocker)
		}
	}
	if len(otherSessions) > 0 {
		release()
		sort.Slice(otherSessions, func(i int, j int) bool {
			return otherSessions[i].UpdatedAt.After(otherSessions[j].UpdatedAt)
		})
		names := make([]string, 0, len(otherSessions))
		for _, blocker := range otherSessions {
			name := strings.TrimSpace(blocker.SessionName)
			if name == "" {
				name = blocker.SessionID
			}
			names = append(names, name)
		}
		return func() {}, errors.Join(serverapi.ErrWorktreeBlocked, fmt.Errorf("worktree is still targeted by active runs: %s", strings.Join(names, ", ")))
	}
	processBlockers := s.backgroundProcessBlockers(worktreeRoot)
	if len(processBlockers) > 0 {
		release()
		return func() {}, errors.Join(serverapi.ErrWorktreeBlocked, fmt.Errorf("worktree has active background processes: %s", strings.Join(processBlockers, ", ")))
	}
	return release, nil
}

func (s *Service) backgroundProcessBlockers(worktreeRoot string) []string {
	if s == nil || s.processes == nil {
		return nil
	}
	canonicalTarget, err := config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		return []string{strings.TrimSpace(worktreeRoot)}
	}
	entries := s.processes.List()
	blockers := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.Running {
			continue
		}
		candidate, err := config.CanonicalWorkspaceRoot(entry.Workdir)
		if err != nil {
			continue
		}
		if !sameOrDescendantPath(canonicalTarget, candidate) {
			continue
		}
		blockers = append(blockers, fmt.Sprintf("%s (%s)", entry.ID, strings.TrimSpace(entry.Command)))
	}
	return blockers
}

func (s *Service) resolveRequestedWorktreeRoot(requestedRoot string, workspaceID string, createSpec CreateSpec) (string, error) {
	if strings.TrimSpace(requestedRoot) == "" {
		workspaceBaseDir := filepath.Join(s.baseDir, workspaceID)
		if err := os.MkdirAll(workspaceBaseDir, 0o755); err != nil {
			return "", err
		}
		root, err := defaultWorktreeRoot(s.baseDir, workspaceID, defaultWorktreePathSeed(createSpec))
		if err != nil {
			return "", err
		}
		return nextAvailableWorktreeRoot(root)
	}
	trimmed := strings.TrimSpace(requestedRoot)
	expanded, err := expandTildePath(trimmed)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(expanded) {
		return config.CanonicalWorkspaceRoot(expanded)
	}
	cleaned := filepath.Clean(expanded)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("relative worktree root %q escapes base dir", requestedRoot)
	}
	return config.CanonicalWorkspaceRoot(filepath.Join(s.baseDir, cleaned))
}

func defaultWorktreePathSeed(createSpec CreateSpec) string {
	if createSpec.CreateBranch {
		return strings.TrimSpace(createSpec.BranchName)
	}
	trimmedRef := strings.TrimSpace(createSpec.BaseRef)
	if short := shortRefName(trimmedRef); short != "" {
		return short
	}
	return trimmedRef
}

func shortRefName(ref string) string {
	trimmed := strings.TrimSpace(ref)
	switch {
	case strings.HasPrefix(trimmed, "refs/heads/"):
		return strings.TrimPrefix(trimmed, "refs/heads/")
	case strings.HasPrefix(trimmed, "refs/tags/"):
		return strings.TrimPrefix(trimmed, "refs/tags/")
	case strings.HasPrefix(trimmed, "refs/remotes/"):
		return strings.TrimPrefix(trimmed, "refs/remotes/")
	default:
		return trimmed
	}
}

// ErrWorktreeRootCollisionCap is returned when no free worktree root can be
// found within the collision-suffix attempt cap. Callers match it via errors.Is.
var ErrWorktreeRootCollisionCap = errors.New("no available worktree root within collision cap")

// TaskBranchCollisionError reports that the task worktree branch already exists
// or resolves to an existing ref. It exposes the branch name and resolved ref
// so callers can inspect them via errors.As instead of parsing message wording.
type TaskBranchCollisionError struct {
	BranchName  string
	ResolvedRef string
}

func (e *TaskBranchCollisionError) Error() string {
	return fmt.Sprintf("task worktree branch %q already exists or resolves to %q", e.BranchName, e.ResolvedRef)
}

func nextAvailableWorktreeRoot(baseRoot string) (string, error) {
	canonicalBase, err := config.CanonicalWorkspaceRoot(baseRoot)
	if err != nil {
		return "", err
	}
	const maxCollisionSuffixAttempts = 1024
	for idx := 0; idx < maxCollisionSuffixAttempts; idx++ {
		candidate := canonicalBase
		if idx > 0 {
			candidate = fmt.Sprintf("%s-%d", canonicalBase, idx+1)
		}
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("no available worktree root under %q after %d attempts: %w", canonicalBase, maxCollisionSuffixAttempts, ErrWorktreeRootCollisionCap)
}

type setupExecutionRequest struct {
	SetupOperationID    serverapi.WorktreeSetupOperationID
	SourceWorkspaceRoot string
	BranchName          string
	WorktreeRoot        string
	ScriptPayload       setupScriptPayload
	CreatedBranch       bool
}

func (s *Service) runSetupForWorktree(ctx context.Context, req setupExecutionRequest) error {
	trimmedScript := strings.TrimSpace(s.setupScript)
	if trimmedScript == "" {
		return nil
	}
	operationID := req.SetupOperationID
	if err := operationID.Validate(); err != nil {
		operationID = serverapi.NewWorktreeSetupOperationID()
	}
	scriptPath, err := resolveSetupScriptPath(req.SourceWorkspaceRoot, trimmedScript)
	if err != nil {
		s.publishSetupEvent(serverapi.WorktreeSetupEvent{
			SetupOperationID:    operationID,
			SourceWorkspaceRoot: strings.TrimSpace(req.SourceWorkspaceRoot),
			WorktreeRoot:        strings.TrimSpace(req.WorktreeRoot),
			ScriptPath:          strings.TrimSpace(trimmedScript),
			Phase:               serverapi.WorktreeSetupPhaseFailed,
			Error:               err.Error(),
		})
		return fmt.Errorf("resolve worktree setup script: %w", err)
	}
	sessionID, err := normalizeSetupSessionID(req.ScriptPayload.SessionID)
	if err != nil {
		return err
	}
	payload := setupScriptPayload{
		SourceWorkspaceRoot: strings.TrimSpace(req.SourceWorkspaceRoot),
		BranchName:          strings.TrimSpace(req.BranchName),
		WorktreeRoot:        strings.TrimSpace(req.WorktreeRoot),
		SessionID:           sessionID,
		ProjectID:           strings.TrimSpace(req.ScriptPayload.ProjectID),
		WorkspaceID:         strings.TrimSpace(req.ScriptPayload.WorkspaceID),
		WorktreeID:          strings.TrimSpace(req.ScriptPayload.WorktreeID),
		CreatedBranch:       req.CreatedBranch,
	}
	started := serverapi.WorktreeSetupEvent{
		SetupOperationID:    operationID,
		SourceWorkspaceRoot: payload.SourceWorkspaceRoot,
		WorktreeRoot:        payload.WorktreeRoot,
		ScriptPath:          scriptPath,
		Phase:               serverapi.WorktreeSetupPhaseStarted,
	}
	s.publishSetupEvent(started)
	if err := s.runSetupScript(ctx, scriptPath, payload); err != nil {
		failure := started
		failure.Phase = serverapi.WorktreeSetupPhaseFailed
		var setupErr *setupScriptError
		if errors.As(err, &setupErr) {
			failure.Timeout = setupErr.Timeout
			failure.Canceled = setupErr.Canceled
			failure.ExitCode = setupErr.ExitCode
			failure.Stdout = setupErr.Stdout
			failure.Stderr = setupErr.Stderr
			failure.Error = setupErr.Error()
		} else {
			failure.Error = err.Error()
		}
		s.publishSetupEvent(failure)
		return err
	}
	completed := started
	completed.Phase = serverapi.WorktreeSetupPhaseCompleted
	s.publishSetupEvent(completed)
	return nil
}

func (s *Service) runSetupScript(ctx context.Context, scriptPath string, payload setupScriptPayload) error {
	setupCtx := ctx
	var cancel context.CancelFunc
	if s != nil && s.setupTimeoutSeconds > 0 {
		setupCtx, cancel = context.WithTimeout(ctx, time.Duration(s.setupTimeoutSeconds)*time.Second)
		defer cancel()
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return &setupScriptError{Message: fmt.Sprintf("marshal setup payload: %v", err)}
	}
	cmd := exec.CommandContext(setupCtx, scriptPath, payload.SourceWorkspaceRoot, payload.BranchName, payload.WorktreeRoot)
	cmd.Dir = payload.WorktreeRoot
	cmd.Stdin = strings.NewReader(string(body))
	cmd.Env, err = buildSetupEnvironment(os.Environ(), payload, platformSetupEnvironmentKeyCanonicalizer)
	if err != nil {
		return &setupScriptError{Message: fmt.Sprintf("build setup environment: %v", err), ScriptPath: scriptPath, WorktreeRoot: payload.WorktreeRoot}
	}
	stdout := shelltool.NewBoundedOutput(setupDiagnosticLimitBytes)
	stderr := shelltool.NewBoundedOutput(setupDiagnosticLimitBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	configureSetupCommand(cmd)
	if err := cmd.Start(); err != nil {
		return &setupScriptError{Message: err.Error(), ScriptPath: scriptPath, WorktreeRoot: payload.WorktreeRoot, Stdout: strings.TrimSpace(stdout.String()), Stderr: strings.TrimSpace(stderr.String())}
	}
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()
	select {
	case err = <-waitCh:
	case <-setupCtx.Done():
		terminateSetupCommand(cmd)
		err = <-waitCh
	}
	if err == nil {
		return nil
	}
	setupErr := &setupScriptError{Message: err.Error(), ScriptPath: scriptPath, WorktreeRoot: payload.WorktreeRoot, Stdout: strings.TrimSpace(stdout.String()), Stderr: strings.TrimSpace(stderr.String())}
	if setupCtx.Err() != nil {
		setupErr.Timeout = errors.Is(setupCtx.Err(), context.DeadlineExceeded)
		setupErr.Canceled = errors.Is(setupCtx.Err(), context.Canceled)
		if setupErr.Timeout {
			setupErr.TimeoutSeconds = s.setupTimeoutSeconds
			setupErr.Message = fmt.Sprintf("timed out after %s", time.Duration(s.setupTimeoutSeconds)*time.Second)
		} else {
			setupErr.Message = setupCtx.Err().Error()
		}
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode := exitErr.ExitCode()
		setupErr.ExitCode = &exitCode
	}
	return setupErr
}

func (s *Service) publishSetupEvent(evt serverapi.WorktreeSetupEvent) {
	if s == nil || s.setupBroker == nil {
		return
	}
	s.setupBroker.Publish(evt)
}

type setupScriptError struct {
	Message        string
	ScriptPath     string
	WorktreeRoot   string
	Timeout        bool
	TimeoutSeconds int
	Canceled       bool
	ExitCode       *int
	Stdout         string
	Stderr         string
}

func (e *setupScriptError) Error() string {
	if e == nil {
		return ""
	}
	parts := []string{"worktree setup script failed"}
	if strings.TrimSpace(e.Message) != "" {
		parts = append(parts, e.Message)
	}
	if e.Timeout && e.TimeoutSeconds > 0 {
		parts = append(parts, fmt.Sprintf("configured timeout %s", time.Duration(e.TimeoutSeconds)*time.Second))
	}
	if strings.TrimSpace(e.ScriptPath) != "" {
		parts = append(parts, "script "+strings.TrimSpace(e.ScriptPath))
	}
	if strings.TrimSpace(e.WorktreeRoot) != "" {
		parts = append(parts, "worktree "+strings.TrimSpace(e.WorktreeRoot))
	}
	if strings.TrimSpace(e.Stderr) != "" {
		parts = append(parts, strings.TrimSpace(e.Stderr))
	}
	if strings.TrimSpace(e.Stdout) != "" {
		parts = append(parts, strings.TrimSpace(e.Stdout))
	}
	return strings.Join(parts, ": ")
}
