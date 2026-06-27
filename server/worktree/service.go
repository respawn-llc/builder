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
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"github.com/google/uuid"
)

const setupScriptTimeout = 20 * time.Second

const rollbackSessionTargetTimeout = 5 * time.Second

type runtimeController interface {
	SyncExecutionTarget(ctx context.Context, sessionID string, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error
	HasActiveRun(ctx context.Context, sessionID string) (bool, error)
}

type worktreeLease interface {
	Release()
}

type leaseFunc func()

func (f leaseFunc) Release() {
	if f != nil {
		f()
	}
}

type activeRuntimeSource interface {
	IsSessionRuntimeActive(sessionID string) bool
}

type processSource interface {
	List() []shelltool.Snapshot
}

type localEntryAppender interface {
	AppendCommittedEntry(ctx context.Context, req serverapi.RuntimeAppendCommittedEntryRequest) error
	AppendSessionEntry(ctx context.Context, sessionID string, role string, text string) error
}

type ServiceOptions struct {
	BaseDir     string
	SetupScript string
}

type Service struct {
	metadata    *metadata.Store
	git         *GitInspector
	runtime     runtimeController
	active      activeRuntimeSource
	processes   processSource
	localNotes  localEntryAppender
	baseDir     string
	setupScript string

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
	SourceWorkspaceRoot string `json:"source_workspace_root"`
	BranchName          string `json:"branch_name"`
	WorktreeRoot        string `json:"worktree_root"`
	SessionID           string `json:"session_id"`
	ProjectID           string `json:"project_id"`
	WorkspaceID         string `json:"workspace_id"`
	WorktreeID          string `json:"worktree_id"`
	CreatedBranch       bool   `json:"created_branch"`
}

type EnsureTaskWorktreeRequest struct {
	TaskID string
}

type EnsureTaskWorktreeResponse struct {
	Worktree      serverapi.WorktreeView
	Created       bool
	CreatedBranch bool
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
		metadata:       metadataStore,
		git:            gitInspector,
		runtime:        runtime,
		active:         active,
		processes:      processes,
		localNotes:     localNotes,
		baseDir:        strings.TrimSpace(opts.BaseDir),
		setupScript:    strings.TrimSpace(opts.SetupScript),
		workspaceLocks: make(map[string]*workspaceMutationLock),
	}
}

func (s *Service) EnsureTaskWorktree(ctx context.Context, req EnsureTaskWorktreeRequest) (resp EnsureTaskWorktreeResponse, err error) {
	if s == nil || s.metadata == nil || s.git == nil {
		return EnsureTaskWorktreeResponse{}, errors.New("worktree service dependencies are required")
	}
	taskID := strings.TrimSpace(req.TaskID)
	if taskID == "" {
		return EnsureTaskWorktreeResponse{}, errors.New("task_id is required")
	}
	task, err := s.metadata.Queries().GetTask(ctx, taskID)
	if err != nil {
		return EnsureTaskWorktreeResponse{}, err
	}
	if task.ManagedWorktreeID.Valid && strings.TrimSpace(task.ManagedWorktreeID.String) != "" {
		view, err := s.taskManagedWorktreeView(ctx, strings.TrimSpace(task.ManagedWorktreeID.String))
		if err != nil {
			return EnsureTaskWorktreeResponse{}, err
		}
		return EnsureTaskWorktreeResponse{Worktree: view}, nil
	}
	workspace, err := s.taskSourceWorkspace(ctx, task.ProjectID, task.SourceWorkspaceID.String)
	if err != nil {
		return EnsureTaskWorktreeResponse{}, err
	}
	release := s.acquireWorkspaceMutationLock(workspace.WorkspaceID)
	defer release.Release()
	task, err = s.metadata.Queries().GetTask(ctx, taskID)
	if err != nil {
		return EnsureTaskWorktreeResponse{}, err
	}
	if task.ManagedWorktreeID.Valid && strings.TrimSpace(task.ManagedWorktreeID.String) != "" {
		view, err := s.taskManagedWorktreeView(ctx, strings.TrimSpace(task.ManagedWorktreeID.String))
		if err != nil {
			return EnsureTaskWorktreeResponse{}, err
		}
		return EnsureTaskWorktreeResponse{Worktree: view}, nil
	}
	createSpec, err := normalizeCreateSpec(CreateSpec{BaseRef: "HEAD", CreateBranch: true, BranchName: task.ShortID})
	if err != nil {
		return EnsureTaskWorktreeResponse{}, err
	}
	resolution, err := s.git.ResolveCreateTarget(ctx, workspace.RootPath, createSpec.BranchName)
	if err != nil {
		return EnsureTaskWorktreeResponse{}, err
	}
	if resolution.Kind != CreateTargetResolutionKindNewBranch {
		return EnsureTaskWorktreeResponse{}, &TaskBranchCollisionError{BranchName: createSpec.BranchName, ResolvedRef: resolution.ResolvedRef}
	}
	worktreeRoot, err := s.resolveRequestedWorktreeRoot("", workspace.WorkspaceID, createSpec)
	if err != nil {
		return EnsureTaskWorktreeResponse{}, err
	}
	cleanup := failedCreateCleanup{
		active:        false,
		workspaceID:   workspace.WorkspaceID,
		workspaceRoot: workspace.RootPath,
		worktreeRoot:  worktreeRoot,
		branchName:    createSpec.BranchName,
		createdBranch: true,
	}
	defer func() {
		if err == nil || !cleanup.active {
			return
		}
		if cleanupErr := s.cleanupFailedCreate(ctx, cleanup); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()
	createdBranch, err := s.git.Add(ctx, workspace.RootPath, worktreeRoot, createSpec)
	if err != nil {
		return EnsureTaskWorktreeResponse{}, err
	}
	cleanup.active = true
	cleanup.createdBranch = createdBranch
	worktreeRoot, err = config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		return EnsureTaskWorktreeResponse{}, err
	}
	cleanup.worktreeRoot = worktreeRoot
	synced, err := s.syncWorkspace(ctx, workspace.WorkspaceID, workspace.RootPath, false)
	if err != nil {
		return EnsureTaskWorktreeResponse{}, err
	}
	created, ok := findSyncedWorktreeByRoot(synced, worktreeRoot)
	if !ok {
		return EnsureTaskWorktreeResponse{}, fmt.Errorf("created task worktree %q was not discovered after git sync: %w", worktreeRoot, serverapi.ErrWorktreeNotFound)
	}
	created.record.Managed = true
	created.record.CreatedBranch = createdBranch
	created.record.UpdatedAt = time.Now().UTC()
	cleanup.worktreeID = strings.TrimSpace(created.record.ID)
	if err := s.metadata.UpsertWorktreeRecord(ctx, created.record); err != nil {
		return EnsureTaskWorktreeResponse{}, err
	}
	if updated, err := s.metadata.Queries().UpdateTaskManagedWorktree(ctx, sqlitegen.UpdateTaskManagedWorktreeParams{ID: taskID, ManagedWorktreeID: sql.NullString{String: created.record.ID, Valid: true}, UpdatedAtUnixMs: time.Now().UTC().UnixMilli()}); err != nil {
		return EnsureTaskWorktreeResponse{}, err
	} else if updated != 1 {
		return EnsureTaskWorktreeResponse{}, sql.ErrNoRows
	}
	cleanup.active = false
	return EnsureTaskWorktreeResponse{Worktree: worktreeViewFromSynced(created, clientui.SessionExecutionTarget{}), Created: true, CreatedBranch: createdBranch}, nil
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
	defer release.Release()
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
	gitMetadata, err := worktreeGitMetadataFromRecord(record)
	if err != nil {
		return serverapi.WorktreeView{}, err
	}
	return worktreeViewFromSynced(syncedWorktree{record: record, git: gitMetadata}, clientui.SessionExecutionTarget{}), nil
}

func (s *Service) ListWorktrees(ctx context.Context, req serverapi.WorktreeListRequest) (serverapi.WorktreeListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorktreeListResponse{}, err
	}
	release, workspaceCtx, err := s.beginMutation(ctx, req.SessionID)
	if err != nil {
		return serverapi.WorktreeListResponse{}, err
	}
	defer release.Release()
	synced, err := s.syncWorkspace(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot, req.IncludeDirtyCount)
	if err != nil {
		return serverapi.WorktreeListResponse{}, err
	}
	workspaceCtx.target, err = s.metadata.ResolveSessionExecutionTarget(ctx, req.SessionID)
	if err != nil {
		return serverapi.WorktreeListResponse{}, err
	}
	return serverapi.WorktreeListResponse{Target: workspaceCtx.target, Worktrees: mapSyncedWorktrees(synced, workspaceCtx.target)}, nil
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
	defer release.Release()
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
	previous := currentSyncedWorktree(synced, workspaceCtx.target)
	nextTarget, err := s.switchSessionTarget(ctx, workspaceCtx, previous, created)
	if err != nil {
		return serverapi.WorktreeCreateResponse{}, err
	}
	setupScheduled := s.scheduleSetupScript(workspaceCtx, created, strings.TrimSpace(created.git.BranchName), createdBranch)
	createdView := worktreeViewFromSynced(created, nextTarget)
	createdView.Managed = true
	createdView.CreatedBranch = createdBranch
	createdView.OriginSessionID = workspaceCtx.sessionID
	cleanup.active = false
	return serverapi.WorktreeCreateResponse{Target: nextTarget, Worktree: createdView, CreatedBranch: createdBranch, SetupScheduled: setupScheduled}, nil
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
	defer release.Release()
	synced, err := s.syncWorkspace(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot, false)
	if err != nil {
		return serverapi.WorktreeSwitchResponse{}, err
	}
	targetWorktree, ok := findSyncedWorktreeByID(synced, req.WorktreeID)
	if !ok {
		return serverapi.WorktreeSwitchResponse{}, serverapi.ErrWorktreeNotFound
	}
	previous := currentSyncedWorktree(synced, workspaceCtx.target)
	nextTarget, err := s.switchSessionTarget(ctx, workspaceCtx, previous, targetWorktree)
	if err != nil {
		return serverapi.WorktreeSwitchResponse{}, err
	}
	return serverapi.WorktreeSwitchResponse{Target: nextTarget, Worktree: worktreeViewFromSynced(targetWorktree, nextTarget)}, nil
}

func (s *Service) DeleteWorktree(ctx context.Context, req serverapi.WorktreeDeleteRequest) (serverapi.WorktreeDeleteResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorktreeDeleteResponse{}, err
	}
	release, workspaceCtx, err := s.beginMutation(ctx, req.SessionID)
	if err != nil {
		return serverapi.WorktreeDeleteResponse{}, err
	}
	defer release.Release()
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
	if workspaceCtx.target.WorktreeID == targetWorktree.record.ID {
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
	return serverapi.WorktreeDeleteResponse{Target: finalTarget, Worktree: worktreeViewFromSynced(targetWorktree, finalTarget), BranchDeleted: branchDeleted, BranchCleanupMessage: branchCleanupMessage}, nil
}

func (s *Service) beginMutation(ctx context.Context, sessionID string) (worktreeLease, sessionWorkspaceContext, error) {
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
			workspaceLease.Release()
			return nil, sessionWorkspaceContext{}, err
		}
		if strings.TrimSpace(lockedWorkspaceCtx.workspaceID) == strings.TrimSpace(workspaceCtx.workspaceID) {
			return workspaceLease, lockedWorkspaceCtx, nil
		}
		workspaceLease.Release()
	}
}

func (s *Service) acquireWorkspaceMutationLock(workspaceID string) worktreeLease {
	trimmedWorkspaceID := strings.TrimSpace(workspaceID)
	if s == nil || trimmedWorkspaceID == "" {
		return leaseFunc(func() {})
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
	return leaseFunc(func() {
		once.Do(func() {
			lock.mu.Unlock()
			s.workspaceMu.Lock()
			defer s.workspaceMu.Unlock()
			lock.refs--
			if lock.refs == 0 {
				delete(s.workspaceLocks, trimmedWorkspaceID)
			}
		})
	})
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

type worktreeSessionRetargetFilter func(metadata.WorktreeSessionBlocker) bool

type worktreeReminderFactory func(metadata.WorktreeRecord, clientui.SessionExecutionTarget) (session.WorktreeReminderState, error)

type worktreeSessionRetargetOptions struct {
	filter          worktreeSessionRetargetFilter
	reminder        worktreeReminderFactory
	rollbackOnError bool
}

type pendingWorktreeSessionRetarget struct {
	sessionID      string
	previousTarget clientui.SessionExecutionTarget
}

func (s *Service) retargetActiveSessionsFromDeletedWorktree(ctx context.Context, workspaceID string, workspaceRoot string, worktree metadata.WorktreeRecord, currentSessionID string) error {
	trimmedCurrentSessionID := strings.TrimSpace(currentSessionID)
	return s.retargetSessionsFromWorktree(ctx, workspaceID, workspaceRoot, worktree, worktreeSessionRetargetOptions{
		filter: func(blocker metadata.WorktreeSessionBlocker) bool {
			sessionID := strings.TrimSpace(blocker.SessionID)
			if sessionID == "" || sessionID == trimmedCurrentSessionID {
				return false
			}
			return s.active != nil && s.active.IsSessionRuntimeActive(sessionID)
		},
		reminder:        worktreeReminderStateForExitedWorktree,
		rollbackOnError: true,
	})
}

func (s *Service) retargetSessionsFromWorktree(ctx context.Context, workspaceID string, workspaceRoot string, worktree metadata.WorktreeRecord, options worktreeSessionRetargetOptions) error {
	if s == nil || s.metadata == nil || s.runtime == nil {
		return errors.New("worktree service dependencies are required")
	}
	trimmedWorkspaceID := strings.TrimSpace(workspaceID)
	trimmedWorkspaceRoot := strings.TrimSpace(workspaceRoot)
	trimmedWorktreeID := strings.TrimSpace(worktree.ID)
	if trimmedWorkspaceID == "" || trimmedWorkspaceRoot == "" || trimmedWorktreeID == "" {
		return nil
	}
	blockers, err := s.metadata.ListSessionsTargetingWorktree(ctx, trimmedWorktreeID)
	if err != nil {
		return err
	}
	reminderFactory := options.reminder
	if reminderFactory == nil {
		reminderFactory = worktreeReminderStateForExitedWorktree
	}
	pending := make([]pendingWorktreeSessionRetarget, 0, len(blockers))
	collected := make([]error, 0)
	appendErr := func(sessionID string, err error) {
		collected = append(collected, fmt.Errorf("retarget session %q from worktree %q: %w", strings.TrimSpace(sessionID), trimmedWorktreeID, err))
	}
	for _, blocker := range blockers {
		if options.filter != nil && !options.filter(blocker) {
			continue
		}
		previousTarget, err := s.metadata.ResolveSessionExecutionTarget(ctx, blocker.SessionID)
		if err != nil {
			appendErr(blocker.SessionID, err)
			continue
		}
		cwdRelpath := clampCwdRelpath(previousTarget.CwdRelpath, trimmedWorkspaceRoot)
		if err := s.metadata.UpdateSessionExecutionTargetByID(ctx, blocker.SessionID, trimmedWorkspaceID, "", cwdRelpath); err != nil {
			appendErr(blocker.SessionID, err)
			if options.rollbackOnError {
				return errors.Join(errors.Join(collected...), s.rollbackRetargetedSessions(ctx, trimmedWorkspaceID, pending))
			}
			continue
		}
		pending = append(pending, pendingWorktreeSessionRetarget{sessionID: blocker.SessionID, previousTarget: previousTarget})
	}
	for _, item := range pending {
		nextTarget, err := s.metadata.ResolveSessionExecutionTarget(ctx, item.sessionID)
		if err != nil {
			appendErr(item.sessionID, err)
			if options.rollbackOnError {
				return errors.Join(errors.Join(collected...), s.rollbackRetargetedSessions(ctx, trimmedWorkspaceID, pending))
			}
			continue
		}
		reminder, err := reminderFactory(worktree, nextTarget)
		if err != nil {
			appendErr(item.sessionID, err)
			if options.rollbackOnError {
				return errors.Join(errors.Join(collected...), s.rollbackRetargetedSessions(ctx, trimmedWorkspaceID, pending))
			}
			continue
		}
		if err := s.runtime.SyncExecutionTarget(ctx, item.sessionID, nextTarget, &reminder); err != nil {
			appendErr(item.sessionID, err)
			if options.rollbackOnError {
				return errors.Join(errors.Join(collected...), s.rollbackRetargetedSessions(ctx, trimmedWorkspaceID, pending))
			}
			rollbackCtx, cancel := liveRollbackContext(ctx)
			rollbackErr := s.metadata.UpdateSessionExecutionTargetByID(rollbackCtx, item.sessionID, trimmedWorkspaceID, item.previousTarget.WorktreeID, item.previousTarget.CwdRelpath)
			cancel()
			if rollbackErr != nil {
				appendErr(item.sessionID, errors.Join(err, fmt.Errorf("rollback execution target after runtime sync failure: %w", rollbackErr)))
				continue
			}
			continue
		}
	}
	return errors.Join(collected...)
}

func (s *Service) rollbackRetargetedSessions(ctx context.Context, workspaceID string, pending []pendingWorktreeSessionRetarget) error {
	if len(pending) == 0 {
		return nil
	}
	collected := make([]error, 0)
	for i := len(pending) - 1; i >= 0; i-- {
		item := pending[i]
		sessionID := strings.TrimSpace(item.sessionID)
		rollbackCtx, cancel := liveRollbackContext(ctx)
		if err := s.metadata.UpdateSessionExecutionTargetByID(rollbackCtx, sessionID, workspaceID, item.previousTarget.WorktreeID, item.previousTarget.CwdRelpath); err != nil {
			collected = append(collected, fmt.Errorf("rollback session %q execution target: %w", sessionID, err))
			cancel()
			continue
		}
		if s.active != nil && s.active.IsSessionRuntimeActive(sessionID) {
			if err := s.runtime.SyncExecutionTarget(rollbackCtx, sessionID, item.previousTarget, nil); err != nil {
				collected = append(collected, fmt.Errorf("rollback session %q runtime target: %w", sessionID, err))
			}
		}
		cancel()
	}
	return errors.Join(collected...)
}

func shouldResetWorktreeProvenance(record metadata.WorktreeRecord, gitEntry GitWorktree) bool {
	if !record.Managed && !record.CreatedBranch && strings.TrimSpace(record.OriginSessionID) == "" {
		return false
	}
	if gitEntry.Detached || (strings.TrimSpace(gitEntry.BranchRef) == "" && !gitEntry.IsMain) {
		return true
	}
	previousGit, err := worktreeGitMetadataFromRecord(record)
	if err != nil {
		return false
	}
	if !worktreeHasStableIdentity(previousGit) {
		return false
	}
	if previousGit.IsMain != gitEntry.IsMain || previousGit.Detached != gitEntry.Detached || previousGit.Bare != gitEntry.Bare {
		return true
	}
	return strings.TrimSpace(previousGit.BranchRef) != strings.TrimSpace(gitEntry.BranchRef)
}

func worktreeHasStableIdentity(entry GitWorktree) bool {
	return strings.TrimSpace(entry.BranchRef) != "" || strings.TrimSpace(entry.HeadOID) != "" || entry.Detached || entry.IsMain || entry.Bare
}

func (s *Service) switchSessionTarget(ctx context.Context, workspaceCtx sessionWorkspaceContext, previous *syncedWorktree, next syncedWorktree) (clientui.SessionExecutionTarget, error) {
	nextWorktreeID := strings.TrimSpace(next.record.ID)
	nextBaseRoot := strings.TrimSpace(next.record.CanonicalRoot)
	if next.git.IsMain {
		nextWorktreeID = ""
		nextBaseRoot = workspaceCtx.workspaceRoot
	}
	previousTarget := workspaceCtx.target
	cwdRelpath := clampCwdRelpath(previousTarget.CwdRelpath, nextBaseRoot)
	if err := s.metadata.UpdateSessionExecutionTargetByID(ctx, workspaceCtx.sessionID, workspaceCtx.workspaceID, nextWorktreeID, cwdRelpath); err != nil {
		return clientui.SessionExecutionTarget{}, err
	}
	nextTarget, err := s.metadata.ResolveSessionExecutionTarget(ctx, workspaceCtx.sessionID)
	if err != nil {
		s.rollbackSessionTarget(ctx, workspaceCtx, previousTarget)
		return clientui.SessionExecutionTarget{}, err
	}
	if reminder, ok := worktreeReminderStateForTransition(previous, previousTarget, next, nextTarget); ok {
		if err := s.applySessionTarget(ctx, workspaceCtx.sessionID, nextTarget, &reminder); err != nil {
			s.rollbackSessionTarget(ctx, workspaceCtx, previousTarget)
			return clientui.SessionExecutionTarget{}, err
		}
		return nextTarget, nil
	}
	if err := s.applySessionTarget(ctx, workspaceCtx.sessionID, nextTarget, nil); err != nil {
		s.rollbackSessionTarget(ctx, workspaceCtx, previousTarget)
		return clientui.SessionExecutionTarget{}, err
	}
	return nextTarget, nil
}

func (s *Service) applySessionTarget(ctx context.Context, sessionID string, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
	return s.runtime.SyncExecutionTarget(ctx, sessionID, target, reminder)
}

func (s *Service) rollbackSessionTarget(ctx context.Context, workspaceCtx sessionWorkspaceContext, previousTarget clientui.SessionExecutionTarget) {
	rollbackCtx, cancel := liveRollbackContext(ctx)
	defer cancel()
	_ = s.metadata.UpdateSessionExecutionTargetByID(rollbackCtx, workspaceCtx.sessionID, workspaceCtx.workspaceID, previousTarget.WorktreeID, previousTarget.CwdRelpath)
	if strings.TrimSpace(previousTarget.EffectiveWorkdir) != "" {
		_ = s.runtime.SyncExecutionTarget(rollbackCtx, workspaceCtx.sessionID, previousTarget, nil)
	}
}

func liveRollbackContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.WithTimeout(context.Background(), rollbackSessionTargetTimeout)
	}
	return context.WithTimeout(context.WithoutCancel(ctx), rollbackSessionTargetTimeout)
}

func worktreeReminderStateForTransition(previous *syncedWorktree, previousTarget clientui.SessionExecutionTarget, next syncedWorktree, nextTarget clientui.SessionExecutionTarget) (session.WorktreeReminderState, bool) {
	if next.git.IsMain {
		if previous == nil || strings.TrimSpace(previousTarget.WorktreeID) == "" || previous.git.IsMain {
			return session.WorktreeReminderState{}, false
		}
		return session.WorktreeReminderState{
			Mode:          session.WorktreeReminderModeExit,
			Branch:        strings.TrimSpace(previous.git.BranchName),
			WorktreePath:  strings.TrimSpace(previous.record.CanonicalRoot),
			WorkspaceRoot: strings.TrimSpace(nextTarget.WorkspaceRoot),
			EffectiveCwd:  strings.TrimSpace(nextTarget.EffectiveWorkdir),
		}, true
	}
	return session.WorktreeReminderState{
		Mode:          session.WorktreeReminderModeEnter,
		Branch:        strings.TrimSpace(next.git.BranchName),
		WorktreePath:  strings.TrimSpace(next.record.CanonicalRoot),
		WorkspaceRoot: strings.TrimSpace(nextTarget.WorkspaceRoot),
		EffectiveCwd:  strings.TrimSpace(nextTarget.EffectiveWorkdir),
	}, true
}

func worktreeReminderStateForExitedWorktree(worktree metadata.WorktreeRecord, nextTarget clientui.SessionExecutionTarget) (session.WorktreeReminderState, error) {
	gitMetadata, err := worktreeGitMetadataFromRecord(worktree)
	if err != nil {
		return session.WorktreeReminderState{}, err
	}
	branchName := strings.TrimSpace(gitMetadata.BranchName)
	if branchName == "" {
		branchName = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(gitMetadata.BranchRef), "refs/heads/"))
	}
	return session.WorktreeReminderState{
		Mode:          session.WorktreeReminderModeExit,
		Branch:        branchName,
		WorktreePath:  strings.TrimSpace(worktree.CanonicalRoot),
		WorkspaceRoot: strings.TrimSpace(nextTarget.WorkspaceRoot),
		EffectiveCwd:  strings.TrimSpace(nextTarget.EffectiveWorkdir),
	}, nil
}

func worktreeGitMetadataFromRecord(worktree metadata.WorktreeRecord) (GitWorktree, error) {
	metadataJSON := strings.TrimSpace(worktree.GitMetadataJSON)
	if metadataJSON == "" {
		return GitWorktree{}, nil
	}
	var gitMetadata GitWorktree
	if err := json.Unmarshal([]byte(metadataJSON), &gitMetadata); err != nil {
		return GitWorktree{}, fmt.Errorf("decode git worktree metadata: %w", err)
	}
	gitMetadata.IsMain = worktree.IsMain
	return gitMetadata, nil
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
		active, err := s.runtime.HasActiveRun(ctx, sessionID)
		if err != nil {
			return func() {}, err
		}
		if active {
			otherSessions = append(otherSessions, blocker)
		}
	}
	if len(otherSessions) > 0 {
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
		return func() {}, errors.Join(serverapi.ErrWorktreeBlocked, fmt.Errorf("worktree has active background processes: %s", strings.Join(processBlockers, ", ")))
	}
	return func() {}, nil
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

func (s *Service) scheduleSetupScript(workspaceCtx sessionWorkspaceContext, created syncedWorktree, branchName string, createdBranch bool) bool {
	trimmedScript := strings.TrimSpace(s.setupScript)
	if trimmedScript == "" {
		return false
	}
	scriptPath, err := resolveSetupScriptPath(workspaceCtx.workspaceRoot, trimmedScript)
	if err != nil {
		s.appendLocalNote(context.Background(), workspaceCtx.sessionID, fmt.Sprintf("Worktree setup script skipped: %v", err))
		return false
	}
	payload := setupScriptPayload{
		SourceWorkspaceRoot: workspaceCtx.workspaceRoot,
		BranchName:          strings.TrimSpace(branchName),
		WorktreeRoot:        created.record.CanonicalRoot,
		SessionID:           workspaceCtx.sessionID,
		ProjectID:           workspaceCtx.projectID,
		WorkspaceID:         workspaceCtx.workspaceID,
		WorktreeID:          created.record.ID,
		CreatedBranch:       createdBranch,
	}
	go s.runSetupScript(scriptPath, workspaceCtx.sessionID, payload)
	return true
}

func (s *Service) runSetupScript(scriptPath string, sessionID string, payload setupScriptPayload) {
	ctx, cancel := context.WithTimeout(context.Background(), setupScriptTimeout)
	defer cancel()
	body, err := json.Marshal(payload)
	if err != nil {
		s.appendSessionNote(context.Background(), sessionID, fmt.Sprintf("Worktree setup script failed before start: %v", err))
		return
	}
	cmd := exec.CommandContext(ctx, scriptPath, payload.SourceWorkspaceRoot, payload.BranchName, payload.WorktreeRoot)
	cmd.Dir = payload.WorktreeRoot
	cmd.Stdin = strings.NewReader(string(body))
	cmd.Env = append(os.Environ(),
		"KENT_WORKTREE_SOURCE_WORKSPACE_ROOT="+payload.SourceWorkspaceRoot,
		"KENT_WORKTREE_BRANCH_NAME="+payload.BranchName,
		"KENT_WORKTREE_ROOT="+payload.WorktreeRoot,
		"KENT_WORKTREE_SESSION_ID="+payload.SessionID,
		"KENT_WORKTREE_PROJECT_ID="+payload.ProjectID,
		"KENT_WORKTREE_WORKSPACE_ID="+payload.WorkspaceID,
		"KENT_WORKTREE_WORKTREE_ID="+payload.WorktreeID,
		fmt.Sprintf("KENT_WORKTREE_CREATED_BRANCH=%t", payload.CreatedBranch),
		"KENT_WORKTREE_PAYLOAD_JSON="+string(body),
	)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return
	}
	detail := strings.TrimSpace(string(output))
	if ctx.Err() != nil {
		s.appendSessionNote(context.Background(), sessionID, fmt.Sprintf("Worktree setup timed out for %s", payload.WorktreeRoot))
		return
	}
	if detail == "" {
		detail = err.Error()
	}
	s.appendSessionNote(context.Background(), sessionID, fmt.Sprintf("Worktree setup failed for %s: %s", payload.WorktreeRoot, detail))
}

func (s *Service) appendLocalNote(ctx context.Context, sessionID string, text string) {
	trimmedText := strings.TrimSpace(text)
	if s == nil || s.localNotes == nil || trimmedText == "" {
		return
	}
	_ = s.localNotes.AppendCommittedEntry(ctx, serverapi.RuntimeAppendCommittedEntryRequest{
		ClientRequestID: uuid.NewString(),
		SessionID:       strings.TrimSpace(sessionID),
		Role:            "system",
		Text:            trimmedText,
	})
}

func (s *Service) appendSessionNote(ctx context.Context, sessionID string, text string) {
	trimmedText := strings.TrimSpace(text)
	if s == nil || s.localNotes == nil || trimmedText == "" {
		return
	}
	_ = s.localNotes.AppendSessionEntry(ctx, strings.TrimSpace(sessionID), "system", trimmedText)
}

func mapSyncedWorktrees(items []syncedWorktree, target clientui.SessionExecutionTarget) []serverapi.WorktreeView {
	out := make([]serverapi.WorktreeView, 0, len(items))
	for _, item := range items {
		out = append(out, worktreeViewFromSynced(item, target))
	}
	return out
}

func worktreeViewFromSynced(item syncedWorktree, target clientui.SessionExecutionTarget) serverapi.WorktreeView {
	isCurrent := strings.TrimSpace(target.WorktreeID) == strings.TrimSpace(item.record.ID)
	if strings.TrimSpace(target.WorktreeID) == "" && item.git.IsMain {
		isCurrent = true
	}
	return serverapi.WorktreeView{
		WorktreeID:      item.record.ID,
		DisplayName:     item.record.DisplayName,
		CanonicalRoot:   item.record.CanonicalRoot,
		Availability:    item.record.Availability,
		BranchRef:       item.git.BranchRef,
		BranchName:      item.git.BranchName,
		Detached:        item.git.Detached,
		LockedReason:    item.git.LockedReason,
		PrunableReason:  item.git.PrunableReason,
		DirtyFileCount:  item.git.DirtyFileCount,
		IsMain:          item.git.IsMain,
		IsCurrent:       isCurrent,
		Managed:         item.record.Managed,
		CreatedBranch:   item.record.CreatedBranch,
		OriginSessionID: item.record.OriginSessionID,
	}
}

func findSyncedWorktreeByID(items []syncedWorktree, worktreeID string) (syncedWorktree, bool) {
	trimmedID := strings.TrimSpace(worktreeID)
	for _, item := range items {
		if strings.TrimSpace(item.record.ID) == trimmedID {
			return item, true
		}
	}
	return syncedWorktree{}, false
}

func findSyncedWorktreeByRoot(items []syncedWorktree, worktreeRoot string) (syncedWorktree, bool) {
	trimmedRoot := strings.TrimSpace(worktreeRoot)
	for _, item := range items {
		if strings.TrimSpace(item.record.CanonicalRoot) == trimmedRoot {
			return item, true
		}
	}
	return syncedWorktree{}, false
}

func findMainWorktree(items []syncedWorktree) (syncedWorktree, bool) {
	for _, item := range items {
		if item.git.IsMain {
			return item, true
		}
	}
	return syncedWorktree{}, false
}

func currentSyncedWorktree(items []syncedWorktree, target clientui.SessionExecutionTarget) *syncedWorktree {
	trimmedID := strings.TrimSpace(target.WorktreeID)
	if trimmedID == "" {
		return nil
	}
	for idx := range items {
		if strings.TrimSpace(items[idx].record.ID) == trimmedID {
			return &items[idx]
		}
	}
	return nil
}

func (s *Service) shouldAttemptBranchCleanup(target syncedWorktree, explicitDeleteBranch bool) bool {
	if strings.TrimSpace(target.git.BranchName) == "" {
		return false
	}
	if explicitDeleteBranch {
		return true
	}
	return target.record.Managed && target.record.CreatedBranch
}

func (s *Service) branchCleanupSkippedMessage(target syncedWorktree, explicitDeleteBranch bool) string {
	branchName := strings.TrimSpace(target.git.BranchName)
	if branchName == "" {
		return ""
	}
	if explicitDeleteBranch || (target.record.Managed && target.record.CreatedBranch) {
		return ""
	}
	return fmt.Sprintf("Kept branch %s: Kent cannot prove this worktree created it", branchName)
}

func pathAvailability(path string) string {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "missing"
		}
		return "inaccessible"
	}
	return "available"
}

func marshalGitMetadata(entry GitWorktree) (string, error) {
	body, err := json.Marshal(struct {
		HeadOID        string `json:"head_oid,omitempty"`
		BranchRef      string `json:"branch_ref,omitempty"`
		BranchName     string `json:"branch_name,omitempty"`
		Detached       bool   `json:"detached,omitempty"`
		Bare           bool   `json:"bare,omitempty"`
		LockedReason   string `json:"locked_reason,omitempty"`
		PrunableReason string `json:"prunable_reason,omitempty"`
	}{
		HeadOID:        entry.HeadOID,
		BranchRef:      entry.BranchRef,
		BranchName:     entry.BranchName,
		Detached:       entry.Detached,
		Bare:           entry.Bare,
		LockedReason:   entry.LockedReason,
		PrunableReason: entry.PrunableReason,
	})
	if err != nil {
		return "", fmt.Errorf("marshal git worktree metadata: %w", err)
	}
	return string(body), nil
}

func clampCwdRelpath(cwdRelpath string, nextBaseRoot string) string {
	trimmedRelpath := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(cwdRelpath))))
	if trimmedRelpath == "" || trimmedRelpath == "." || trimmedRelpath == "/" {
		return "."
	}
	if filepath.IsAbs(filepath.FromSlash(trimmedRelpath)) || trimmedRelpath == ".." || strings.HasPrefix(trimmedRelpath, "../") {
		return "."
	}
	candidate := filepath.Join(strings.TrimSpace(nextBaseRoot), filepath.FromSlash(trimmedRelpath))
	info, err := os.Stat(candidate)
	if err != nil || !info.IsDir() {
		return "."
	}
	return trimmedRelpath
}

func sameOrDescendantPath(root string, candidate string) bool {
	trimmedRoot := strings.TrimSpace(root)
	trimmedCandidate := strings.TrimSpace(candidate)
	if trimmedRoot == "" || trimmedCandidate == "" {
		return false
	}
	if trimmedRoot == trimmedCandidate {
		return true
	}
	rel, err := filepath.Rel(trimmedRoot, trimmedCandidate)
	if err != nil {
		return false
	}
	cleaned := filepath.Clean(rel)
	return cleaned != ".." && !strings.HasPrefix(cleaned, ".."+string(filepath.Separator))
}

func resolveSetupScriptPath(workspaceRoot string, configuredPath string) (string, error) {
	expanded, err := expandTildePath(configuredPath)
	if err != nil {
		return "", err
	}
	path := expanded
	if !filepath.IsAbs(path) {
		path = filepath.Join(strings.TrimSpace(workspaceRoot), path)
	}
	canonical, err := config.CanonicalWorkspaceRoot(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("setup script %q is a directory", canonical)
	}
	return canonical, nil
}

func expandTildePath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || !strings.HasPrefix(trimmed, "~") {
		return trimmed, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	if trimmed == "~" {
		return home, nil
	}
	if strings.HasPrefix(trimmed, "~/") {
		return filepath.Join(home, strings.TrimPrefix(trimmed, "~/")), nil
	}
	if strings.HasPrefix(trimmed, "~\\") {
		return filepath.Join(home, strings.TrimPrefix(trimmed, "~\\")), nil
	}
	return trimmed, nil
}
