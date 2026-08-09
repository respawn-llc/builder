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
	"strings"
	"sync"
	"time"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/requestmemo"
	"core/server/sessionruntime"
	shelltool "core/server/tools/shell"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/boundedio"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/invariant"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/worktreecontract"
	"github.com/google/uuid"
)

const setupDiagnosticLimitBytes = 16 * 1024

const rollbackSessionTargetTimeout = 5 * time.Second

type runtimePublisher interface {
	PublishSessionIdentity(sessionID string) error
	PublishWorktreeTransitionOutcome(sessionID string, outcome clientui.WorktreeTransitionOutcome)
}

type processSource interface {
	List() []shelltool.Snapshot
}

type ServiceOptions struct {
	BaseDir             string
	SetupScript         string
	SetupTimeoutSeconds int
	ResolveSetup        func(sourceWorkspaceRoot string) (config.WorktreeSettings, error)
}

type Service struct {
	metadata            *metadata.Store
	git                 *GitInspector
	authority           *sessionruntime.Authority
	publisher           runtimePublisher
	processes           processSource
	managedRoots        *managedRootAllocator
	setupScript         string
	setupTimeoutSeconds int
	resolveSetup        func(sourceWorkspaceRoot string) (config.WorktreeSettings, error)
	setupBroker         *setupEventBroker
	workspaceMutations  *requestmemo.MutationLaneRegistry[string]

	transitionCtx     context.Context
	cancelTransitions context.CancelFunc
	transitionMu      sync.Mutex
	transitions       map[string]pendingWorktreeTransition
	transitionWG      sync.WaitGroup
	transitionsClosed bool
}

type syncedWorktree struct {
	record metadata.WorktreeRecord
	git    GitWorktree
}

func (s *Service) evaluateDeleteCleanliness(ctx context.Context, entry serverapi.WorktreeTopologyEntry) (clientui.WorktreeDirtyState, error) {
	if s == nil || s.git == nil {
		return clientui.WorktreeDirtyState{}, errors.New("worktree service dependencies are required")
	}
	if err := entry.Validate(); err != nil {
		return clientui.WorktreeDirtyState{}, err
	}
	if entry.Variant == serverapi.WorktreeTopologyVariantMissing {
		return clientui.WorktreeDirtyState{Kind: clientui.WorktreeDirtyStateClean}, nil
	}
	root := topologyRoot(entry)
	if strings.TrimSpace(root) == "" {
		return clientui.WorktreeDirtyState{}, errors.New("worktree topology root is required")
	}
	dirtyState, err := s.git.ProbeDirtyState(ctx, root)
	if err == nil {
		return dirtyState, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return clientui.WorktreeDirtyState{}, err
	}
	cause := err.Error()
	return clientui.WorktreeDirtyState{
		Kind:         clientui.WorktreeDirtyStateUnknown,
		UnknownCause: &cause,
	}, nil
}

type deleteTargetActivityLease struct {
	ctx   context.Context
	close func()
}

func (l deleteTargetActivityLease) Context() context.Context {
	return l.ctx
}

func (l deleteTargetActivityLease) Close() {
	if l.close != nil {
		l.close()
	}
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

type managedRootKind uint8

const (
	managedRootKindExplicit managedRootKind = iota + 1
	managedRootKindAutomatic
)

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

type TaskExecutionRootPreparationRequest struct {
	TaskID           workflow.TaskID
	SetupOperationID *serverapi.WorktreeSetupOperationID
	ManagedTarget    *GitRevision
	SetupRequirement worktreecontract.SetupRequirement
}

type TaskExecutionRootPreparation struct {
	Root                     workflowstore.ExecutionRoot
	SetupResult              *WorktreeSetupResult
	RetainedPreviousWorktree *serverapi.RetainedPreviousWorktree
	Materialization          *TaskWorktreeMaterialization
}

type LockedTaskWorktreeRestoreRequest struct {
	TaskID           workflow.TaskID
	SetupOperationID *serverapi.WorktreeSetupOperationID
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
	SetupResult   *WorktreeSetupResult
}

type WorktreeSetupResult struct {
	Completed   *serverapi.WorktreeSetupCompleted
	NotRequired *serverapi.WorktreeSetupNotRequired
	Failed      *serverapi.WorktreeSetupFailed
}

func (r WorktreeSetupResult) Validate() error {
	payloadCount := 0
	if r.Completed != nil {
		payloadCount++
		if err := r.Completed.Validate(); err != nil {
			return err
		}
	}
	if r.NotRequired != nil {
		payloadCount++
		if err := r.NotRequired.Validate(); err != nil {
			return err
		}
	}
	if r.Failed != nil {
		payloadCount++
		if err := r.Failed.Validate(); err != nil {
			return err
		}
	}
	if payloadCount != 1 {
		return errors.New("worktree setup result requires exactly one final payload")
	}
	return nil
}

type boundManagedTaskWorktree struct {
	materialization TaskWorktreeMaterialization
	record          metadata.WorktreeRecord
	workspace       taskSourceWorkspace
	task            sqlitegen.TaskRecord
	branchName      string
	setupPayload    setupScriptPayload
}

func (b boundManagedTaskWorktree) setupExecution(settings *config.WorktreeSettings) (setupExecutionRequest, error) {
	recordedCheckout, err := worktreeGitMetadataFromRecord(b.record)
	if err != nil {
		return setupExecutionRequest{}, err
	}
	return setupExecutionRequest{
		SourceWorkspaceRoot: b.workspace.RootPath,
		ResolvedSettings:    settings,
		BranchName:          b.branchName,
		WorktreeRoot:        b.record.CanonicalRoot,
		ScriptPayload:       b.setupPayload,
		CreatedBranch:       b.materialization.CreatedBranch,
		RetainedWorktree:    &b.materialization.Worktree,
		Recreation: &setupRecreationFacts{
			RecordedCheckout:      recordedCheckout,
			CreationBaseCommitOID: b.record.CreationBaseCommitOID,
		},
	}, nil
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

func NewService(metadataStore *metadata.Store, gitInspector *GitInspector, authority *sessionruntime.Authority, publisher runtimePublisher, processes processSource, opts ServiceOptions) *Service {
	if gitInspector == nil {
		gitInspector = NewGitInspector(nil)
	}
	transitionCtx, cancelTransitions := context.WithCancel(context.Background())
	return &Service{
		metadata:            metadataStore,
		git:                 gitInspector,
		authority:           authority,
		publisher:           publisher,
		processes:           processes,
		managedRoots:        newManagedRootAllocator(opts.BaseDir, nil),
		setupScript:         strings.TrimSpace(opts.SetupScript),
		setupTimeoutSeconds: opts.SetupTimeoutSeconds,
		resolveSetup:        opts.ResolveSetup,
		setupBroker:         newSetupEventBroker(),
		workspaceMutations:  requestmemo.NewMutationLaneRegistry[string](),
		transitionCtx:       transitionCtx,
		cancelTransitions:   cancelTransitions,
		transitions:         make(map[string]pendingWorktreeTransition),
	}
}

type sessionStartAdmissionMode uint8

const (
	sessionStartAdmissionWait sessionStartAdmissionMode = iota + 1
	sessionStartAdmissionTry
)

func parseSessionStartAdmissionIDs(raw []string) ([]runtimeids.SessionID, error) {
	ids, err := sessionruntime.ParseSessionIDs(raw)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, errors.New("session ids are required")
	}
	return ids, nil
}

func (s *Service) acquireSessionStartAdmission(
	ctx context.Context,
	sessionIDs []runtimeids.SessionID,
	mode sessionStartAdmissionMode,
) (sessionruntime.SessionStartBlockRelease, error) {
	if s == nil || s.authority == nil {
		return nil, errors.New("worktree runtime authority is required")
	}
	if len(sessionIDs) == 0 {
		return nil, errors.New("session ids are required")
	}
	switch mode {
	case sessionStartAdmissionWait:
		return s.authority.BlockSessionStarts(ctx, sessionIDs, sessionruntime.SessionStartBlockMaintenance)
	case sessionStartAdmissionTry:
		return s.authority.TryBlockSessionStarts(ctx, sessionIDs, sessionruntime.SessionStartBlockMaintenance)
	default:
		return nil, fmt.Errorf("session start admission mode %d is invalid", mode)
	}
}

func releaseSessionStarts(release sessionruntime.SessionStartBlockRelease) {
	if release == nil {
		return
	}
	if err := release.Close(context.Background()); err != nil {
		panic(fmt.Sprintf("release worktree session start block: %v", err))
	}
}

func authorizeSessionMaintenance(ctx context.Context, release sessionruntime.SessionStartBlockRelease) context.Context {
	if release == nil {
		return ctx
	}
	return release.AuthorizeMaintenance(ctx)
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

func (s *Service) PrepareTaskExecutionRoot(ctx context.Context, req TaskExecutionRootPreparationRequest) (TaskExecutionRootPreparation, error) {
	if s == nil || s.metadata == nil || s.git == nil {
		return TaskExecutionRootPreparation{}, errors.New("worktree service dependencies are required")
	}
	taskID := strings.TrimSpace(string(req.TaskID))
	if taskID == "" {
		return TaskExecutionRootPreparation{}, errors.New("task_id is required")
	}
	var target *GitRevision
	if req.ManagedTarget != nil {
		resolved, err := validateResolvedTaskWorktreeTarget(*req.ManagedTarget)
		if err != nil {
			return TaskExecutionRootPreparation{}, err
		}
		target = &resolved
	}
	task, err := s.metadata.Queries().GetTask(ctx, taskID)
	if err != nil {
		return TaskExecutionRootPreparation{}, err
	}
	workspace, err := s.taskSourceWorkspace(ctx, task.ProjectID, task.SourceWorkspaceID.String)
	if err != nil {
		return TaskExecutionRootPreparation{}, err
	}
	lease, err := s.acquireWorkspaceMutationLease(ctx, workspace.WorkspaceID)
	if err != nil {
		return TaskExecutionRootPreparation{}, err
	}
	defer lease.Release()
	task, err = s.metadata.Queries().GetTask(ctx, taskID)
	if err != nil {
		return TaskExecutionRootPreparation{}, err
	}
	if task.ExecutionTargetMode.Valid {
		return TaskExecutionRootPreparation{}, errors.New("task execution-root preparation requires an unlocked task")
	}
	if !worktreecontract.IsValidSetupRequirement(req.SetupRequirement) {
		return TaskExecutionRootPreparation{}, errors.New("task execution-root preparation setup requirement is invalid")
	}
	root := workflowstore.ExecutionRoot{
		SourceWorkspaceID:   workspace.WorkspaceID,
		SourceWorkspaceRoot: workspace.RootPath,
	}
	var previous *serverapi.RetainedPreviousWorktree
	replacement := false
	var existingRecord *metadata.WorktreeRecord
	if task.ManagedWorktreeID.Valid && strings.TrimSpace(task.ManagedWorktreeID.String) != "" {
		record, err := s.metadata.GetWorktreeRecordByID(ctx, strings.TrimSpace(task.ManagedWorktreeID.String))
		if err != nil {
			return TaskExecutionRootPreparation{}, err
		}
		if target != nil && sameCreationBaseCommit(record.CreationBaseCommitOID, target.CommitOID) {
			existingRecord = &record
		} else {
			previous, err = s.releaseProvisionalTaskWorktree(ctx, task, workspace, record)
			if err != nil {
				return TaskExecutionRootPreparation{}, err
			}
			replacement = true
		}
	}
	if target == nil {
		return TaskExecutionRootPreparation{Root: root, RetainedPreviousWorktree: previous}, nil
	}
	materialized, err := s.prepareManagedTaskWorktree(
		ctx,
		task,
		workspace,
		req.SetupOperationID,
		*target,
		existingRecord,
		replacement,
		req.SetupRequirement,
	)
	prepared := taskExecutionRootPreparation(root, materialized, previous)
	if err != nil {
		var retained *serverapi.WorktreeSetupRetainedError
		if errors.As(err, &retained) && previous != nil {
			retained.RetainedPreviousWorktree = previous
		}
		return prepared, err
	}
	return prepared, nil
}

func (s *Service) prepareManagedTaskWorktree(
	ctx context.Context,
	task sqlitegen.TaskRecord,
	workspace taskSourceWorkspace,
	setupOperationID *serverapi.WorktreeSetupOperationID,
	resolvedTarget GitRevision,
	existingRecord *metadata.WorktreeRecord,
	replacement bool,
	setupRequirement worktreecontract.SetupRequirement,
) (TaskWorktreeMaterialization, error) {
	if existingRecord != nil {
		record := *existingRecord
		identity, identityErr := s.git.ValidateManagedWorktreeIdentity(ctx, ManagedWorktreeIdentitySpec{
			SourceWorkspaceRoot:  workspace.RootPath,
			ExpectedWorktreeRoot: record.CanonicalRoot,
		})
		if identityErr == nil {
			bound, err := s.reuseProvisionalManagedTaskWorktree(ctx, task, workspace, record, identity)
			if err != nil {
				return TaskWorktreeMaterialization{}, err
			}
			if setupRequirement == worktreecontract.SetupRequirementAlreadyCompleted {
				bound.materialization.SetupResult = &WorktreeSetupResult{
					Completed: &serverapi.WorktreeSetupCompleted{},
				}
				return bound.materialization, nil
			}
			return s.runManagedTaskWorktreeSetupRecovery(ctx, bound, setupOperationID, true)
		}
		var typedIdentityErr *ManagedWorktreeIdentityError
		if !errors.As(identityErr, &typedIdentityErr) || typedIdentityErr.Kind != ManagedWorktreeIdentityErrorRootMissing {
			return TaskWorktreeMaterialization{}, identityErr
		}
	}
	createSpec := CreateSpec{BaseRef: resolvedTarget.CommitOID, CreateBranch: true, BranchName: task.ShortID}
	var location managedTaskWorktreeLocation
	var err error
	if replacement {
		location, err = s.managedRoots.reserveTaskLocation(
			workspace.RootPath,
			task.ShortID,
			managedTaskWorktreeLocationReplacement,
			func(branch string) (bool, error) {
				return s.git.BranchExists(ctx, workspace.RootPath, branch)
			},
		)
		if err != nil {
			return TaskWorktreeMaterialization{}, err
		}
		createSpec.BranchName = location.BranchName
	} else {
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
		location, err = s.managedRoots.reserveTaskLocation(
			workspace.RootPath,
			task.ShortID,
			managedTaskWorktreeLocationInitial,
			nil,
		)
		if err != nil {
			return TaskWorktreeMaterialization{}, err
		}
		if createSpec.CreateBranch {
			createSpec.BranchName = location.BranchName
		}
	}
	creationBaseOID := resolvedTarget.CommitOID
	materialized, err := s.createManagedTaskWorktree(ctx, managedTaskWorktreeCreationRequest{
		Task:             task,
		Workspace:        workspace,
		CreateSpec:       createSpec,
		RequestedRoot:    &location.Root,
		ReservedRoot:     true,
		SetupOperationID: setupOperationID,
		ExistingRecord:   existingRecord,
		CreationBaseOID:  &creationBaseOID,
	})
	if err != nil {
		return materialized, err
	}
	return materialized, nil
}

func taskExecutionRootPreparation(
	root workflowstore.ExecutionRoot,
	materialized TaskWorktreeMaterialization,
	previous *serverapi.RetainedPreviousWorktree,
) TaskExecutionRootPreparation {
	prepared := TaskExecutionRootPreparation{
		Root:                     root,
		SetupResult:              materialized.SetupResult,
		RetainedPreviousWorktree: previous,
	}
	if materialized.Worktree.Registered != nil {
		prepared.Materialization = &materialized
		prepared.Root.Managed = &workflowstore.ManagedExecutionRoot{
			WorktreeID: materialized.Worktree.Registered.Kent.WorktreeID,
			Root:       materialized.Worktree.Registered.Git.CanonicalRoot,
		}
	}
	return prepared
}

func (s *Service) releaseProvisionalTaskWorktree(
	ctx context.Context,
	task sqlitegen.TaskRecord,
	workspace taskSourceWorkspace,
	record metadata.WorktreeRecord,
) (*serverapi.RetainedPreviousWorktree, error) {
	recorded, err := worktreeGitMetadataFromRecord(record)
	if err != nil {
		return nil, err
	}
	live, safelyRecreatable, err := s.inspectSafeWorktreeRecreation(
		ctx,
		workspace.RootPath,
		record.CanonicalRoot,
		record.CreationBaseCommitOID,
		recorded,
	)
	if err != nil {
		return nil, err
	}
	topology := registeredTopologyEntry(syncedWorktree{record: record, git: live})
	if !safelyRecreatable {
		if err := s.unbindTaskManagedWorktree(ctx, task); err != nil {
			return nil, err
		}
		return &serverapi.RetainedPreviousWorktree{Worktree: topology}, nil
	}
	branchName, named := worktreeNamedBranch(live)
	if record.CreatedBranch && !named {
		return nil, &ManagedWorktreeIdentityError{Kind: ManagedWorktreeIdentityErrorDetachedHead}
	}
	if err := s.git.Remove(ctx, workspace.RootPath, record.CanonicalRoot, false); err != nil {
		return nil, err
	}
	if err := s.unbindTaskManagedWorktree(ctx, task); err != nil {
		return nil, err
	}
	if record.CreatedBranch {
		if err := s.git.deleteBranch(ctx, workspace.RootPath, branchName, true); err != nil {
			return nil, err
		}
	}
	if err := s.metadata.DeleteWorktreeRecordByID(ctx, record.ID); err != nil {
		return nil, err
	}
	return nil, nil
}

func (s *Service) unbindTaskManagedWorktree(ctx context.Context, task sqlitegen.TaskRecord) error {
	updated, err := s.metadata.Queries().UpdateTaskManagedWorktree(ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
		ID:                task.ID,
		ManagedWorktreeID: sql.NullString{},
		UpdatedAtUnixMs:   time.Now().UTC().UnixMilli(),
	})
	if err != nil {
		return err
	}
	if updated != 1 {
		return sql.ErrNoRows
	}
	return nil
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
	lease, err := s.acquireWorkspaceMutationLease(ctx, workspace.WorkspaceID)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	defer lease.Release()
	task, err = s.metadata.Queries().GetTask(ctx, taskID)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	if !isManagedExecutionTargetMode(task.ExecutionTargetMode) {
		return TaskWorktreeMaterialization{}, errors.New("task does not have a locked managed execution target")
	}
	if !task.ManagedWorktreeID.Valid || strings.TrimSpace(task.ManagedWorktreeID.String) == "" {
		return s.restoreUnboundLockedTaskWorktree(task, workspace)
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

func (s *Service) restoreUnboundLockedTaskWorktree(task sqlitegen.TaskRecord, workspace taskSourceWorkspace) (TaskWorktreeMaterialization, error) {
	occupied, err := s.managedRoots.exactTaskRootOccupied(workspace.RootPath, task.ShortID)
	if err != nil {
		return TaskWorktreeMaterialization{}, &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseRootInaccessible, Err: err}
	}
	if occupied {
		return TaskWorktreeMaterialization{}, &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseConflict}
	}
	return TaskWorktreeMaterialization{}, &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseMissingBranch}
}

func (s *Service) inspectManagedTaskWorktree(
	ctx context.Context,
	workspace taskSourceWorkspace,
	record metadata.WorktreeRecord,
	identity ManagedWorktreeIdentity,
) (metadata.WorktreeRecord, GitWorktree, error) {
	validatedRoot, err := s.managedRoots.validatePersistedRoot(record.CanonicalRoot, workspace.RootPath)
	if err != nil {
		return metadata.WorktreeRecord{}, GitWorktree{}, &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseInvalidRoot, Err: err}
	}
	record.CanonicalRoot = validatedRoot
	revision, err := s.git.ResolveHEAD(ctx, record.CanonicalRoot)
	if err != nil {
		return metadata.WorktreeRecord{}, GitWorktree{}, &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseGitFailure, Err: err}
	}
	return record, GitWorktree{
		Root:     record.CanonicalRoot,
		HeadOID:  revision.CommitOID,
		Branch:   identity.branch,
		Detached: identity.branch == nil,
	}, nil
}

func (s *Service) rebindHealthyManagedTaskWorktree(ctx context.Context, task sqlitegen.TaskRecord, workspace taskSourceWorkspace, record metadata.WorktreeRecord, identity ManagedWorktreeIdentity) (TaskWorktreeMaterialization, error) {
	record, gitMetadata, err := s.inspectManagedTaskWorktree(ctx, workspace, record, identity)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
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

func (s *Service) reuseProvisionalManagedTaskWorktree(
	ctx context.Context,
	task sqlitegen.TaskRecord,
	workspace taskSourceWorkspace,
	record metadata.WorktreeRecord,
	identity ManagedWorktreeIdentity,
) (boundManagedTaskWorktree, error) {
	recordedCheckout, err := worktreeGitMetadataFromRecord(record)
	if err != nil {
		return boundManagedTaskWorktree{}, err
	}
	branchName, named := worktreeNamedBranch(recordedCheckout)
	if !named {
		return boundManagedTaskWorktree{}, errors.New("provisional worktree recorded checkout requires a named branch")
	}
	record, live, err := s.inspectManagedTaskWorktree(ctx, workspace, record, identity)
	if err != nil {
		return boundManagedTaskWorktree{}, err
	}
	return boundManagedTaskWorktree{
		materialization: TaskWorktreeMaterialization{
			Worktree: registeredTopologyEntry(syncedWorktree{record: record, git: live}),
		},
		record:     record,
		workspace:  workspace,
		task:       task,
		branchName: branchName,
		setupPayload: setupScriptPayload{
			ProjectID:   task.ProjectID,
			WorkspaceID: workspace.WorkspaceID,
			WorktreeID:  record.ID,
		},
	}, nil
}

type managedTaskWorktreeCreationRequest struct {
	Task             sqlitegen.TaskRecord
	Workspace        taskSourceWorkspace
	CreateSpec       CreateSpec
	RequestedRoot    *string
	ReservedRoot     bool
	SetupOperationID *serverapi.WorktreeSetupOperationID
	ExistingRecord   *metadata.WorktreeRecord
	CreationBaseOID  *string
}

func (s *Service) restoreMissingLockedTaskWorktree(ctx context.Context, req LockedTaskWorktreeRestoreRequest, task sqlitegen.TaskRecord, workspace taskSourceWorkspace, record metadata.WorktreeRecord) (TaskWorktreeMaterialization, error) {
	gitMetadata, err := worktreeGitMetadataFromRecord(record)
	if err != nil {
		return TaskWorktreeMaterialization{}, &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseInvalidRoot, Err: err}
	}
	if gitMetadata.Detached || gitMetadata.Branch == nil {
		return TaskWorktreeMaterialization{}, &LockedTaskWorktreeError{Cause: LockedTaskWorktreeCauseMissingBranch}
	}
	branchName := gitMetadata.Branch.Name()
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
		return materialized, err
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

func (s *Service) validateManagedRootForCreation(ctx context.Context, root string, kind managedRootKind, exemptRoot *string) error {
	existingRoots, err := s.metadata.ListManagedWorktreeRoots(ctx)
	if err == nil {
		err = s.managedRoots.validateNoManagedRootOverlap(root, existingRoots, exemptRoot)
	}
	if err != nil && kind == managedRootKindAutomatic {
		err = errors.Join(err, removeEmptyManagedRootAfterAddFailure(root))
	}
	return err
}

func (s *Service) createManagedTaskWorktree(ctx context.Context, req managedTaskWorktreeCreationRequest) (resp TaskWorktreeMaterialization, err error) {
	setupSettings, err := s.worktreeSetupSettings(req.Workspace.RootPath)
	if err != nil {
		if req.ReservedRoot && req.RequestedRoot != nil {
			err = errors.Join(err, removeEmptyManagedRootAfterAddFailure(strings.TrimSpace(*req.RequestedRoot)))
		}
		return TaskWorktreeMaterialization{}, err
	}
	bound, err := s.createAndBindManagedTaskWorktree(ctx, req)
	if err != nil {
		return TaskWorktreeMaterialization{}, err
	}
	return s.runManagedTaskWorktreeSetupRecoveryWithSettings(ctx, bound, req.SetupOperationID, false, setupSettings)
}

func (s *Service) createAndBindManagedTaskWorktree(ctx context.Context, req managedTaskWorktreeCreationRequest) (resp boundManagedTaskWorktree, err error) {
	createSpec, err := normalizeCreateSpec(req.CreateSpec)
	if err != nil {
		return boundManagedTaskWorktree{}, err
	}
	var worktreeRoot string
	rootKind := managedRootKindExplicit
	if req.RequestedRoot == nil {
		worktreeRoot, err = s.managedRoots.reserveTaskRoot(req.Workspace.RootPath, req.Task.ShortID)
		if err != nil {
			return boundManagedTaskWorktree{}, err
		}
		rootKind = managedRootKindAutomatic
	} else {
		requestedRoot := strings.TrimSpace(*req.RequestedRoot)
		if requestedRoot == "" {
			return boundManagedTaskWorktree{}, errors.New("requested managed worktree root is required")
		}
		worktreeRoot, err = s.managedRoots.resolveExplicitRoot(requestedRoot, req.Workspace.RootPath)
		if err != nil {
			return boundManagedTaskWorktree{}, err
		}
		if req.ReservedRoot {
			rootKind = managedRootKindAutomatic
		}
	}
	var exemptRoot *string
	if req.ExistingRecord != nil {
		exemptRoot = &req.ExistingRecord.CanonicalRoot
	}
	if err := s.validateManagedRootForCreation(ctx, worktreeRoot, rootKind, exemptRoot); err != nil {
		return boundManagedTaskWorktree{}, err
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
	createdBranch, err := s.addManagedWorktree(ctx, req.Workspace.RootPath, worktreeRoot, createSpec, rootKind)
	if err != nil {
		return boundManagedTaskWorktree{}, err
	}
	cleanup.active = true
	cleanup.createdBranch = createdBranch
	worktreeRoot, err = config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		return boundManagedTaskWorktree{}, err
	}
	cleanup.worktreeRoot = worktreeRoot
	identity, err := s.git.ValidateManagedWorktreeIdentity(ctx, ManagedWorktreeIdentitySpec{
		SourceWorkspaceRoot:  req.Workspace.RootPath,
		ExpectedWorktreeRoot: worktreeRoot,
	})
	if err != nil {
		return boundManagedTaskWorktree{}, err
	}
	branchName, ok := identity.NamedBranch()
	if !ok {
		return boundManagedTaskWorktree{}, errors.New("created managed worktree does not have a named branch")
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
		return boundManagedTaskWorktree{}, err
	}
	cleanup.worktreeID = created.record.ID
	updated, err := s.metadata.Queries().UpdateTaskManagedWorktree(ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
		ID:                req.Task.ID,
		ManagedWorktreeID: sql.NullString{String: created.record.ID, Valid: true},
		UpdatedAtUnixMs:   created.record.UpdatedAt.UnixMilli(),
	})
	if err != nil {
		return boundManagedTaskWorktree{}, fmt.Errorf(
			"bind managed worktree %q (workspace %q) to task %q (source workspace %q): %w",
			created.record.ID,
			created.record.WorkspaceID,
			req.Task.ID,
			req.Task.SourceWorkspaceID.String,
			err,
		)
	}
	if updated != 1 {
		return boundManagedTaskWorktree{}, sql.ErrNoRows
	}
	cleanup.active = false
	worktree := registeredTopologyEntry(created)
	return boundManagedTaskWorktree{
		materialization: TaskWorktreeMaterialization{
			Worktree:      worktree,
			Created:       true,
			CreatedBranch: createdBranch,
		},
		record:     created.record,
		workspace:  req.Workspace,
		task:       req.Task,
		branchName: branchName,
		setupPayload: setupScriptPayload{
			ProjectID:   req.Task.ProjectID,
			WorkspaceID: req.Workspace.WorkspaceID,
			WorktreeID:  created.record.ID,
		},
	}, nil
}

func (s *Service) runManagedTaskWorktreeSetupRecovery(
	ctx context.Context,
	bound boundManagedTaskWorktree,
	setupOperationID *serverapi.WorktreeSetupOperationID,
	recreateBeforeFirstAttempt bool,
) (TaskWorktreeMaterialization, error) {
	settings, err := s.worktreeSetupSettings(bound.workspace.RootPath)
	if err != nil {
		return bound.materialization, err
	}
	return s.runManagedTaskWorktreeSetupRecoveryWithSettings(ctx, bound, setupOperationID, recreateBeforeFirstAttempt, settings)
}

func (s *Service) runManagedTaskWorktreeSetupRecoveryWithSettings(
	ctx context.Context,
	bound boundManagedTaskWorktree,
	setupOperationID *serverapi.WorktreeSetupOperationID,
	recreateBeforeFirstAttempt bool,
	settings config.WorktreeSettings,
) (TaskWorktreeMaterialization, error) {
	observer, err := s.taskSetupAttemptObserver(setupOperationID)
	if err != nil {
		return bound.materialization, err
	}
	attempt, err := bound.setupExecution(&settings)
	if err != nil {
		return bound.materialization, err
	}
	retainedMaterialization := bound.materialization
	hasRetainedMaterialization := true
	result, err := s.runSetupRecovery(ctx, setupRecoveryRequest{
		Attempt:                    attempt,
		Observer:                   observer,
		RecreateBeforeFirstAttempt: recreateBeforeFirstAttempt,
		Recreate: func(ctx context.Context) (setupExecutionRequest, error) {
			recreated, removed, err := s.recreateBoundManagedTaskWorktree(ctx, bound)
			if removed {
				hasRetainedMaterialization = false
			}
			if err != nil {
				return setupExecutionRequest{}, err
			}
			bound = recreated
			retainedMaterialization = bound.materialization
			hasRetainedMaterialization = true
			return bound.setupExecution(&settings)
		},
	})
	if err != nil {
		if hasRetainedMaterialization {
			return retainedMaterialization, err
		}
		return TaskWorktreeMaterialization{}, err
	}
	if err := result.Result.Validate(); err != nil {
		return TaskWorktreeMaterialization{}, fmt.Errorf("validate worktree setup result: %w", err)
	}
	bound.materialization.SetupResult = &result.Result
	if result.Result.Failed != nil {
		scriptPath, ok := setupScriptPathFromError(result.Err)
		if !ok {
			return bound.materialization, errors.Join(
				result.Err,
				errors.New("failed setup result is missing typed setup script identity"),
			)
		}
		retainedErr, validationErr := serverapi.NewWorktreeSetupRetainedError(
			bound.materialization.Worktree,
			scriptPath,
			result.Result.Failed.Diagnostic,
			result.Err,
		)
		if validationErr != nil {
			return bound.materialization, errors.Join(result.Err, validationErr)
		}
		return bound.materialization, retainedErr
	}
	return bound.materialization, nil
}

func (s *Service) recreateBoundManagedTaskWorktree(
	ctx context.Context,
	bound boundManagedTaskWorktree,
) (boundManagedTaskWorktree, bool, error) {
	if err := s.git.Remove(ctx, bound.workspace.RootPath, bound.record.CanonicalRoot, false); err != nil {
		return boundManagedTaskWorktree{}, false, err
	}
	root := bound.record.CanonicalRoot
	recreated, err := s.createAndBindManagedTaskWorktree(ctx, managedTaskWorktreeCreationRequest{
		Task:             bound.task,
		Workspace:        bound.workspace,
		CreateSpec:       CreateSpec{BaseRef: bound.branchName},
		RequestedRoot:    &root,
		ExistingRecord:   &bound.record,
		CreationBaseOID:  bound.record.CreationBaseCommitOID,
		SetupOperationID: nil,
	})
	return recreated, true, err
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
	lease, err := s.acquireWorkspaceMutationLease(ctx, record.WorkspaceID)
	if err != nil {
		return DeleteTaskWorktreeResponse{}, err
	}
	defer lease.Release()
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
	if err := s.ensureNoOtherNonTerminalTasksManageWorktree(ctx, taskID, record); err != nil {
		return DeleteTaskWorktreeResponse{}, err
	}
	activityLease, err := s.acquireDeleteTargetActivity(ctx, nil, &record, &record.CanonicalRoot)
	if err != nil {
		return DeleteTaskWorktreeResponse{}, err
	}
	defer activityLease.Close()
	ctx = activityLease.Context()
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
		gitEntry, err := gitWorktreeFromFacts(entry.Registered.Git)
		if err != nil {
			return DeleteTaskWorktreeResponse{}, err
		}
		target = syncedWorktree{record: record, git: gitEntry}
	}
	forceRemoval := false
	if targetFound {
		dirtyState, err := s.git.ProbeDirtyState(ctx, target.record.CanonicalRoot)
		if err != nil {
			return DeleteTaskWorktreeResponse{}, err
		}
		forceRemoval = dirtyState.Kind != clientui.WorktreeDirtyStateClean
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
	workspace, err := s.metadata.ResolveProjectSourceWorkspace(ctx, projectID)
	if err != nil {
		return taskSourceWorkspace{}, err
	}
	if strings.TrimSpace(workspace.CanonicalRootPath) == "" {
		return taskSourceWorkspace{}, fmt.Errorf("task source workspace %q has no root path", workspace.ID)
	}
	return taskSourceWorkspace{WorkspaceID: workspace.ID, RootPath: workspace.CanonicalRootPath}, nil
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
	defer func() {
		if err == nil {
			return
		}
		if contractErr := serverapi.ValidateWorktreeCreateErrorBoundary(err, "worktree.create", invariant.NewPolicy()); contractErr != nil {
			err = contractErr
			return
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		var retainedErr *serverapi.WorktreeSetupRetainedError
		if errors.As(err, &retainedErr) {
			return
		}
		var createErr *serverapi.WorktreeCreateError
		if errors.As(err, &createErr) {
			return
		}
		err = serverapi.NewWorktreeCreateError(serverapi.WorktreeCreateErrorOwnerForm, err.Error(), err)
	}()
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
	if createSpec.CreateBranch {
		resolved, resolveErr := s.git.ResolveRevisionCommit(ctx, workspaceCtx.workspaceRoot, createSpec.BaseRef)
		if resolveErr != nil {
			if errors.Is(resolveErr, context.Canceled) || errors.Is(resolveErr, context.DeadlineExceeded) {
				return serverapi.WorktreeCreateResponse{}, resolveErr
			}
			owner := serverapi.WorktreeCreateErrorOwnerForm
			var revisionErr *GitRevisionResolutionError
			if errors.As(resolveErr, &revisionErr) {
				switch revisionErr.Kind {
				case GitRevisionResolutionErrorInvalidRevision, GitRevisionResolutionErrorNonCommit:
					owner = serverapi.WorktreeCreateErrorOwnerBaseRef
				}
			}
			return serverapi.WorktreeCreateResponse{}, serverapi.NewWorktreeCreateError(
				owner,
				resolveErr.Error(),
				resolveErr,
			)
		}
		createSpec.BaseRef = resolved.CommitOID
	}
	var worktreeRoot string
	rootKind := managedRootKindExplicit
	if strings.TrimSpace(req.RootPath) == "" {
		worktreeRoot, err = s.managedRoots.reserveRegularRoot(workspaceCtx.workspaceRoot)
		if err != nil {
			return serverapi.WorktreeCreateResponse{}, err
		}
		rootKind = managedRootKindAutomatic
	} else {
		worktreeRoot, err = s.managedRoots.resolveExplicitRoot(req.RootPath, workspaceCtx.workspaceRoot)
		if err != nil {
			return serverapi.WorktreeCreateResponse{}, err
		}
	}
	if err := s.validateManagedRootForCreation(ctx, worktreeRoot, rootKind, nil); err != nil {
		return serverapi.WorktreeCreateResponse{}, err
	}
	createdBranch, err := s.addManagedWorktree(ctx, workspaceCtx.workspaceRoot, worktreeRoot, createSpec, rootKind)
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
	branchName, named := worktreeNamedBranch(created.git)
	if !named {
		return serverapi.WorktreeCreateResponse{}, errors.New("created managed worktree does not have a named branch")
	}
	cleanup.active = false
	if err := s.runSetupForWorktree(ctx, req.SetupOperationID, setupExecutionRequest{
		SourceWorkspaceRoot: workspaceCtx.workspaceRoot,
		BranchName:          branchName,
		WorktreeRoot:        created.record.CanonicalRoot,
		ScriptPayload: setupScriptPayload{
			SessionID:   setupSessionID,
			ProjectID:   workspaceCtx.projectID,
			WorkspaceID: workspaceCtx.workspaceID,
			WorktreeID:  created.record.ID,
		},
		CreatedBranch: createdBranch,
		RetainedWorktree: &serverapi.WorktreeTopologyEntry{
			Variant: serverapi.WorktreeTopologyVariantRegistered,
			Registered: &serverapi.WorktreeRegisteredFacts{
				Git:  gitFactsFromEntry(created.git),
				Kent: kentFactsFromRecord(created.record),
			},
		},
	}); err != nil {
		scriptPath, ok := setupScriptPathFromError(err)
		if !ok {
			return serverapi.WorktreeCreateResponse{}, err
		}
		retainedErr, validationErr := serverapi.NewWorktreeSetupRetainedError(
			serverapi.WorktreeTopologyEntry{
				Variant: serverapi.WorktreeTopologyVariantRegistered,
				Registered: &serverapi.WorktreeRegisteredFacts{
					Git:  gitFactsFromEntry(created.git),
					Kent: kentFactsFromRecord(created.record),
				},
			},
			scriptPath,
			err.Error(),
			err,
		)
		if validationErr != nil {
			return serverapi.WorktreeCreateResponse{}, errors.Join(err, validationErr)
		}
		return serverapi.WorktreeCreateResponse{}, retainedErr
	}
	createdEntry, err := s.createdWorktreeListEntry(ctx, workspaceCtx, created.record.ID)
	if err != nil {
		return serverapi.WorktreeCreateResponse{}, err
	}
	return serverapi.WorktreeCreateResponse{Target: workspaceCtx.target, Worktree: createdEntry}, nil
}

func (s *Service) addManagedWorktree(
	ctx context.Context,
	workspaceRoot string,
	worktreeRoot string,
	createSpec CreateSpec,
	rootKind managedRootKind,
) (bool, error) {
	createdBranch, err := s.git.Add(ctx, workspaceRoot, worktreeRoot, createSpec)
	if err == nil || rootKind != managedRootKindAutomatic {
		return createdBranch, err
	}
	if cleanupErr := removeEmptyManagedRootAfterAddFailure(worktreeRoot); cleanupErr != nil {
		return false, errors.Join(err, cleanupErr)
	}
	return false, err
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
		workspaceLease, err := s.acquireWorkspaceMutationLease(ctx, workspaceCtx.workspaceID)
		if err != nil {
			return nil, sessionWorkspaceContext{}, err
		}
		lockedWorkspaceCtx, err := s.resolveSessionWorkspaceContext(ctx, sessionID)
		if err != nil {
			workspaceLease.Release()
			return nil, sessionWorkspaceContext{}, err
		}
		if strings.TrimSpace(lockedWorkspaceCtx.workspaceID) == strings.TrimSpace(workspaceCtx.workspaceID) {
			return workspaceLease.Release, lockedWorkspaceCtx, nil
		}
		workspaceLease.Release()
	}
}

func (s *Service) acquireWorkspaceMutationLease(ctx context.Context, workspaceID string) (*requestmemo.MutationLaneLease[string], error) {
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
	if s.workspaceMutations == nil {
		return nil, errors.New("worktree workspace mutation lanes are required")
	}
	return s.workspaceMutations.Acquire(ctx, trimmedWorkspaceID)
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

type setupExecutionRequest struct {
	SourceWorkspaceRoot string
	ResolvedSettings    *config.WorktreeSettings
	BranchName          string
	WorktreeRoot        string
	ScriptPayload       setupScriptPayload
	CreatedBranch       bool
	RetainedWorktree    *serverapi.WorktreeTopologyEntry
	Recreation          *setupRecreationFacts
}

type setupRecreationFacts struct {
	RecordedCheckout      GitWorktree
	CreationBaseCommitOID *string
}

type setupAttemptObserver interface {
	ObserveSetupAttempt(serverapi.WorktreeSetupStarted)
}

type setupAttemptObserverFunc func(serverapi.WorktreeSetupStarted)

func (f setupAttemptObserverFunc) ObserveSetupAttempt(attempt serverapi.WorktreeSetupStarted) {
	f(attempt)
}

type setupRecoveryRequest struct {
	Attempt                    setupExecutionRequest
	Observer                   setupAttemptObserver
	RecreateBeforeFirstAttempt bool
	Recreate                   func(context.Context) (setupExecutionRequest, error)
}

type setupRecoveryResult struct {
	Result WorktreeSetupResult
	Err    error
}

type preparedSetupAttempt struct {
	started        serverapi.WorktreeSetupStarted
	scriptPath     string
	payload        setupScriptPayload
	timeoutSeconds int
	retained       *serverapi.WorktreeTopologyEntry
	recreation     *setupRecreationFacts
}

func (s *Service) taskSetupAttemptObserver(setupOperationID *serverapi.WorktreeSetupOperationID) (setupAttemptObserver, error) {
	if setupOperationID == nil {
		return setupAttemptObserverFunc(func(serverapi.WorktreeSetupStarted) {}), nil
	}
	operationID := *setupOperationID
	if err := operationID.Validate(); err != nil {
		return nil, err
	}
	return setupAttemptObserverFunc(func(started serverapi.WorktreeSetupStarted) {
		s.publishSetupEvent(serverapi.WorktreeSetupEvent{
			SetupOperationID: operationID,
			Phase:            serverapi.WorktreeSetupPhaseStarted,
			Started:          &started,
		})
	}), nil
}

func (s *Service) runSetupRecovery(ctx context.Context, req setupRecoveryRequest) (setupRecoveryResult, error) {
	if req.Observer == nil {
		return setupRecoveryResult{}, errors.New("setup attempt observer is required")
	}
	attempt, retryConsumed, err := s.prepareSetupAttemptForRecovery(req.Attempt, true)
	if err != nil {
		if result, identified := setupPreparationFailureResult(err, req.Attempt.RetainedWorktree); identified {
			return result, nil
		}
		return setupRecoveryResult{}, err
	}
	if attempt == nil {
		return setupRecoveryResult{
			Result: WorktreeSetupResult{
				NotRequired: &serverapi.WorktreeSetupNotRequired{
					Reason: serverapi.WorktreeSetupNotRequiredNoConfiguredScript,
				},
			},
		}, nil
	}
	if req.RecreateBeforeFirstAttempt {
		var preparationRetried bool
		attempt, preparationRetried, err = s.recreateSetupAttemptIfClean(
			ctx,
			attempt,
			req.Recreate,
			!retryConsumed,
		)
		retryConsumed = retryConsumed || preparationRetried
		if err != nil {
			if result, identified := setupPreparationFailureResult(err, req.Attempt.RetainedWorktree); identified {
				return result, nil
			}
			return setupRecoveryResult{}, err
		}
	}
	firstErr := s.executeSetupAttempt(ctx, *attempt, req.Observer)
	if firstErr == nil {
		return setupRecoveryResult{
			Result: WorktreeSetupResult{Completed: &serverapi.WorktreeSetupCompleted{}},
		}, nil
	}
	if errors.Is(firstErr, context.Canceled) || errors.Is(firstErr, context.DeadlineExceeded) || ctx.Err() != nil {
		return setupRecoveryResult{
			Result: WorktreeSetupResult{Failed: setupFailureFromError(firstErr, attempt.retained)},
			Err:    firstErr,
		}, nil
	}
	if retryConsumed {
		return setupRecoveryResult{
			Result: WorktreeSetupResult{Failed: setupFailureFromError(firstErr, attempt.retained)},
			Err:    firstErr,
		}, nil
	}
	previousAttempt := attempt
	attempt, _, err = s.recreateSetupAttemptIfClean(ctx, attempt, req.Recreate, false)
	if err != nil {
		if result, identified := setupPreparationFailureResult(err, previousAttempt.retained); identified {
			return result, nil
		}
		return setupRecoveryResult{}, err
	}
	finalErr := s.executeSetupAttempt(ctx, *attempt, req.Observer)
	if finalErr == nil {
		return setupRecoveryResult{
			Result: WorktreeSetupResult{Completed: &serverapi.WorktreeSetupCompleted{}},
		}, nil
	}
	return setupRecoveryResult{
		Result: WorktreeSetupResult{Failed: setupFailureFromError(finalErr, attempt.retained)},
		Err:    finalErr,
	}, nil
}

func (s *Service) prepareSetupAttemptForRecovery(
	req setupExecutionRequest,
	retryAvailable bool,
) (*preparedSetupAttempt, bool, error) {
	attempt, err := s.prepareSetupAttempt(req)
	if err == nil || !retryAvailable {
		return attempt, false, err
	}
	if _, identified := setupScriptPathFromError(err); !identified {
		return nil, false, err
	}
	attempt, err = s.prepareSetupAttempt(req)
	return attempt, true, err
}

func setupPreparationFailureResult(
	err error,
	retained *serverapi.WorktreeTopologyEntry,
) (setupRecoveryResult, bool) {
	if _, identified := setupScriptPathFromError(err); !identified {
		return setupRecoveryResult{}, false
	}
	return setupRecoveryResult{
		Result: WorktreeSetupResult{Failed: setupFailureFromError(err, retained)},
		Err:    err,
	}, true
}

func (s *Service) recreateSetupAttemptIfClean(
	ctx context.Context,
	attempt *preparedSetupAttempt,
	recreate func(context.Context) (setupExecutionRequest, error),
	preparationRetryAvailable bool,
) (*preparedSetupAttempt, bool, error) {
	if recreate == nil {
		return attempt, false, nil
	}
	if attempt.recreation == nil {
		return nil, false, errors.New("setup recreation requires recorded checkout topology")
	}
	_, safelyRecreatable, err := s.inspectSafeWorktreeRecreation(
		ctx,
		attempt.payload.SourceWorkspaceRoot,
		attempt.payload.WorktreeRoot,
		attempt.recreation.CreationBaseCommitOID,
		attempt.recreation.RecordedCheckout,
	)
	if err != nil {
		return nil, false, err
	}
	if !safelyRecreatable {
		return attempt, false, nil
	}
	recreated, err := recreate(ctx)
	if err != nil {
		return nil, false, err
	}
	return s.prepareSetupAttemptForRecovery(recreated, preparationRetryAvailable)
}

func (s *Service) prepareSetupAttempt(req setupExecutionRequest) (*preparedSetupAttempt, error) {
	settings := req.ResolvedSettings
	if settings == nil {
		resolved, err := s.worktreeSetupSettings(req.SourceWorkspaceRoot)
		if err != nil {
			return nil, err
		}
		settings = &resolved
	}
	trimmedScript := strings.TrimSpace(settings.SetupScript)
	if trimmedScript == "" {
		return nil, nil
	}
	scriptPath, err := resolveSetupScriptPath(req.SourceWorkspaceRoot, trimmedScript)
	if err != nil {
		if strings.TrimSpace(scriptPath) != "" {
			return nil, &setupScriptError{
				Message:      fmt.Sprintf("resolve worktree setup script: %v", err),
				ScriptPath:   scriptPath,
				WorktreeRoot: strings.TrimSpace(req.WorktreeRoot),
			}
		}
		return nil, fmt.Errorf("resolve worktree setup script: %w", err)
	}
	sessionID, err := normalizeSetupSessionID(req.ScriptPayload.SessionID)
	if err != nil {
		return nil, err
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
	return &preparedSetupAttempt{
		started: serverapi.WorktreeSetupStarted{
			SourceWorkspaceRoot: payload.SourceWorkspaceRoot,
			WorktreeRoot:        payload.WorktreeRoot,
			ScriptPath:          scriptPath,
		},
		scriptPath:     scriptPath,
		payload:        payload,
		timeoutSeconds: settings.SetupTimeoutSeconds,
		retained:       req.RetainedWorktree,
		recreation:     req.Recreation,
	}, nil
}

func (s *Service) inspectSafeWorktreeRecreation(
	ctx context.Context,
	sourceWorkspaceRoot string,
	root string,
	creationBaseCommitOID *string,
	recordedCheckout GitWorktree,
) (GitWorktree, bool, error) {
	identity, err := s.git.ValidateManagedWorktreeIdentity(ctx, ManagedWorktreeIdentitySpec{
		SourceWorkspaceRoot:  sourceWorkspaceRoot,
		ExpectedWorktreeRoot: root,
	})
	if err != nil {
		return GitWorktree{}, false, err
	}
	revision, err := s.git.ResolveHEAD(ctx, root)
	if err != nil {
		return GitWorktree{}, false, err
	}
	live := GitWorktree{
		Root:     root,
		HeadOID:  revision.CommitOID,
		Branch:   identity.branch,
		Detached: identity.branch == nil,
	}
	creationBase, err := normalizeOptionalCommitOID(creationBaseCommitOID)
	if err != nil {
		return GitWorktree{}, false, err
	}
	if creationBase == nil ||
		revision.CommitOID != *creationBase ||
		!sameWorktreeBranchTopology(recordedCheckout, live) {
		return live, false, nil
	}
	dirtyState, err := s.git.ProbeRecreationDirtyState(ctx, root)
	if err != nil {
		return GitWorktree{}, false, err
	}
	return live, dirtyState.Kind == clientui.WorktreeDirtyStateClean, nil
}

func (s *Service) executeSetupAttempt(ctx context.Context, attempt preparedSetupAttempt, observer setupAttemptObserver) error {
	observer.ObserveSetupAttempt(attempt.started)
	return s.runSetupScript(ctx, attempt.scriptPath, attempt.payload, attempt.timeoutSeconds)
}

func setupFailureFromError(err error, retained *serverapi.WorktreeTopologyEntry) *serverapi.WorktreeSetupFailed {
	failed := &serverapi.WorktreeSetupFailed{
		RetryReadiness: serverapi.WorktreeSetupRetryReady,
		Cause: serverapi.WorktreeSetupFailureCause{
			Kind:        serverapi.WorktreeSetupFailureOperational,
			Operational: &serverapi.WorktreeSetupOperationalFailure{},
		},
		Diagnostic:       err.Error(),
		RetainedWorktree: retained,
	}
	if scriptPath, identified := setupScriptPathFromError(err); identified {
		failed.ScriptPath = &scriptPath
	}
	var setupErr *setupScriptError
	if !errors.As(err, &setupErr) {
		return failed
	}
	stdout := setupErr.Stdout
	stderr := setupErr.Stderr
	switch {
	case setupErr.Timeout:
		failed.Cause = serverapi.WorktreeSetupFailureCause{
			Kind: serverapi.WorktreeSetupFailureTimeout,
			Timeout: &serverapi.WorktreeSetupTimeout{
				Stdout: &stdout,
				Stderr: &stderr,
			},
		}
	case setupErr.Canceled:
		failed.RetryReadiness = serverapi.WorktreeSetupNonRetryable
		failed.Cause = serverapi.WorktreeSetupFailureCause{
			Kind:     serverapi.WorktreeSetupFailureCanceled,
			Canceled: &serverapi.WorktreeSetupCanceled{},
		}
	case setupErr.ExitCode != nil:
		failed.Cause = serverapi.WorktreeSetupFailureCause{
			Kind: serverapi.WorktreeSetupFailureProcessExit,
			ProcessExit: &serverapi.WorktreeSetupProcessExit{
				ExitCode: *setupErr.ExitCode,
				Stdout:   &stdout,
				Stderr:   &stderr,
			},
		}
	}
	return failed
}

func (s *Service) runSetupForWorktree(ctx context.Context, operationID serverapi.WorktreeSetupOperationID, req setupExecutionRequest) error {
	if err := operationID.Validate(); err != nil {
		return err
	}
	attempt, err := s.prepareSetupAttempt(req)
	if err != nil {
		s.publishSetupEvent(serverapi.WorktreeSetupEvent{
			SetupOperationID: operationID,
			Phase:            serverapi.WorktreeSetupPhaseFailed,
			Failed: &serverapi.WorktreeSetupFailed{
				RetryReadiness: serverapi.WorktreeSetupNonRetryable,
				Cause: serverapi.WorktreeSetupFailureCause{
					Kind:        serverapi.WorktreeSetupFailureOperational,
					Operational: &serverapi.WorktreeSetupOperationalFailure{},
				},
				Diagnostic: err.Error(),
			},
		})
		return err
	}
	if attempt == nil {
		return nil
	}
	observer := setupAttemptObserverFunc(func(started serverapi.WorktreeSetupStarted) {
		s.publishSetupEvent(serverapi.WorktreeSetupEvent{
			SetupOperationID: operationID,
			Phase:            serverapi.WorktreeSetupPhaseStarted,
			Started:          &started,
		})
	})
	if err := s.executeSetupAttempt(ctx, *attempt, observer); err != nil {
		s.publishSetupEvent(serverapi.WorktreeSetupEvent{
			SetupOperationID: operationID,
			Phase:            serverapi.WorktreeSetupPhaseFailed,
			Failed:           setupFailureFromError(err, attempt.retained),
		})
		return err
	}
	s.publishSetupEvent(serverapi.WorktreeSetupEvent{
		SetupOperationID: operationID,
		Phase:            serverapi.WorktreeSetupPhaseCompleted,
		Completed:        &serverapi.WorktreeSetupCompleted{},
	})
	return nil
}

func (s *Service) worktreeSetupSettings(sourceWorkspaceRoot string) (config.WorktreeSettings, error) {
	if s.resolveSetup == nil {
		return config.WorktreeSettings{
			SetupScript:         s.setupScript,
			SetupTimeoutSeconds: s.setupTimeoutSeconds,
		}, nil
	}
	settings, err := s.resolveSetup(sourceWorkspaceRoot)
	if err != nil {
		return config.WorktreeSettings{}, fmt.Errorf("resolve worktree setup settings: %w", err)
	}
	return settings, nil
}

func (s *Service) runSetupScript(ctx context.Context, scriptPath string, payload setupScriptPayload, timeoutSeconds int) error {
	setupCtx := ctx
	var cancel context.CancelFunc
	if timeoutSeconds > 0 {
		setupCtx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
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
	stdout, err := boundedio.NewWriter(setupDiagnosticLimitBytes)
	if err != nil {
		return &setupScriptError{Message: fmt.Sprintf("initialize setup stdout capture: %v", err), ScriptPath: scriptPath, WorktreeRoot: payload.WorktreeRoot}
	}
	stderr, err := boundedio.NewWriter(setupDiagnosticLimitBytes)
	if err != nil {
		return &setupScriptError{Message: fmt.Sprintf("initialize setup stderr capture: %v", err), ScriptPath: scriptPath, WorktreeRoot: payload.WorktreeRoot}
	}
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
			setupErr.TimeoutSeconds = timeoutSeconds
			setupErr.Message = fmt.Sprintf("timed out after %s", time.Duration(timeoutSeconds)*time.Second)
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

func (s *Service) PublishWorkflowTaskSetupEvent(evt serverapi.WorktreeSetupEvent) {
	s.publishSetupEvent(evt)
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

func setupScriptPathFromError(err error) (string, bool) {
	var setupErr *setupScriptError
	if !errors.As(err, &setupErr) {
		return "", false
	}
	return setupErr.ScriptPath, strings.TrimSpace(setupErr.ScriptPath) != ""
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
