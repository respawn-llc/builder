package sessionservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"core/server/metadata"
	"core/server/runtimewire"
	"core/server/session"
	sessionruntime "core/server/sessionruntime"
	"core/server/tools"
	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type sessionRetargetMetadata interface {
	PlanSessionWorkspaceRetarget(context.Context, metadata.SessionWorkspaceRetargetRequest) (metadata.SessionWorkspaceRetargetPlan, error)
	CommitSessionWorkspaceRetarget(context.Context, metadata.SessionWorkspaceRetargetPlan, time.Time) (metadata.SessionWorkspaceRetargetResult, error)
	ResolveProjectWorkspaceBoundary(context.Context, string) (metadata.ProjectWorkspaceBoundary, error)
	ProjectWorkspaceAttached(context.Context, string, string) (bool, error)
	ResolveSessionExecutionTarget(context.Context, string) (clientui.SessionExecutionTarget, error)
}

type sessionIdentityPublisher interface {
	PublishSessionIdentity(sessionID string) error
}

type sessionProcessSource interface {
	List() []shelltool.Snapshot
}

type SessionWorkspaceRetargeter struct {
	metadata  sessionRetargetMetadata
	authority *sessionruntime.Authority
	publisher sessionIdentityPublisher
	processes sessionProcessSource
}

func NewSessionWorkspaceRetargeter(
	metadataStore sessionRetargetMetadata,
	authority *sessionruntime.Authority,
	publisher sessionIdentityPublisher,
	processes sessionProcessSource,
) *SessionWorkspaceRetargeter {
	return &SessionWorkspaceRetargeter{
		metadata:  metadataStore,
		authority: authority,
		publisher: publisher,
		processes: processes,
	}
}

func (s *SessionWorkspaceRetargeter) RetargetWorkspace(ctx context.Context, req metadata.SessionWorkspaceRetargetRequest) (metadata.SessionWorkspaceRetargetResult, error) {
	if s == nil || s.metadata == nil || s.authority == nil || s.publisher == nil || s.processes == nil {
		return metadata.SessionWorkspaceRetargetResult{}, errors.New("session workspace retarget dependencies are required")
	}
	plan, err := s.metadata.PlanSessionWorkspaceRetarget(ctx, req)
	if err != nil {
		return metadata.SessionWorkspaceRetargetResult{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(plan.SessionID))
	if err != nil {
		return metadata.SessionWorkspaceRetargetResult{}, err
	}
	releaseStarts, err := s.authority.BlockSessionStarts(ctx, []runtimeids.SessionID{sessionID}, sessionruntime.SessionStartBlockMaintenance)
	if err != nil {
		return metadata.SessionWorkspaceRetargetResult{}, err
	}
	maintenanceCtx := releaseStarts.AuthorizeMaintenance(ctx)

	var result metadata.SessionWorkspaceRetargetResult
	err = s.authority.RunSessionMaintenance(maintenanceCtx, plan.SessionID, func(runCtx context.Context, store *session.Store, activeRuntime *sessionruntime.ActiveRuntimeMaintenance) error {
		var applyErr error
		result, applyErr = s.applyWorkspaceRetarget(runCtx, req, store, activeRuntime, false)
		return applyErr
	})
	closeErr := releaseStarts.Close(context.Background())
	var publicationErr error
	if err == nil {
		publicationErr = s.publisher.PublishSessionIdentity(plan.SessionID)
	}
	return result, errors.Join(err, publicationErr, closeErr)
}

func (s *SessionWorkspaceRetargeter) ScheduleWorkspaceRetarget(
	ctx context.Context,
	req metadata.SessionWorkspaceRetargetRequest,
	origin serverapi.RuntimeStepOrigin,
	operationID serverapi.WorktreeOperationID,
) (serverapi.WorktreeScheduledAcknowledgement, error) {
	if s == nil || s.metadata == nil || s.authority == nil || s.publisher == nil || s.processes == nil {
		return serverapi.WorktreeScheduledAcknowledgement{}, errors.New("session workspace retarget dependencies are required")
	}
	plan, err := s.metadata.PlanSessionWorkspaceRetarget(ctx, req)
	if err != nil {
		return serverapi.WorktreeScheduledAcknowledgement{}, err
	}
	sourceTarget, err := s.metadata.ResolveSessionExecutionTarget(ctx, plan.SessionID)
	if err != nil {
		return serverapi.WorktreeScheduledAcknowledgement{}, err
	}
	result := make(chan error, 1)
	accepted := make(chan bool, 1)
	runCtx, cancelRun := context.WithCancel(context.WithoutCancel(ctx))
	go func() {
		defer cancelRun()
		offered := false
		scheduled := false
		runErr := s.authority.RunSessionMaintenanceAtStepBoundary(
			runCtx,
			plan.SessionID,
			origin,
			func() {
				offered = true
				result <- nil
				scheduled = <-accepted
			},
			func(boundaryCtx context.Context, store *session.Store, activeRuntime *sessionruntime.ActiveRuntimeMaintenance) error {
				_, applyErr := s.applyWorkspaceRetarget(boundaryCtx, req, store, activeRuntime, true)
				return applyErr
			},
		)
		if offered && !scheduled {
			return
		}
		if scheduled {
			if runErr != nil {
				persistCtx := context.WithoutCancel(runCtx)
				if persistErr := s.persistRebindFailure(persistCtx, plan, sourceTarget.EffectiveWorkdir, runErr); persistErr != nil {
					slog.ErrorContext(
						persistCtx,
						"persist scheduled Session rebind failure notice",
						"session_id", plan.SessionID,
						"rebind_error", runErr,
						"persistence_error", persistErr,
					)
				}
				return
			}
			if publicationErr := s.publisher.PublishSessionIdentity(plan.SessionID); publicationErr != nil {
				slog.ErrorContext(
					context.WithoutCancel(runCtx),
					"publish scheduled Session rebind identity",
					"session_id", plan.SessionID,
					"error", publicationErr,
				)
			}
			return
		}
		result <- runErr
	}()
	select {
	case err := <-result:
		if err != nil {
			cancelRun()
			return serverapi.WorktreeScheduledAcknowledgement{}, err
		}
		accepted <- true
		return serverapi.WorktreeScheduledAcknowledgement{OperationID: operationID}, nil
	case <-ctx.Done():
		cancelRun()
		accepted <- false
		return serverapi.WorktreeScheduledAcknowledgement{}, context.Cause(ctx)
	}
}

func (s *SessionWorkspaceRetargeter) applyWorkspaceRetarget(
	ctx context.Context,
	req metadata.SessionWorkspaceRetargetRequest,
	store *session.Store,
	activeRuntime *sessionruntime.ActiveRuntimeMaintenance,
	ignoreBackgroundProcesses bool,
) (metadata.SessionWorkspaceRetargetResult, error) {
	currentPlan, err := s.metadata.PlanSessionWorkspaceRetarget(ctx, req)
	if err != nil {
		return metadata.SessionWorkspaceRetargetResult{}, err
	}
	sourceTarget, err := s.metadata.ResolveSessionExecutionTarget(ctx, currentPlan.SessionID)
	if err != nil {
		return metadata.SessionWorkspaceRetargetResult{}, err
	}
	if !ignoreBackgroundProcesses {
		ownedProcessActive, processErr := s.ownedBackgroundProcessActive(currentPlan.SessionID)
		if processErr != nil {
			return metadata.SessionWorkspaceRetargetResult{}, processErr
		}
		if ownedProcessActive {
			return metadata.SessionWorkspaceRetargetResult{}, &serverapi.SessionRetargetError{
				Reason:        serverapi.SessionRetargetBackgroundProcess,
				SessionID:     currentPlan.SessionID,
				SourceProject: currentPlan.SourceProject,
				TargetRoot:    currentPlan.TargetWorkspaceRoot,
			}
		}
	}
	if store == nil {
		return metadata.SessionWorkspaceRetargetResult{}, errors.New("session store is required")
	}
	storeDir, err := config.CanonicalWorkspaceRoot(store.Dir())
	if err != nil {
		return metadata.SessionWorkspaceRetargetResult{}, fmt.Errorf("canonicalize session store path: %w", err)
	}
	sourceDir, err := config.CanonicalWorkspaceRoot(currentPlan.SourceSessionDir)
	if err != nil {
		return metadata.SessionWorkspaceRetargetResult{}, fmt.Errorf("canonicalize source session artifact path: %w", err)
	}
	if storeDir != sourceDir {
		return metadata.SessionWorkspaceRetargetResult{}, fmt.Errorf("session store path %q does not match source artifact %q", store.Dir(), currentPlan.SourceSessionDir)
	}
	if err := validateSessionArtifactSource(currentPlan.SourceSessionDir); err != nil {
		return metadata.SessionWorkspaceRetargetResult{}, err
	}
	targetFilesystemContext, err := s.targetFilesystemContext(ctx, currentPlan, activeRuntime)
	if err != nil {
		return metadata.SessionWorkspaceRetargetResult{}, err
	}
	if currentPlan.CrossProject() {
		if err := prepareSessionArtifactTarget(currentPlan.TargetSessionDir); err != nil {
			return metadata.SessionWorkspaceRetargetResult{}, err
		}
	}
	workingDirectoryChanged := sourceTarget.EffectiveWorkdir != currentPlan.TargetWorkspaceRoot
	if currentPlan.CrossProject() || workingDirectoryChanged {
		workingDirectory := currentPlan.TargetWorkspaceRoot
		currentPlan.RebindReminder = &session.SessionRebindReminder{
			Kind:          session.SessionRebindReminderSucceeded,
			SourceProject: currentPlan.SourceProject,
			TargetProject: currentPlan.TargetProject,
		}
		if workingDirectoryChanged {
			currentPlan.RebindReminder.WorkingDirectory = &workingDirectory
		}
	}
	updatedAt := time.Now().UTC()
	var result metadata.SessionWorkspaceRetargetResult
	err = store.RunArtifactRelocation(session.ArtifactRelocationTarget{
		SessionDir:         currentPlan.TargetSessionDir,
		WorkspaceRoot:      currentPlan.TargetWorkspaceRoot,
		WorkspaceContainer: filepath.Base(currentPlan.TargetWorkspaceRoot),
		UpdatedAt:          updatedAt,
		RebindReminder:     currentPlan.RebindReminder,
	}, func() error {
		if activeRuntime != nil {
			if err := activeRuntime.Replace(targetFilesystemContext); err != nil {
				return err
			}
		}
		runtimeRebound := activeRuntime != nil
		rollbackRuntime := func() error {
			if !runtimeRebound {
				return nil
			}
			runtimeRebound = false
			return activeRuntime.Replace(activeRuntime.PreviousFilesystemContext)
		}
		moved := false
		if currentPlan.CrossProject() {
			if err := os.Rename(currentPlan.SourceSessionDir, currentPlan.TargetSessionDir); err != nil {
				return errors.Join(fmt.Errorf("move session artifact: %w", err), rollbackRuntime())
			}
			moved = true
		}
		result, err = s.metadata.CommitSessionWorkspaceRetarget(ctx, currentPlan, updatedAt)
		if err != nil {
			var rollbackErr error
			if moved {
				if moveErr := os.Rename(currentPlan.TargetSessionDir, currentPlan.SourceSessionDir); moveErr != nil {
					rollbackErr = fmt.Errorf("restore session artifact: %w", moveErr)
				}
			}
			return errors.Join(err, rollbackErr, rollbackRuntime())
		}
		runtimeRebound = false
		return nil
	})
	return result, err
}

func (s *SessionWorkspaceRetargeter) targetFilesystemContext(
	ctx context.Context,
	plan metadata.SessionWorkspaceRetargetPlan,
	activeRuntime *sessionruntime.ActiveRuntimeMaintenance,
) (tools.FilesystemContext, error) {
	if activeRuntime == nil {
		return tools.FilesystemContext{}, nil
	}
	previous := activeRuntime.PreviousFilesystemContext
	var target tools.FilesystemContext
	var err error
	if plan.CrossProject() {
		targetBoundary, boundaryErr := s.metadata.ResolveProjectWorkspaceBoundary(ctx, plan.TargetProject.ID)
		if boundaryErr != nil {
			return tools.FilesystemContext{}, boundaryErr
		}
		attached, attachedErr := s.metadata.ProjectWorkspaceAttached(ctx, plan.TargetProject.ID, plan.TargetWorkspaceRoot)
		if attachedErr != nil {
			return tools.FilesystemContext{}, attachedErr
		}
		if !attached {
			targetBoundary, _, err = targetBoundary.WithWorkspace(metadata.ProjectWorkspace{CanonicalRoot: plan.TargetWorkspaceRoot})
			if err != nil {
				return tools.FilesystemContext{}, err
			}
		}
		target, err = runtimewire.NewFilesystemContext(plan.TargetWorkspaceRoot, plan.TargetWorkspaceRoot, targetBoundary)
	} else {
		target, err = runtimewire.WithExecutionTarget(previous, plan.TargetWorkspaceRoot, plan.TargetWorkspaceRoot, nil)
	}
	if err != nil {
		return tools.FilesystemContext{}, err
	}
	if previous.ManagedWorktree != nil {
		target.ManagedWorktree, err = previous.ManagedWorktree.WithCurrentWorktreeRoot(nil)
	}
	return target, err
}

func (s *SessionWorkspaceRetargeter) persistRebindFailure(
	ctx context.Context,
	plan metadata.SessionWorkspaceRetargetPlan,
	workingDirectory string,
	cause error,
) error {
	diagnostic := cause.Error()
	reminder := session.SessionRebindReminder{
		Kind:              session.SessionRebindReminderFailed,
		SourceProject:     plan.SourceProject,
		TargetProject:     plan.TargetProject,
		WorkingDirectory:  &workingDirectory,
		FailureDiagnostic: &diagnostic,
	}
	sessionID, err := runtimeids.ParseSessionID(plan.SessionID)
	if err != nil {
		return err
	}
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		return err
	}
	return s.authority.WithSessionStore(ctx, descriptor, func(_ context.Context, store *session.Store) error {
		return store.SetSessionRebindReminder(&reminder)
	})
}

func (s *SessionWorkspaceRetargeter) ownedBackgroundProcessActive(sessionID string) (bool, error) {
	id := strings.TrimSpace(sessionID)
	for _, process := range s.processes.List() {
		if !process.Running {
			continue
		}
		ownerSessionID := strings.TrimSpace(process.OwnerSessionID)
		if ownerSessionID == "" {
			return false, fmt.Errorf("running background process %q has no owner session id", process.ID)
		}
		if ownerSessionID == id {
			return true, nil
		}
	}
	return false, nil
}

func validateSessionArtifactSource(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat session artifact source %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("session artifact source %q is not a real directory", path)
	}
	return nil
}

func prepareSessionArtifactTarget(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("session artifact target %q already exists", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat session artifact target %q: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create session artifact target parent: %w", err)
	}
	return nil
}
