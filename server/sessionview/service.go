package sessionview

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"core/server/auth"
	"core/server/chatcontext"
	"core/server/launch"
	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/server/worktree"
	servicecontract "core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/invariant"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type PersistedSessionResolver = session.PersistedSessionResolver

type ExecutionTargetResolver interface {
	ResolveSessionExecutionTarget(ctx context.Context, sessionID string) (clientui.SessionExecutionTarget, error)
}

type chatContextWorkspaceResolver interface {
	Resolve(workspaceRoot string) (config.App, error)
}

type chatContextAuthReader interface {
	Load(context.Context) (auth.State, error)
}

type Service struct {
	persisted         PersistedSessionResolver
	mainViews         runtimeMainViewSnapshotProvider
	targets           ExecutionTargetResolver
	app               config.App
	auth              servicecontract.AuthStatusService
	git               *worktree.GitInspector
	cacheWarningMode  config.CacheWarningMode
	contextWorkspaces chatContextWorkspaceResolver
	contextAuth       chatContextAuthReader
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
	sessions PersistedSessionResolver,
	mainViews runtimeMainViewSnapshotProvider,
	targets ExecutionTargetResolver,
) *Service {
	svc := &Service{
		persisted:        sessions,
		mainViews:        mainViews,
		targets:          targets,
		cacheWarningMode: config.CacheWarningModeDefault,
	}
	return svc
}

func (s *Service) SubscribeQuestionHistory(
	ctx context.Context,
	req serverapi.QuestionHistorySubscribeRequest,
) (serverapi.QuestionHistorySubscription, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if s == nil || s.persisted == nil {
		return nil, errPersistedSessionResolverRequired
	}
	record, err := s.persisted.ResolvePersistedSession(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	cursor, err := session.OpenQuestionHistoryCursor(record.SessionDir, req.MaxHandoffs)
	if err != nil {
		return nil, err
	}
	return &questionHistorySubscription{cursor: cursor}, nil
}

func (s *Service) WithChatContextWorkspaceResolver(resolver chatContextWorkspaceResolver) *Service {
	if s != nil {
		s.contextWorkspaces = resolver
	}
	return s
}

func (s *Service) WithChatContextAuthReader(reader chatContextAuthReader) *Service {
	if s != nil {
		s.contextAuth = reader
	}
	return s
}

func (s *Service) ReadSessionChatContext(ctx context.Context, sessionID runtimeids.SessionID) (serverapi.ChatContext, error) {
	if s == nil || s.persisted == nil {
		return serverapi.ChatContext{}, errors.New("persisted Session resolver is required")
	}
	return s.readDormantSessionChatContext(ctx, sessionID)
}

func (s *Service) readDormantSessionChatContext(ctx context.Context, sessionID runtimeids.SessionID) (serverapi.ChatContext, error) {
	if s.targets == nil {
		return serverapi.ChatContext{}, errors.New("Session execution-target resolver is required")
	}
	if s.contextWorkspaces == nil {
		return serverapi.ChatContext{}, errors.New("fresh workspace config resolver is required")
	}
	record, err := session.ResolvePersistedSessionRecord(ctx, s.persisted, sessionID.String())
	if err != nil {
		return serverapi.ChatContext{}, err
	}
	snapshot := session.ContextSnapshot{Meta: *record.Meta, Facts: record.ContextFacts}
	target, err := s.targets.ResolveSessionExecutionTarget(ctx, sessionID.String())
	if err != nil {
		return serverapi.ChatContext{}, err
	}
	executionRoot, err := clientui.SessionExecutionWorkspaceRoot(target, target.WorkspaceRoot)
	if err != nil {
		return serverapi.ChatContext{}, err
	}
	app, err := s.contextWorkspaces.Resolve(executionRoot)
	if err != nil {
		return serverapi.ChatContext{}, err
	}
	current, err := launch.ResolveReadOnlySessionContextSettings(app, snapshot.Meta, false)
	if err != nil {
		return serverapi.ChatContext{}, err
	}
	provider, err := llm.ResolveEffectiveProviderCapabilities(
		ctx,
		snapshot.Meta.Locked,
		current.Settings,
		s.contextAuth,
	)
	if err != nil {
		return serverapi.ChatContext{}, err
	}
	usedTokens := int64(0)
	if snapshot.Meta.UsageState != nil {
		usedTokens = int64(snapshot.Meta.UsageState.InputTokens)
	}
	completedCount := int64(0)
	if snapshot.Facts.CompletedCompactionCount != nil {
		completedCount = int64(*snapshot.Facts.CompletedCompactionCount)
	}
	manualEligible := snapshot.Facts.ManualCompactEligible != nil && *snapshot.Facts.ManualCompactEligible
	return chatcontext.Project(chatcontext.ProjectionInput{
		Policy:                   chatcontext.ResolvePolicy(current.Settings, provider.Capabilities, snapshot.Meta.Locked),
		UsedTokens:               usedTokens,
		AutoCompactionEnabled:    current.AutoCompactionEnabled,
		CompletedCompactionCount: completedCount,
		ManualCompactEligible:    manualEligible,
	}), nil
}

func (s *Service) WithCacheWarningMode(mode config.CacheWarningMode) *Service {
	if s == nil {
		return nil
	}
	s.cacheWarningMode = normalizeServiceCacheWarningMode(mode)
	return s
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
	if err := req.Validate(); err != nil {
		return serverapi.SessionMainViewResponse{}, err
	}
	view, err := s.resolveMainView(ctx, req.SessionID)
	if err != nil {
		return serverapi.SessionMainViewResponse{}, err
	}
	if s.targets != nil && strings.TrimSpace(view.Session.SessionID) != "" {
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
	view, err := resolvePersistedSessionView(ctx, s.persisted, sessionID)
	if err != nil {
		return nil, err
	}
	return (dormantSessionSnapshot{view: view, cacheWarningMode: s.cacheWarningMode}).TranscriptTailEntries(ctx)
}

func (s *Service) GetSessionTranscriptPage(ctx context.Context, req serverapi.SessionTranscriptPageRequest) (serverapi.SessionTranscriptPageResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.SessionTranscriptPageResponse{}, err
	}
	pageReq := clientui.TranscriptPageRequest{Cursor: req.Cursor, NewerCursor: req.NewerCursor}
	view, err := resolvePersistedSessionView(ctx, s.persisted, req.SessionID)
	if err != nil {
		return serverapi.SessionTranscriptPageResponse{}, err
	}
	page, err := (dormantSessionSnapshot{view: view, cacheWarningMode: s.cacheWarningMode}).TranscriptPage(ctx, pageReq)
	if err != nil {
		return serverapi.SessionTranscriptPageResponse{}, err
	}
	response := serverapi.SessionTranscriptPageResponse{Transcript: page}
	if err := validateSessionTranscriptPageResponse(response); err != nil {
		return serverapi.SessionTranscriptPageResponse{}, err
	}
	return response, nil
}

func validateSessionTranscriptPageResponse(response serverapi.SessionTranscriptPageResponse) error {
	if err := response.Validate(); err != nil {
		invariant.NewPolicy().Check(false, invariant.ReadModelPublicationDiagnostic(
			invariant.ReadModelPublicationDiagnosticInput{
				Operation:        "session_view.transcript_page",
				SessionID:        response.Transcript.SessionID,
				PublicationCause: err.Error(),
				OwnerSnapshots:   "canonical_transcript_page",
				ResolverInputs:   "session_transcript_page",
			},
		))
		return fmt.Errorf("validate session transcript page response: %w", err)
	}
	return nil
}

func (s *Service) GetLatestCommittedAssistantFinalAnswer(ctx context.Context, req serverapi.SessionLatestCommittedAssistantFinalAnswerRequest) (serverapi.SessionLatestCommittedAssistantFinalAnswerResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{}, err
	}
	if s == nil {
		return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{}, errPersistedSessionResolverRequired
	}
	view, err := resolvePersistedSessionView(ctx, s.persisted, req.SessionID)
	if err != nil {
		return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{}, err
	}
	answer, err := runtime.LatestCommittedAssistantFinalAnswerFromEventLog(view)
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
	if s == nil || s.persisted == nil {
		return serverapi.SessionExecutionEnvironmentResponse{}, errPersistedSessionResolverRequired
	}
	record, err := session.ResolvePersistedSessionRecord(ctx, s.persisted, req.SessionID.String())
	if err != nil {
		return serverapi.SessionExecutionEnvironmentResponse{}, err
	}
	meta := *record.Meta
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
	if err := status.Validate(); err != nil {
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

var _ servicecontract.SessionViewService = (*Service)(nil)
var _ chatcontext.SessionOwner = (*Service)(nil)
