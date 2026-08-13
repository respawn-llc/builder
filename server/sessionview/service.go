package sessionview

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"core/server/launch"
	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/worktree"
	servicecontract "core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
)

type SessionStoreResolver interface {
	ResolveSessionStore(ctx context.Context, sessionID string) (*session.Store, error)
}

type ExecutionTargetResolver interface {
	ResolveSessionExecutionTarget(ctx context.Context, sessionID string) (clientui.SessionExecutionTarget, error)
}

type Service struct {
	sessions         SessionStoreResolver
	snapshots        *resolvedSessionSnapshotSource
	targets          ExecutionTargetResolver
	app              config.App
	auth             servicecontract.AuthStatusService
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

func (s *Service) WithExecutionEnvironmentAuth(provider servicecontract.AuthStatusService) *Service {
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

func NewService(
	sessions SessionStoreResolver,
	activity runtimeReadModelSnapshotProvider,
	authority *sessionruntime.Authority,
	targets ExecutionTargetResolver,
) *Service {
	svc := &Service{
		sessions:         sessions,
		targets:          targets,
		cacheWarningMode: config.CacheWarningModeDefault,
	}
	svc.snapshots = newResolvedSessionSnapshotSource(sessions, activity, authority, svc.cacheWarningModeValue)
	return svc
}

func (s *Service) WithCacheWarningMode(mode config.CacheWarningMode) *Service {
	if s == nil {
		return nil
	}
	s.setCacheWarningMode(normalizeServiceCacheWarningMode(mode))
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

func (s *Service) GetSessionMainView(ctx context.Context, req serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.SessionMainViewRequest]) (serverapi.SessionMainViewResponse, error) {
		return s.getSessionMainView(ctx, validated.Value(), nil)
	})
}

func (s *Service) GetSessionMainViewValidated(ctx context.Context, req servicecontract.Validated[serverapi.SessionMainViewRequest], authorization servicecontract.AuthorizedSessionInActiveProject) (serverapi.SessionMainViewResponse, error) {
	return s.getSessionMainView(ctx, req.Value(), &authorization.ExecutionTarget)
}

func (s *Service) getSessionMainView(ctx context.Context, req serverapi.SessionMainViewRequest, authorizedTarget *clientui.SessionExecutionTarget) (serverapi.SessionMainViewResponse, error) {
	snapshot, err := s.resolveSnapshot(ctx, req.SessionID)
	if err != nil {
		return serverapi.SessionMainViewResponse{}, err
	}
	view, err := snapshot.MainView(ctx)
	if err != nil {
		return serverapi.SessionMainViewResponse{}, err
	}
	if authorizedTarget != nil {
		view.Session.ExecutionTarget = *authorizedTarget
	} else if s.targets != nil && strings.TrimSpace(view.Session.SessionID) != "" {
		target, err := s.targets.ResolveSessionExecutionTarget(ctx, view.Session.SessionID)
		if err != nil {
			return serverapi.SessionMainViewResponse{}, err
		}
		view.Session.ExecutionTarget = target
	}
	return serverapi.SessionMainViewResponse{MainView: view}, nil
}

func (s *Service) SessionTranscriptTailEntries(ctx context.Context, sessionID string) ([]runtime.ChatEntry, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, serverapi.ErrSessionIDRequired
	}
	snapshot, err := s.resolveSnapshot(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return snapshot.TranscriptTailEntries(ctx)
}

func (s *Service) GetSessionTranscriptPage(ctx context.Context, req serverapi.SessionTranscriptPageRequest) (serverapi.SessionTranscriptPageResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.SessionTranscriptPageRequest]) (serverapi.SessionTranscriptPageResponse, error) {
		return s.getSessionTranscriptPage(ctx, validated.Value())
	})
}

func (s *Service) GetSessionTranscriptPageValidated(ctx context.Context, req servicecontract.Validated[serverapi.SessionTranscriptPageRequest], _ servicecontract.AuthorizedSessionInActiveProject) (serverapi.SessionTranscriptPageResponse, error) {
	return s.getSessionTranscriptPage(ctx, req.Value())
}

func (s *Service) getSessionTranscriptPage(ctx context.Context, req serverapi.SessionTranscriptPageRequest) (serverapi.SessionTranscriptPageResponse, error) {
	pageReq := clientui.TranscriptPageRequest{Cursor: req.Cursor, NewerCursor: req.NewerCursor}
	snapshot, err := s.resolveSnapshot(ctx, req.SessionID)
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
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.SessionLatestCommittedAssistantFinalAnswerRequest]) (serverapi.SessionLatestCommittedAssistantFinalAnswerResponse, error) {
		return s.getLatestCommittedAssistantFinalAnswer(ctx, validated.Value())
	})
}

func (s *Service) GetLatestCommittedAssistantFinalAnswerValidated(ctx context.Context, req servicecontract.Validated[serverapi.SessionLatestCommittedAssistantFinalAnswerRequest], _ servicecontract.AuthorizedSessionInActiveProject) (serverapi.SessionLatestCommittedAssistantFinalAnswerResponse, error) {
	return s.getLatestCommittedAssistantFinalAnswer(ctx, req.Value())
}

func (s *Service) getLatestCommittedAssistantFinalAnswer(ctx context.Context, req serverapi.SessionLatestCommittedAssistantFinalAnswerRequest) (serverapi.SessionLatestCommittedAssistantFinalAnswerResponse, error) {
	if s == nil {
		return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{}, errSessionStoreResolverRequired
	}
	_, eventLog, err := resolveSessionStoreAndEventLog(ctx, s.sessions, req.SessionID)
	if err != nil {
		return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{}, err
	}
	answer, err := runtime.LatestCommittedAssistantFinalAnswerFromEventLog(eventLog)
	if err != nil {
		return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{}, err
	}
	if answer != nil && strings.TrimSpace(*answer) == "" {
		return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{}, errors.New("latest committed assistant final answer must not be blank")
	}
	return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{Answer: answer}, nil
}

func (s *Service) GetSessionExecutionEnvironment(ctx context.Context, req serverapi.SessionExecutionEnvironmentRequest) (serverapi.SessionExecutionEnvironmentResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.SessionExecutionEnvironmentRequest]) (serverapi.SessionExecutionEnvironmentResponse, error) {
		return s.getSessionExecutionEnvironment(ctx, validated.Value(), nil)
	})
}

func (s *Service) GetSessionExecutionEnvironmentValidated(ctx context.Context, req servicecontract.Validated[serverapi.SessionExecutionEnvironmentRequest], authorization servicecontract.AuthorizedSessionInActiveProject) (serverapi.SessionExecutionEnvironmentResponse, error) {
	return s.getSessionExecutionEnvironment(ctx, req.Value(), &authorization.ExecutionTarget)
}

func (s *Service) getSessionExecutionEnvironment(ctx context.Context, req serverapi.SessionExecutionEnvironmentRequest, authorizedTarget *clientui.SessionExecutionTarget) (serverapi.SessionExecutionEnvironmentResponse, error) {
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
	var target clientui.SessionExecutionTarget
	var targetErr error
	if authorizedTarget != nil {
		target = *authorizedTarget
	} else {
		target, targetErr = s.resolveExecutionTarget(ctx, req.SessionID.String())
	}
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
	availability := string(target.WorkspaceAvailability)
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
	canonicalWorkdir, err := config.CanonicalWorkspaceRoot(target.EffectiveWorkdir)
	if err != nil {
		return serverapi.FailedSessionExecutionBranch(serverapi.SessionExecutionFieldError{Code: serverapi.SessionExecutionFieldErrorSourceFailure, Message: err.Error()})
	}
	for _, entry := range entries {
		relative, err := filepath.Rel(entry.Root, canonicalWorkdir)
		if err != nil || !filepath.IsLocal(relative) {
			continue
		}
		if relative == "." || filepath.IsLocal(relative) {
			if entry.Detached {
				return serverapi.UnavailableSessionExecutionBranch(serverapi.SessionExecutionBranchUnavailableDetachedHead)
			}
			if entry.Branch == nil {
				return serverapi.UnavailableSessionExecutionBranch(serverapi.SessionExecutionBranchUnavailableNotGitRepository)
			}
			return serverapi.AvailableSessionExecutionBranch(entry.Branch.Name())
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
	status, err := s.auth.GetAuthStatus(ctx, serverapi.AuthStatusRequest{})
	if err != nil {
		return serverapi.FailedSessionExecutionAuth(serverapi.SessionExecutionFieldError{Code: serverapi.SessionExecutionFieldErrorSourceFailure, Message: err.Error()})
	}
	if status.Resolution.Kind == serverapi.AuthStatusResolutionUnavailable {
		return serverapi.FailedSessionExecutionAuth(serverapi.SessionExecutionFieldError{
			Code:    serverapi.SessionExecutionFieldErrorSourceFailure,
			Message: status.Resolution.Failure.Cause,
		})
	}
	return serverapi.AvailableSessionExecutionAuth(serverapi.SessionExecutionAuth{
		Provider: effectiveModel.Provider,
		Method:   serverapi.SessionExecutionAuthMethod(status.Resolution.Facts.Method),
	})
}

func sessionExecutionProviderUsesKentManagedAuth(provider string) bool {
	capabilities, err := llm.InferProviderCapabilities(strings.TrimSpace(provider))
	return err == nil && capabilities.IsOpenAIFirstParty
}

func (s *Service) resolveSnapshot(ctx context.Context, sessionID string) (sessionSnapshot, error) {
	if s == nil || s.snapshots == nil {
		return nil, errSessionStoreResolverRequired
	}
	return s.snapshots.resolveSessionSnapshot(ctx, sessionID)
}

var _ servicecontract.SessionViewService = (*Service)(nil)
