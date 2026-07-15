package sessionview

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/launch"
	"core/server/llm"
	"core/server/runtime"
	"core/server/runtimeops"
	"core/server/session"
	"core/server/worktree"
	servicecontract "core/shared/apicontract"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
)

type SessionStoreResolver interface {
	ResolveSessionStore(ctx context.Context, sessionID string) (*session.Store, error)
}

type RuntimeResolver interface {
	ResolveRuntime(ctx context.Context, sessionID string) (*runtime.Engine, error)
}

type ExecutionTargetResolver interface {
	ResolveSessionExecutionTarget(ctx context.Context, sessionID string) (clientui.SessionExecutionTarget, error)
}

type UpdateStatusProvider interface {
	Status(ctx context.Context) clientui.UpdateStatus
}

type Service struct {
	sessions         SessionStoreResolver
	snapshots        SessionSnapshotSource
	targets          ExecutionTargetResolver
	updates          UpdateStatusProvider
	operations       *runtimeops.Coordinator
	app              config.App
	auth             client.AuthStatusClient
	git              *worktree.GitInspector
	cacheWarningMu   sync.RWMutex
	cacheWarningMode config.CacheWarningMode
}

func (s *Service) WithExecutionEnvironmentConfig(app config.App) *Service {
	if s != nil {
		s.app = app
	}
	return s
}

func (s *Service) WithExecutionEnvironmentAuth(provider client.AuthStatusClient) *Service {
	if s != nil {
		s.auth = provider
	}
	return s
}

func (s *Service) WithExecutionEnvironmentGit(inspector *worktree.GitInspector) *Service {
	if s != nil {
		s.git = inspector
	}
	return s
}

func NewService(sessions SessionStoreResolver, runtimes RuntimeResolver, targets ExecutionTargetResolver) *Service {
	svc := &Service{
		sessions:         sessions,
		targets:          targets,
		cacheWarningMode: config.CacheWarningModeDefault,
		operations:       runtimeops.NewCoordinator(),
	}
	baseSnapshots := newResolvedSessionSnapshotSource(sessions, runtimes, svc.cacheWarningModeValue)
	svc.snapshots = newEnrichedSessionSnapshotSource(baseSnapshots, targets, func() UpdateStatusProvider {
		if svc == nil {
			return nil
		}
		return svc.updates
	})
	return svc
}

func (s *Service) WithOperationCoordinator(coordinator *runtimeops.Coordinator) *Service {
	if s == nil {
		return nil
	}
	if coordinator == nil {
		coordinator = runtimeops.NewCoordinator()
	}
	s.operations = coordinator
	return s
}

func (s *Service) WithCacheWarningMode(mode config.CacheWarningMode) *Service {
	if s == nil {
		return nil
	}
	normalized := normalizeServiceCacheWarningMode(mode)
	changed := s.cacheWarningModeValue() != normalized
	s.setCacheWarningMode(normalized)
	if changed {
		if clearer, ok := s.snapshots.(interface{ ClearCaches() }); ok {
			clearer.ClearCaches()
		}
	}
	return s
}

func (s *Service) WithUpdateStatusProvider(provider UpdateStatusProvider) *Service {
	if s == nil {
		return nil
	}
	s.updates = provider
	return s
}

func (s *Service) cacheWarningModeValue() config.CacheWarningMode {
	if s == nil {
		return config.CacheWarningModeDefault
	}
	s.cacheWarningMu.RLock()
	defer s.cacheWarningMu.RUnlock()
	return s.cacheWarningMode
}

func (s *Service) setCacheWarningMode(mode config.CacheWarningMode) {
	if s == nil {
		return
	}
	s.cacheWarningMu.Lock()
	defer s.cacheWarningMu.Unlock()
	s.cacheWarningMode = mode
}

func normalizeServiceCacheWarningMode(mode config.CacheWarningMode) config.CacheWarningMode {
	switch strings.ToLower(strings.TrimSpace(string(mode))) {
	case string(config.CacheWarningModeOff):
		return config.CacheWarningModeOff
	case string(config.CacheWarningModeVerbose):
		return config.CacheWarningModeVerbose
	default:
		return config.CacheWarningModeDefault
	}
}

type staticSessionResolver struct {
	store *session.Store
}

func NewStaticSessionResolver(store *session.Store) SessionStoreResolver {
	if store == nil {
		return nil
	}
	return staticSessionResolver{store: store}
}

func (r staticSessionResolver) ResolveSessionStore(_ context.Context, sessionID string) (*session.Store, error) {
	if r.store == nil {
		return nil, errors.New("session store is required")
	}
	if strings.TrimSpace(sessionID) != strings.TrimSpace(r.store.Meta().SessionID) {
		return nil, fmt.Errorf("session %q not available", strings.TrimSpace(sessionID))
	}
	return r.store, nil
}

type staticRuntimeResolver struct {
	engine *runtime.Engine
}

func NewStaticRuntimeResolver(engine *runtime.Engine) RuntimeResolver {
	if engine == nil {
		return nil
	}
	return staticRuntimeResolver{engine: engine}
}

func (r staticRuntimeResolver) ResolveRuntime(_ context.Context, sessionID string) (*runtime.Engine, error) {
	if r.engine == nil {
		return nil, nil
	}
	if strings.TrimSpace(sessionID) != strings.TrimSpace(r.engine.SessionID()) {
		return nil, fmt.Errorf("session %q not available", strings.TrimSpace(sessionID))
	}
	return r.engine, nil
}

func (r staticRuntimeResolver) WithGuardedRuntime(ctx context.Context, sessionID string, fn func(*runtime.Engine) error) (bool, error) {
	engine, err := r.ResolveRuntime(ctx, sessionID)
	if err != nil || engine == nil {
		return false, nil
	}
	return true, fn(engine)
}

func (s *Service) GetSessionMainView(ctx context.Context, req serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.SessionMainViewResponse{}, err
	}
	snapshot, err := s.resolveSnapshot(ctx, req.SessionID, req.PendingOperationRefs)
	if err != nil {
		return serverapi.SessionMainViewResponse{}, err
	}
	view, err := snapshot.MainView(ctx)
	if err != nil {
		return serverapi.SessionMainViewResponse{}, err
	}
	if len(view.InputReconciliation.Operations) == 0 && len(req.PendingOperationRefs) > 0 {
		view.InputReconciliation = s.operations.Snapshot(strings.TrimSpace(req.SessionID), view.Version, req.PendingOperationRefs)
	}
	return serverapi.SessionMainViewResponse{MainView: view}, nil
}

func (s *Service) SessionTranscriptTailEntries(ctx context.Context, sessionID string) ([]runtime.ChatEntry, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, serverapi.ErrSessionIDRequired
	}
	snapshot, err := s.resolveSnapshot(ctx, sessionID, nil)
	if err != nil {
		return nil, err
	}
	return snapshot.TranscriptTailEntries(ctx)
}

func (s *Service) GetSessionTranscriptPage(ctx context.Context, req serverapi.SessionTranscriptPageRequest) (serverapi.SessionTranscriptPageResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.SessionTranscriptPageResponse{}, err
	}
	pageReq := clientui.TranscriptPageRequest{Cursor: req.Cursor, NewerCursor: req.NewerCursor}
	snapshot, err := s.resolveSnapshot(ctx, req.SessionID, nil)
	if err != nil {
		return serverapi.SessionTranscriptPageResponse{}, err
	}
	page, err := snapshot.TranscriptPage(ctx, pageReq)
	if err != nil {
		return serverapi.SessionTranscriptPageResponse{}, err
	}
	return serverapi.SessionTranscriptPageResponse{Transcript: page}, nil
}

func (s *Service) GetLatestCommittedAssistantFinalAnswer(ctx context.Context, req serverapi.SessionLatestCommittedAssistantFinalAnswerRequest) (serverapi.SessionLatestCommittedAssistantFinalAnswerResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{}, err
	}
	if s == nil || s.sessions == nil {
		return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{}, errSessionStoreResolverRequired
	}
	store, err := s.sessions.ResolveSessionStore(ctx, req.SessionID)
	if err != nil {
		return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{}, err
	}
	answer, err := runtime.LatestCommittedAssistantFinalAnswerFromStore(store)
	if err != nil {
		return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{}, err
	}
	if answer != nil && strings.TrimSpace(*answer) == "" {
		return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{}, errors.New("latest committed assistant final answer must not be blank")
	}
	return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{Answer: answer}, nil
}

func (s *Service) GetSessionExecutionEnvironment(ctx context.Context, req serverapi.SessionExecutionEnvironmentRequest) (serverapi.SessionExecutionEnvironmentResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.SessionExecutionEnvironmentResponse{}, err
	}
	if s == nil || s.sessions == nil {
		return serverapi.SessionExecutionEnvironmentResponse{}, errSessionStoreResolverRequired
	}
	store, err := s.sessions.ResolveSessionStore(ctx, req.SessionID.String())
	if err != nil {
		return serverapi.SessionExecutionEnvironmentResponse{}, err
	}
	meta := store.Meta()
	if strings.TrimSpace(meta.SessionID) != req.SessionID.String() {
		return serverapi.SessionExecutionEnvironmentResponse{}, fmt.Errorf("session execution environment identity mismatch: requested %q, resolved %q", req.SessionID.String(), meta.SessionID)
	}
	environment := serverapi.SessionExecutionEnvironment{SessionID: req.SessionID}
	target, targetErr := s.resolveExecutionTarget(ctx, req.SessionID.String())
	if targetErr != nil {
		environment.Workspace = serverapi.FailedSessionExecutionWorkspace(serverapi.SessionExecutionFieldError{Code: serverapi.SessionExecutionFieldErrorSourceFailure, Message: targetErr.Error()})
	} else {
		environment.Workspace = resolveSessionExecutionWorkspace(target)
	}
	environment.Branch = s.resolveBranch(ctx, target, environment.Workspace)
	model, modelErr := launch.ResolveReadOnlySessionModel(s.app, meta)
	if modelErr != nil {
		var unavailable *launch.ReadOnlySessionModelUnavailableError
		if errors.As(modelErr, &unavailable) {
			environment.Model = serverapi.UnavailableSessionExecutionModel(unavailable.Reason)
		} else {
			environment.Model = serverapi.FailedSessionExecutionModel(serverapi.SessionExecutionFieldError{Code: serverapi.SessionExecutionFieldErrorInvalidConfiguration, Message: modelErr.Error()})
		}
	} else {
		environment.Model = serverapi.AvailableSessionExecutionModel(serverapi.SessionExecutionModel{
			Name:     model.Name,
			Provider: model.Provider.ID(),
			Locked:   model.Locked,
		})
	}
	environment.Auth = s.resolveAuth(ctx, environment.Model)
	return serverapi.SessionExecutionEnvironmentResponse{Environment: environment}, nil
}

func (s *Service) resolveExecutionTarget(ctx context.Context, sessionID string) (clientui.SessionExecutionTarget, error) {
	if s.targets == nil {
		return clientui.SessionExecutionTarget{}, nil
	}
	return s.targets.ResolveSessionExecutionTarget(ctx, sessionID)
}

func resolveSessionExecutionWorkspace(target clientui.SessionExecutionTarget) serverapi.SessionExecutionWorkspaceField {
	target = clientui.NormalizeSessionExecutionTarget(target)
	if clientui.SessionExecutionTargetIsZero(target) {
		return serverapi.UnavailableSessionExecutionWorkspace(
			serverapi.SessionExecutionWorkspaceUnavailableNotConfigured,
		)
	}
	availability := target.WorkspaceAvailability
	targetKind := "workspace"
	if target.Worktree != nil {
		availability = target.Worktree.Availability
		targetKind = "worktree"
	}
	switch strings.TrimSpace(availability) {
	case "missing", "inaccessible":
		return serverapi.FailedSessionExecutionWorkspace(serverapi.SessionExecutionFieldError{
			Code: serverapi.SessionExecutionFieldErrorSourceFailure,
			Message: fmt.Sprintf(
				"session execution %s target is %s",
				targetKind,
				strings.TrimSpace(availability),
			),
		})
	}
	if strings.TrimSpace(target.EffectiveWorkdir) == "" {
		return serverapi.FailedSessionExecutionWorkspace(serverapi.SessionExecutionFieldError{
			Code:    serverapi.SessionExecutionFieldErrorSourceFailure,
			Message: "session execution target has no effective workdir",
		})
	}
	return serverapi.AvailableSessionExecutionWorkspace(target.EffectiveWorkdir)
}

func (s *Service) resolveBranch(
	ctx context.Context,
	target clientui.SessionExecutionTarget,
	workspace serverapi.SessionExecutionWorkspaceField,
) serverapi.SessionExecutionBranchField {
	if workspace.Kind() != serverapi.SessionExecutionFieldAvailable {
		return serverapi.UnavailableSessionExecutionBranch(serverapi.SessionExecutionBranchUnavailableNotGitRepository)
	}
	if s.git == nil {
		return serverapi.UnavailableSessionExecutionBranch(serverapi.SessionExecutionBranchUnavailableNotGitRepository)
	}
	entries, err := s.git.List(ctx, target.EffectiveWorkdir)
	if err != nil {
		var listErr *worktree.GitWorktreeListError
		if errors.As(err, &listErr) && listErr.Kind == worktree.GitWorktreeListErrorNotRepository {
			return serverapi.UnavailableSessionExecutionBranch(serverapi.SessionExecutionBranchUnavailableNotGitRepository)
		}
		return serverapi.FailedSessionExecutionBranch(serverapi.SessionExecutionFieldError{Code: serverapi.SessionExecutionFieldErrorSourceFailure, Message: err.Error()})
	}
	for _, entry := range entries {
		if entry.Root == target.EffectiveWorkdir {
			if entry.Detached {
				return serverapi.UnavailableSessionExecutionBranch(serverapi.SessionExecutionBranchUnavailableDetachedHead)
			}
			if strings.TrimSpace(entry.BranchName) == "" {
				return serverapi.UnavailableSessionExecutionBranch(serverapi.SessionExecutionBranchUnavailableNotGitRepository)
			}
			return serverapi.AvailableSessionExecutionBranch(entry.BranchName)
		}
	}
	return serverapi.UnavailableSessionExecutionBranch(serverapi.SessionExecutionBranchUnavailableNotGitRepository)
}

func (s *Service) resolveAuth(ctx context.Context, model serverapi.SessionExecutionModelField) serverapi.SessionExecutionAuthField {
	if model.Kind() != serverapi.SessionExecutionFieldAvailable || s.auth == nil {
		return serverapi.UnavailableSessionExecutionAuth(serverapi.SessionExecutionAuthUnavailableNotApplicable)
	}
	effectiveModel, ok := model.Value()
	if !ok || !sessionExecutionProviderUsesKentManagedAuth(effectiveModel.Provider) {
		return serverapi.UnavailableSessionExecutionAuth(serverapi.SessionExecutionAuthUnavailableNotApplicable)
	}
	var statusReq serverapi.AuthStatusRequest
	status, err := s.auth.GetAuthStatus(ctx, statusReq)
	if err != nil {
		return serverapi.FailedSessionExecutionAuth(serverapi.SessionExecutionFieldError{Code: serverapi.SessionExecutionFieldErrorSourceFailure, Message: err.Error()})
	}
	if !status.Auth.Visible && status.Auth.Method == "" {
		return serverapi.UnavailableSessionExecutionAuth(serverapi.SessionExecutionAuthUnavailableNotApplicable)
	}
	method := serverapi.SessionExecutionAuthMethod(string(status.Auth.Method))
	if method == "" {
		method = serverapi.SessionExecutionAuthMethodNone
	}
	return serverapi.AvailableSessionExecutionAuth(serverapi.SessionExecutionAuth{
		Provider: effectiveModel.Provider,
		Method:   method,
	})
}

func sessionExecutionProviderUsesKentManagedAuth(provider string) bool {
	capabilities, err := llm.InferProviderCapabilities(strings.TrimSpace(provider))
	return err == nil && capabilities.IsOpenAIFirstParty
}

func (s *Service) resolveSnapshot(ctx context.Context, sessionID string, refs []clientui.RuntimeOperationRef) (SessionSnapshot, error) {
	if s == nil || s.snapshots == nil {
		return nil, errSessionStoreResolverRequired
	}
	return s.snapshots.ResolveSessionSnapshot(ctx, sessionID, refs)
}

var _ servicecontract.SessionViewService = (*Service)(nil)
