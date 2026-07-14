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
	RunWorktreeTransition(
		ctx context.Context,
		sessionID string,
		fn func(context.Context, func(context.Context, clientui.SessionExecutionTarget, *session.WorktreeReminderState) error) error,
	) error
	PublishWorktreeTransitionOutcome(sessionID string, outcome clientui.WorktreeTransitionOutcome)
	SteerWorktreeTransitionFailure(ctx context.Context, sessionID string, outcome clientui.WorktreeTransitionOutcome) error
}

type activeRuntimeSource interface {
	IsSessionRuntimeActive(sessionID string) bool
	BlockSessionRuns(sessionIDs []string) func()
}

type processSource interface {
	List() []shelltool.Snapshot
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
	baseDir             string
	setupScript         string
	setupTimeoutSeconds int
	setupBroker         *setupEventBroker

	workspaceMu    sync.Mutex
	workspaceLocks map[string]*workspaceMutationLock

	transitionCtx     context.Context
	cancelTransitions context.CancelFunc
	transitionMu      sync.Mutex
	transitions       map[string]pendingWorktreeTransition
	transitionWG      sync.WaitGroup
	transitionsClosed bool
}

type workspaceMutationLock struct {
	token chan struct{}
	refs  int
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
	Worktree      serverapi.WorktreeTopologyEntry
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

func NewService(metadataStore *metadata.Store, gitInspector *GitInspector, active activeRuntimeSource, runtime runtimeController, processes processSource, opts ServiceOptions) *Service {
	if gitInspector == nil {
		gitInspector = NewGitInspector(nil)
	}
	if active == nil {
		if source, ok := runtime.(activeRuntimeSource); ok {
			active = source
		}
	}
	transitionCtx, cancelTransitions := context.WithCancel(context.Background())
	return &Service{
		metadata:            metadataStore,
		git:                 gitInspector,
		runtime:             runtime,
		active:              active,
		processes:           processes,
		baseDir:             strings.TrimSpace(opts.BaseDir),
		setupScript:         strings.TrimSpace(opts.SetupScript),
		setupTimeoutSeconds: opts.SetupTimeoutSeconds,
		setupBroker:         newSetupEventBroker(),
		workspaceLocks:      make(map[string]*workspaceMutationLock),
		transitionCtx:       transitionCtx,
		cancelTransitions:   cancelTransitions,
		transitions:         make(map[string]pendingWorktreeTransition),
	}
}

func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.transitionMu.Lock()
	s.transitionsClosed = true
	s.transitionMu.Unlock()
	if s.cancelTransitions != nil {
		s.cancelTransitions()
	}
	s.transitionWG.Wait()
	return nil
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
	release, err := s.acquireWorkspaceMutationLock(ctx, workspace.WorkspaceID)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
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
	if !isManagedExecutionTargetMode(task.ExecutionTargetMode) {
		return TaskWorktreeMaterialization{}, errors.New("task does not have a locked managed execution target")
	}
	workspace, err := s.taskSourceWorkspace(ctx, task.ProjectID, task.SourceWorkspaceID.String)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	release, err := s.acquireWorkspaceMutationLock(ctx, workspace.WorkspaceID)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	defer release()
	task, err = s.metadata.Queries().GetTask(ctx, taskID)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	if !isManagedExecutionTargetMode(task.ExecutionTargetMode) {
		return TaskWorktreeMaterialization{}, errors.New("task does not have a locked managed execution target")
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

func isManagedExecutionTargetMode(mode sql.NullString) bool {
	if !mode.Valid {
		return false
	}
	switch workflow.ExecutionTargetMode(mode.String) {
	case workflow.ExecutionTargetModeHead, workflow.ExecutionTargetModeDefaultBranch, workflow.ExecutionTargetModeCustomRef:
		return true
	default:
		return false
	}
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
				ID:            uuid.NewString(),
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
	return TaskWorktreeMaterialization{
		Worktree: registeredTopologyEntry(syncedWorktree{record: record, git: gitMetadata}),
	}, nil
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
	branchName, ok := identity.NamedBranch()
	if !ok {
		return TaskWorktreeMaterialization{}, errors.New("created managed worktree does not have a named branch")
	}
	created, err := s.registerCreatedWorktree(ctx, createdWorktreeRegistration{
		WorkspaceID:           req.Workspace.WorkspaceID,
		WorkspaceRoot:         req.Workspace.RootPath,
		WorktreeRoot:          worktreeRoot,
		Managed:               true,
		CreatedBranch:         createdBranch,
		ExistingRecord:        req.ExistingRecord,
		CreationBaseCommitOID: req.CreationBaseOID,
	})
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	cleanup.worktreeID = created.record.ID
	updated, err := s.metadata.Queries().UpdateTaskManagedWorktree(ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
		ID:                req.Task.ID,
		ManagedWorktreeID: sql.NullString{String: created.record.ID, Valid: true},
		UpdatedAtUnixMs:   created.record.UpdatedAt.UnixMilli(),
	})
	if err != nil {
		return TaskWorktreeMaterialization{}, fmt.Errorf(
			"bind managed worktree %q (workspace %q) to task %q (source workspace %q): %w",
			created.record.ID,
			created.record.WorkspaceID,
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
		WorktreeRoot:        created.record.CanonicalRoot,
		ScriptPayload: setupScriptPayload{
			ProjectID:   req.Task.ProjectID,
			WorkspaceID: req.Workspace.WorkspaceID,
			WorktreeID:  created.record.ID,
		},
		CreatedBranch: createdBranch,
	}); err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	return TaskWorktreeMaterialization{
		Worktree:      registeredTopologyEntry(created),
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
	release, err := s.acquireWorkspaceMutationLock(ctx, record.WorkspaceID)
	if err != nil {
		return DeleteTaskWorktreeResponse{}, err
	}
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
	topology, err := s.projectTopology(ctx, record.WorkspaceID, workspaceRoot)
	if err != nil {
		return DeleteTaskWorktreeResponse{}, err
	}
	entry, found := topologyEntryByWorktreeID(topology, worktreeID)
	if !found {
		return DeleteTaskWorktreeResponse{}, fmt.Errorf("managed worktree %q is absent from projected topology: %w", worktreeID, serverapi.ErrWorktreeNotFound)
	}
	var target syncedWorktree
	targetFound := entry.Variant == serverapi.WorktreeTopologyVariantRegistered
	if targetFound {
		target = syncedWorktree{record: record, git: gitWorktreeFromFacts(entry.Registered.Git)}
	}
	forceRemoval := false
	if targetFound {
		dirtyState, err := s.git.ProbeDirtyState(ctx, target.record.CanonicalRoot)
		if err != nil {
			return DeleteTaskWorktreeResponse{}, err
		}
		forceRemoval = dirtyState.Kind != serverapi.WorktreeDirtyStateClean
	}
	retargetCompensation, err := s.retargetDeleteSessions(ctx, sessionWorkspaceContext{
		workspaceID:   record.WorkspaceID,
		workspaceRoot: workspaceRoot,
	}, record, nil)
	if err != nil {
		return DeleteTaskWorktreeResponse{}, err
	}
	if targetFound {
		if err := s.git.Remove(ctx, workspaceRoot, target.record.CanonicalRoot, forceRemoval); err != nil {
			return DeleteTaskWorktreeResponse{}, errors.Join(err, retargetCompensation.rollback(ctx))
		}
	}
	// The worktree itself is already removed by this point, so a branch-cleanup
	// failure must not abort the remaining metadata cleanup; otherwise the record
	// is left pointing at a removed worktree. Treat branch deletion as best-effort
	// and report the outcome via BranchDeleted.
	branchDeleted, branchErr := s.deleteTaskWorktreeBranch(ctx, workspaceRoot, record, target, targetFound)
	if branchErr != nil {
		branchDeleted = false
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
	if !record.Managed {
		return false, nil
	}
	var live *GitWorktree
	if found {
		live = &target.git
	}
	branchName, proven, err := kentCreatedBranchForCleanup(record, live)
	if err != nil {
		return false, err
	}
	if !proven {
		return false, nil
	}
	if err := s.git.deleteBranch(ctx, workspaceRoot, branchName, false); err != nil {
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

func (s *Service) ListWorktrees(ctx context.Context, req serverapi.WorktreeListRequest) (serverapi.WorktreeListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorktreeListResponse{}, err
	}
	workspaceCtx, err := s.resolveSessionWorkspaceContext(ctx, req.SessionID)
	if err != nil {
		return serverapi.WorktreeListResponse{}, err
	}
	topology, err := s.projectTopology(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot)
	if err != nil {
		return serverapi.WorktreeListResponse{}, err
	}
	worktrees, err := projectWorktreeList(topology, &workspaceCtx.target)
	if err != nil {
		return serverapi.WorktreeListResponse{}, err
	}
	return serverapi.WorktreeListResponse{Target: workspaceCtx.target, Worktrees: worktrees}, nil
}

func (s *Service) ListWorkspaceWorktrees(ctx context.Context, req serverapi.WorktreeWorkspaceListRequest) (serverapi.WorktreeWorkspaceListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorktreeWorkspaceListResponse{}, err
	}
	if s == nil || s.metadata == nil {
		return serverapi.WorktreeWorkspaceListResponse{}, errors.New("worktree service metadata store is required")
	}
	binding, err := s.metadata.LookupWorkspaceBindingByID(ctx, strings.TrimSpace(req.WorkspaceID))
	if err != nil {
		return serverapi.WorktreeWorkspaceListResponse{}, err
	}
	if strings.TrimSpace(binding.ProjectID) != strings.TrimSpace(req.ProjectID) {
		return serverapi.WorktreeWorkspaceListResponse{}, serverapi.ErrWorkspaceNotRegistered
	}
	topology, err := s.projectTopology(ctx, binding.WorkspaceID, binding.CanonicalRoot)
	if err != nil {
		return serverapi.WorktreeWorkspaceListResponse{}, err
	}
	worktrees, err := projectWorktreeList(topology, nil)
	if err != nil {
		return serverapi.WorktreeWorkspaceListResponse{}, err
	}
	return serverapi.WorktreeWorkspaceListResponse{
		WorkspaceID: binding.WorkspaceID,
		Worktrees:   worktrees,
	}, nil
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
	release, workspaceCtx, err := s.beginWorkspaceMutation(ctx, req.SessionID)
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
	created, err := s.registerCreatedWorktree(ctx, createdWorktreeRegistration{
		WorkspaceID:     workspaceCtx.workspaceID,
		WorkspaceRoot:   workspaceCtx.workspaceRoot,
		WorktreeRoot:    worktreeRoot,
		Managed:         true,
		CreatedBranch:   createdBranch,
		OriginSessionID: workspaceCtx.sessionID,
	})
	if err != nil {
		return serverapi.WorktreeCreateResponse{}, err
	}
	cleanup.worktreeID = strings.TrimSpace(created.record.ID)
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
		return serverapi.WorktreeCreateResponse{}, serverapi.NewWorktreeSetupRetainedError(
			serverapi.WorktreeTopologyEntry{
				Variant: serverapi.WorktreeTopologyVariantRegistered,
				Registered: &serverapi.WorktreeRegisteredFacts{
					Git:  gitFactsFromEntry(created.git),
					Kent: kentFactsFromRecord(created.record),
				},
			},
			err.Error(),
			err,
		)
	}
	createdEntry, err := s.createdWorktreeListEntry(ctx, workspaceCtx, created.record.ID)
	if err != nil {
		return serverapi.WorktreeCreateResponse{}, err
	}
	return serverapi.WorktreeCreateResponse{Target: workspaceCtx.target, Worktree: createdEntry}, nil
}

func (s *Service) createdWorktreeListEntry(ctx context.Context, workspaceCtx sessionWorkspaceContext, worktreeID string) (serverapi.WorktreeListEntry, error) {
	topology, err := s.projectTopology(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot)
	if err != nil {
		return serverapi.WorktreeListEntry{}, err
	}
	entries, err := projectWorktreeList(topology, &workspaceCtx.target)
	if err != nil {
		return serverapi.WorktreeListEntry{}, err
	}
	for _, entry := range entries {
		id := topologyWorktreeID(entry.Topology)
		if id != nil && strings.TrimSpace(*id) == strings.TrimSpace(worktreeID) {
			return entry, nil
		}
	}
	return serverapi.WorktreeListEntry{}, fmt.Errorf("created worktree %q is absent from projected topology: %w", strings.TrimSpace(worktreeID), serverapi.ErrWorktreeNotFound)
}

type createdWorktreeRegistration struct {
	WorkspaceID           string
	WorkspaceRoot         string
	WorktreeRoot          string
	Managed               bool
	CreatedBranch         bool
	OriginSessionID       string
	ExistingRecord        *metadata.WorktreeRecord
	CreationBaseCommitOID *string
}

func (s *Service) registerCreatedWorktree(ctx context.Context, req createdWorktreeRegistration) (syncedWorktree, error) {
	gitEntry, found, err := s.git.FindCreatedWorktree(ctx, req.WorkspaceRoot, req.WorktreeRoot)
	if err != nil {
		return syncedWorktree{}, err
	}
	if !found {
		return syncedWorktree{}, fmt.Errorf("created worktree %q was not discovered in Git topology: %w", req.WorktreeRoot, serverapi.ErrWorktreeNotFound)
	}
	creationBaseCommitOID, err := normalizeOptionalCommitOID(req.CreationBaseCommitOID)
	if err != nil {
		return syncedWorktree{}, err
	}
	now := time.Now().UTC()
	record := metadata.WorktreeRecord{
		ID:                    uuid.NewString(),
		WorkspaceID:           strings.TrimSpace(req.WorkspaceID),
		CanonicalRoot:         strings.TrimSpace(gitEntry.Root),
		DisplayName:           filepath.Base(strings.TrimSpace(gitEntry.Root)),
		Availability:          string(PathAvailability(gitEntry.Root)),
		IsMain:                gitEntry.IsMain,
		Managed:               req.Managed,
		CreatedBranch:         req.CreatedBranch,
		OriginSessionID:       strings.TrimSpace(req.OriginSessionID),
		CreationBaseCommitOID: creationBaseCommitOID,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if req.ExistingRecord != nil {
		record.ID = req.ExistingRecord.ID
		record.Managed = req.ExistingRecord.Managed || req.Managed
		record.CreatedBranch = req.ExistingRecord.CreatedBranch || req.CreatedBranch
		record.OriginSessionID = req.ExistingRecord.OriginSessionID
		record.CreatedAt = req.ExistingRecord.CreatedAt
		if req.ExistingRecord.CreationBaseCommitOID != nil {
			existingCreationBaseCommitOID, err := normalizeOptionalCommitOID(req.ExistingRecord.CreationBaseCommitOID)
			if err != nil {
				return syncedWorktree{}, err
			}
			if record.CreationBaseCommitOID != nil && !sameCreationBaseCommit(req.ExistingRecord.CreationBaseCommitOID, *record.CreationBaseCommitOID) {
				return syncedWorktree{}, &TaskWorktreeBaseCommitMismatchError{
					WorktreeID:            record.ID,
					RequestedCommitOID:    *record.CreationBaseCommitOID,
					CreationBaseCommitOID: req.ExistingRecord.CreationBaseCommitOID,
				}
			}
			record.CreationBaseCommitOID = existingCreationBaseCommitOID
		}
	}
	record.GitMetadataJSON, err = marshalGitMetadata(gitEntry)
	if err != nil {
		return syncedWorktree{}, err
	}
	if err := s.metadata.UpsertWorktreeRecord(ctx, record); err != nil {
		return syncedWorktree{}, err
	}
	return syncedWorktree{record: record, git: gitEntry}, nil
}

func normalizeOptionalCommitOID(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil, errors.New("worktree creation base commit oid must be non-blank when present")
	}
	return &normalized, nil
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

func (s *Service) beginWorkspaceMutation(ctx context.Context, sessionID string) (func(), sessionWorkspaceContext, error) {
	if s == nil || s.metadata == nil {
		return nil, sessionWorkspaceContext{}, errors.New("worktree service metadata store is required")
	}
	for {
		workspaceCtx, err := s.resolveSessionWorkspaceContext(ctx, sessionID)
		if err != nil {
			return nil, sessionWorkspaceContext{}, err
		}
		workspaceLease, err := s.acquireWorkspaceMutationLock(ctx, workspaceCtx.workspaceID)
		if err != nil {
			return nil, sessionWorkspaceContext{}, err
		}
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

func (s *Service) acquireWorkspaceMutationLock(ctx context.Context, workspaceID string) (func(), error) {
	trimmedWorkspaceID := strings.TrimSpace(workspaceID)
	if s == nil {
		return nil, errors.New("worktree service is required")
	}
	if ctx == nil {
		return nil, errors.New("workspace mutation context is required")
	}
	if trimmedWorkspaceID == "" {
		return nil, errors.New("workspace mutation requires a workspace id")
	}
	s.workspaceMu.Lock()
	if s.workspaceLocks == nil {
		s.workspaceLocks = make(map[string]*workspaceMutationLock)
	}
	lock := s.workspaceLocks[trimmedWorkspaceID]
	if lock == nil {
		token := make(chan struct{}, 1)
		token <- struct{}{}
		lock = &workspaceMutationLock{token: token}
		s.workspaceLocks[trimmedWorkspaceID] = lock
	}
	lock.refs++
	s.workspaceMu.Unlock()

	select {
	case <-ctx.Done():
		s.releaseWorkspaceMutationLockReference(trimmedWorkspaceID, lock)
		return nil, ctx.Err()
	case <-lock.token:
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			select {
			case lock.token <- struct{}{}:
			default:
				panic(fmt.Sprintf(
					"release workspace mutation lock invariant violated: workspace_id=%q token already available",
					trimmedWorkspaceID,
				))
			}
			s.releaseWorkspaceMutationLockReference(trimmedWorkspaceID, lock)
		})
	}, nil
}

func (s *Service) releaseWorkspaceMutationLockReference(workspaceID string, lock *workspaceMutationLock) {
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	registered := s.workspaceLocks[workspaceID]
	if registered != lock || lock.refs <= 0 {
		panic(fmt.Sprintf(
			"release workspace mutation lock reference invariant violated: workspace_id=%q refs=%d registered_same=%t",
			workspaceID,
			lock.refs,
			registered == lock,
		))
	}
	lock.refs--
	if lock.refs == 0 {
		delete(s.workspaceLocks, workspaceID)
	}
}

func (s *Service) resolveSessionWorkspaceContext(ctx context.Context, sessionID string) (sessionWorkspaceContext, error) {
	if s == nil || s.metadata == nil {
		return sessionWorkspaceContext{}, errors.New("worktree service metadata store is required")
	}
	target, err := s.metadata.ResolveSessionExecutionTarget(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return sessionWorkspaceContext{}, fmt.Errorf(
			"resolve worktree workspace context for session %q: %w",
			strings.TrimSpace(sessionID),
			err,
		)
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
