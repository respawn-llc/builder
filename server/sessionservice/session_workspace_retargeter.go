package sessionservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"core/server/metadata"
	"core/server/runtimewire"
	"core/server/session"
	sessionruntime "core/server/sessionruntime"
	"core/server/tools"
	"core/shared/config"
	"core/shared/serverapi"
)

type sessionRetargetMetadata interface {
	PlanSessionWorkspaceRetarget(context.Context, metadata.SessionWorkspaceRetargetRequest) (metadata.SessionWorkspaceRetargetPlan, error)
	ResolveSessionWorkspaceRetargetSource(context.Context, string) (metadata.SessionWorkspaceRetargetSource, error)
	CommitSessionWorkspaceRetarget(context.Context, metadata.SessionWorkspaceRetargetPlan, time.Time) (metadata.SessionWorkspaceRetargetResult, error)
	ResolveProjectWorkspaceBoundary(context.Context, string) (metadata.ProjectWorkspaceBoundary, error)
	ProjectWorkspaceAttached(context.Context, string, string) (bool, error)
}

type sessionIdentityPublisher interface {
	PublishSessionIdentity(sessionID string) error
}

type sessionRetargetOutcomePublisher interface {
	PublishSessionRetargetOutcome(sessionID string, outcome serverapi.SessionRetargetOutcome)
}

type SessionWorkspaceRetargetInvocation struct {
	OperationID    serverapi.WorktreeOperationID
	Request        metadata.SessionWorkspaceRetargetRequest
	Origin         *serverapi.RuntimeStepOrigin
	CompletionMode serverapi.SessionRetargetCompletionMode
}

type SessionWorkspaceRetargeter struct {
	metadata  sessionRetargetMetadata
	authority *sessionruntime.Authority
	publisher sessionIdentityPublisher
	outcomes  sessionRetargetOutcomePublisher

	lifetimeCtx context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	wg          sync.WaitGroup
	closed      bool
}

func NewSessionWorkspaceRetargeter(
	metadataStore sessionRetargetMetadata,
	authority *sessionruntime.Authority,
	publisher sessionIdentityPublisher,
) *SessionWorkspaceRetargeter {
	lifetimeCtx, cancel := context.WithCancel(context.Background())
	return &SessionWorkspaceRetargeter{
		metadata:    metadataStore,
		authority:   authority,
		publisher:   publisher,
		lifetimeCtx: lifetimeCtx,
		cancel:      cancel,
	}
}

func (s *SessionWorkspaceRetargeter) WithOutcomePublisher(publisher sessionRetargetOutcomePublisher) *SessionWorkspaceRetargeter {
	if s != nil {
		s.outcomes = publisher
	}
	return s
}

func (s *SessionWorkspaceRetargeter) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
	return nil
}

func (s *SessionWorkspaceRetargeter) RetargetWorkspace(
	ctx context.Context,
	invocation SessionWorkspaceRetargetInvocation,
) (serverapi.SessionRetargetWorkspaceResponse, error) {
	if s == nil || s.metadata == nil || s.authority == nil || s.publisher == nil {
		return serverapi.SessionRetargetWorkspaceResponse{}, errors.New("session workspace retarget dependencies are required")
	}
	plan, err := s.metadata.PlanSessionWorkspaceRetarget(ctx, invocation.Request)
	if err != nil {
		return serverapi.SessionRetargetWorkspaceResponse{}, err
	}
	response := serverapi.SessionRetargetWorkspaceResponse{
		Acknowledgement: serverapi.WorktreeScheduledAcknowledgement{OperationID: invocation.OperationID},
	}
	if plan.NoOp() {
		if plan.SourceBinding == nil {
			return serverapi.SessionRetargetWorkspaceResponse{}, errors.New("no-op Session retarget requires a source binding")
		}
		outcome := successfulSessionRetargetOutcome(invocation.OperationID, *plan.SourceBinding, false)
		if !s.publishTerminalOutcome(plan.SessionID, outcome, nil) {
			return serverapi.SessionRetargetWorkspaceResponse{}, context.Canceled
		}
		if invocation.CompletionMode == serverapi.SessionRetargetCompletionWait {
			response.Outcome = &outcome
		}
		return response, nil
	}
	if !s.register() {
		return serverapi.SessionRetargetWorkspaceResponse{}, context.Canceled
	}
	run := func(runCtx context.Context) (*serverapi.SessionRetargetOutcome, error) {
		defer s.wg.Done()
		result, source, runErr := s.executeWithSource(
			runCtx,
			invocation.Request,
			invocation.Origin,
			metadata.SessionWorkspaceRetargetSource{
				Project:                   plan.SourceProject,
				EffectiveWorkingDirectory: plan.SourceEffectiveWorkingDirectory,
			},
		)
		if runErr != nil {
			if s.shutdownCanceled(runErr) {
				return nil, runErr
			}
			outcome := failedSessionRetargetOutcome(invocation.OperationID, source, runErr)
			return &outcome, runErr
		}
		if invocation.Origin != nil && result.RebindReminder != nil {
			if err := s.authority.SteerSessionRebindReminder(runCtx, plan.SessionID, *result.RebindReminder); err != nil {
				slog.WarnContext(
					runCtx,
					"steer committed Session rebind reminder failed",
					"session_id", plan.SessionID,
					"operation_id", invocation.OperationID.String(),
					"error", err,
				)
			}
		}
		outcome := successfulSessionRetargetOutcome(invocation.OperationID, result.Binding, result.WorkspaceBindingCreated)
		return &outcome, nil
	}
	if invocation.CompletionMode == serverapi.SessionRetargetCompletionWait || invocation.Origin != nil {
		runCtx, cancel := s.requestContext(ctx)
		defer cancel()
		outcome, runErr := run(runCtx)
		if outcome == nil {
			if invocation.CompletionMode == serverapi.SessionRetargetCompletionWait {
				return serverapi.SessionRetargetWorkspaceResponse{}, runErr
			}
			return response, nil
		}
		if !s.publishTerminalOutcome(plan.SessionID, *outcome, runErr) {
			if invocation.CompletionMode == serverapi.SessionRetargetCompletionWait {
				return serverapi.SessionRetargetWorkspaceResponse{}, context.Canceled
			}
			return response, nil
		}
		if invocation.CompletionMode == serverapi.SessionRetargetCompletionWait {
			response.Outcome = outcome
			return response, nil
		}
		return response, nil
	}
	go func() {
		outcome, runErr := run(s.lifetimeCtx)
		if outcome == nil {
			return
		}
		s.publishTerminalOutcome(plan.SessionID, *outcome, runErr)
	}()
	return response, nil
}

func (s *SessionWorkspaceRetargeter) register() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.wg.Add(1)
	return true
}

func (s *SessionWorkspaceRetargeter) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.lifetimeCtx, cancel)
	return runCtx, func() {
		stop()
		cancel()
	}
}

func (s *SessionWorkspaceRetargeter) shutdownCanceled(err error) bool {
	return err != nil &&
		context.Cause(s.lifetimeCtx) != nil &&
		errors.Is(err, context.Canceled)
}

func (s *SessionWorkspaceRetargeter) execute(
	ctx context.Context,
	req metadata.SessionWorkspaceRetargetRequest,
	origin *serverapi.RuntimeStepOrigin,
) (metadata.SessionWorkspaceRetargetResult, error) {
	result, _, err := s.executeWithSource(
		ctx,
		req,
		origin,
		metadata.SessionWorkspaceRetargetSource{},
	)
	return result, err
}

func (s *SessionWorkspaceRetargeter) executeWithSource(
	ctx context.Context,
	req metadata.SessionWorkspaceRetargetRequest,
	origin *serverapi.RuntimeStepOrigin,
	fallbackSource metadata.SessionWorkspaceRetargetSource,
) (metadata.SessionWorkspaceRetargetResult, metadata.SessionWorkspaceRetargetSource, error) {
	var result metadata.SessionWorkspaceRetargetResult
	source := fallbackSource
	err := s.authority.RunSessionExecutionTargetTransition(ctx, req.SessionID, origin, func(
		runCtx context.Context,
		store *session.Store,
		activeRuntime *sessionruntime.ActiveRuntimeMaintenance,
		authority func(func() error) error,
	) error {
		currentPlan, err := s.metadata.PlanSessionWorkspaceRetarget(runCtx, req)
		if err != nil {
			return err
		}
		source = metadata.SessionWorkspaceRetargetSource{
			Project:                   currentPlan.SourceProject,
			EffectiveWorkingDirectory: currentPlan.SourceEffectiveWorkingDirectory,
		}
		if currentPlan.NoOp() {
			if currentPlan.SourceBinding == nil {
				return errors.New("no-op Session retarget requires a source binding")
			}
			result = metadata.SessionWorkspaceRetargetResult{Binding: *currentPlan.SourceBinding}
			return nil
		}
		if store == nil {
			return errors.New("session store is required")
		}
		storeDir, err := config.CanonicalWorkspaceRoot(store.Dir())
		if err != nil {
			return fmt.Errorf("canonicalize session store path: %w", err)
		}
		sourceDir, err := config.CanonicalWorkspaceRoot(currentPlan.SourceSessionDir)
		if err != nil {
			return fmt.Errorf("canonicalize source session artifact path: %w", err)
		}
		if storeDir != sourceDir {
			return fmt.Errorf("session store path %q does not match source artifact %q", store.Dir(), currentPlan.SourceSessionDir)
		}
		if err := validateSessionArtifactSource(currentPlan.SourceSessionDir); err != nil {
			return err
		}
		targetFilesystemContext, err := s.targetFilesystemContext(runCtx, currentPlan, activeRuntime)
		if err != nil {
			return err
		}
		if currentPlan.CrossProject() {
			if err := prepareSessionArtifactTarget(currentPlan.TargetSessionDir); err != nil {
				return err
			}
		}
		updatedAt := time.Now().UTC()
		return authority(func() error {
			return store.RunArtifactRelocation(session.ArtifactRelocationTarget{
				SessionDir:         currentPlan.TargetSessionDir,
				WorkspaceRoot:      currentPlan.TargetWorkspaceRoot,
				WorkspaceContainer: filepath.Base(currentPlan.TargetWorkspaceRoot),
				UpdatedAt:          updatedAt,
			}, func() (session.ArtifactRelocationObservation, error) {
				if activeRuntime != nil {
					if err := activeRuntime.Replace(targetFilesystemContext); err != nil {
						return session.ArtifactRelocationObservation{}, err
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
						return session.ArtifactRelocationObservation{}, errors.Join(fmt.Errorf("move session artifact: %w", err), rollbackRuntime())
					}
					moved = true
				}
				result, err = s.metadata.CommitSessionWorkspaceRetarget(runCtx, currentPlan, updatedAt)
				if err != nil {
					var rollbackErr error
					if moved {
						if moveErr := os.Rename(currentPlan.TargetSessionDir, currentPlan.SourceSessionDir); moveErr != nil {
							rollbackErr = fmt.Errorf("restore session artifact: %w", moveErr)
						}
					}
					return session.ArtifactRelocationObservation{}, errors.Join(err, rollbackErr, rollbackRuntime())
				}
				runtimeRebound = false
				return session.ArtifactRelocationObservation{
					UpdatedRebindReminder: result.RebindReminder,
				}, nil
			})
		})
	})
	if err != nil {
		if s.shutdownCanceled(err) {
			return metadata.SessionWorkspaceRetargetResult{}, source, err
		}
		resolvedSource, sourceErr := s.metadata.ResolveSessionWorkspaceRetargetSource(s.lifetimeCtx, req.SessionID)
		if sourceErr != nil {
			return metadata.SessionWorkspaceRetargetResult{}, source, errors.Join(
				err,
				fmt.Errorf("resolve post-apply Session location: %w", sourceErr),
			)
		}
		return metadata.SessionWorkspaceRetargetResult{}, resolvedSource, err
	}
	if err := s.publisher.PublishSessionIdentity(req.SessionID); err != nil {
		slog.WarnContext(
			ctx,
			"publish committed Session rebind identity failed",
			"session_id", req.SessionID,
			"error", err,
		)
	}
	return result, source, nil
}

func (s *SessionWorkspaceRetargeter) steerFailure(
	ctx context.Context,
	sessionID string,
	outcome serverapi.SessionRetargetOutcome,
) {
	if err := s.authority.SteerSessionRetargetFailure(ctx, sessionID, outcome); err != nil {
		slog.WarnContext(
			ctx,
			"steer Session rebind failure failed",
			"session_id", sessionID,
			"operation_id", outcome.OperationID.String(),
			"error", err,
		)
	}
}

func (s *SessionWorkspaceRetargeter) publishTerminalOutcome(
	sessionID string,
	outcome serverapi.SessionRetargetOutcome,
	runErr error,
) bool {
	if context.Cause(s.lifetimeCtx) != nil {
		return false
	}
	if s.outcomes != nil {
		s.outcomes.PublishSessionRetargetOutcome(sessionID, outcome)
	}
	if runErr != nil && context.Cause(s.lifetimeCtx) == nil {
		s.steerFailure(s.lifetimeCtx, sessionID, outcome)
	}
	return true
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
	var (
		target tools.FilesystemContext
		err    error
	)
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

func successfulSessionRetargetOutcome(
	operationID serverapi.WorktreeOperationID,
	binding metadata.Binding,
	created bool,
) serverapi.SessionRetargetOutcome {
	return serverapi.SessionRetargetOutcome{
		OperationID: operationID,
		Kind:        serverapi.SessionRetargetOutcomeSucceeded,
		Success: &serverapi.SessionRetargetSuccess{
			Binding: serverapi.ProjectBinding{
				ProjectID:       binding.ProjectID,
				ProjectKey:      binding.ProjectKey,
				ProjectName:     binding.ProjectName,
				WorkspaceID:     binding.WorkspaceID,
				CanonicalRoot:   binding.CanonicalRoot,
				WorkspaceName:   binding.WorkspaceName,
				WorkspaceStatus: binding.WorkspaceStatus,
			},
			WorkspaceBindingCreated: created,
		},
	}
}

func failedSessionRetargetOutcome(
	operationID serverapi.WorktreeOperationID,
	source metadata.SessionWorkspaceRetargetSource,
	err error,
) serverapi.SessionRetargetOutcome {
	return serverapi.SessionRetargetOutcome{
		OperationID: operationID,
		Kind:        serverapi.SessionRetargetOutcomeFailed,
		Failure: &serverapi.SessionRetargetFailure{
			Diagnostic:                err.Error(),
			UnchangedProject:          source.Project,
			UnchangedWorkingDirectory: source.EffectiveWorkingDirectory,
		},
	}
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
