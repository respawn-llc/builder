package sessionservice

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"core/server/auth"
	"core/server/metadata"
	"core/server/session"
	"core/server/sessionruntime"
	"core/shared/rollbacktarget"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
	"core/shared/worktreecontract"
)

var errSessionWorkspaceRetargeterRequired = errors.New("session workspace retargeter is required")

type SessionLifecycleService struct {
	persistenceRoot string
	containerDir    string
	authority       *sessionruntime.Authority
	retargeter      sessionWorkspaceRetargeter
	navigation      sessionNavigationTargetResolver
	authManager     *auth.Manager
	persisted       session.PersistedSessionResolver
}

func (s *SessionLifecycleService) WithPersistedSessionResolver(resolver session.PersistedSessionResolver) *SessionLifecycleService {
	if s != nil {
		s.persisted = resolver
	}
	return s
}

type sessionWorkspaceRetargeter interface {
	RetargetWorkspace(ctx context.Context, req metadata.SessionWorkspaceRetargetRequest) (metadata.SessionWorkspaceRetargetResult, error)
	ScheduleWorkspaceRetarget(ctx context.Context, req metadata.SessionWorkspaceRetargetRequest, origin serverapi.RuntimeStepOrigin, operationID worktreecontract.OperationID) (serverapi.SessionWorkspaceRetargetScheduledAcknowledgement, error)
}

type sessionNavigationTargetResolver interface {
	ResolveSessionNavigationBinding(ctx context.Context, sessionID string) (serverapi.SessionNavigationBinding, error)
}

func NewSessionLifecycleService(persistenceRoot string, authority *sessionruntime.Authority, authManager *auth.Manager) *SessionLifecycleService {
	return &SessionLifecycleService{
		containerDir: strings.TrimSpace(persistenceRoot),
		authority:    authority,
		authManager:  authManager,
	}
}

func NewGlobalSessionLifecycleService(persistenceRoot string, authority *sessionruntime.Authority, authManager *auth.Manager) *SessionLifecycleService {
	return &SessionLifecycleService{
		persistenceRoot: strings.TrimSpace(persistenceRoot),
		authority:       authority,
		authManager:     authManager,
	}
}

func (s *SessionLifecycleService) WithPersistenceRoot(root string) *SessionLifecycleService {
	if s != nil {
		s.persistenceRoot = strings.TrimSpace(root)
	}
	return s
}

func (s *SessionLifecycleService) WithWorkspaceRetargeter(retargeter sessionWorkspaceRetargeter) *SessionLifecycleService {
	if s == nil {
		return nil
	}
	s.retargeter = retargeter
	return s
}

func (s *SessionLifecycleService) WithNavigationTargetResolver(resolver sessionNavigationTargetResolver) *SessionLifecycleService {
	if s == nil {
		return nil
	}
	s.navigation = resolver
	return s
}

func (s *SessionLifecycleService) GetInitialInput(ctx context.Context, req serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.SessionInitialInputResponse{}, err
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return serverapi.SessionInitialInputResponse{Input: req.TransitionInput}, nil
	}
	if req.OverrideStoredDraft {
		return serverapi.SessionInitialInputResponse{Input: req.TransitionInput}, nil
	}
	meta, err := s.resolvePersistedSessionMeta(ctx, req.SessionID)
	if err != nil {
		return serverapi.SessionInitialInputResponse{}, err
	}
	return serverapi.SessionInitialInputResponse{Input: initialSessionInput(meta, req.TransitionInput)}, nil
}

func (s *SessionLifecycleService) resolvePersistedSessionMeta(ctx context.Context, sessionID string) (session.Meta, error) {
	if s == nil || s.persisted == nil {
		return session.Meta{}, errors.New("persisted Session resolver is required")
	}
	var (
		record session.PersistedSessionRecord
		err    error
	)
	if containerDir := strings.TrimSpace(s.containerDir); containerDir != "" {
		record, err = session.ResolveScopedPersistedSessionRecord(ctx, s.persisted, containerDir, sessionID)
	} else {
		record, err = session.ResolvePersistedSessionRecord(ctx, s.persisted, sessionID)
	}
	if err != nil {
		return session.Meta{}, err
	}
	return *record.Meta, nil
}

func (s *SessionLifecycleService) PersistInputDraft(ctx context.Context, req serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.SessionPersistInputDraftResponse{}, err
	}
	err := s.withStore(ctx, req.SessionID, func(_ context.Context, store *session.Store) error {
		return persistSessionInputDraft(store, req.Input)
	})
	return serverapi.SessionPersistInputDraftResponse{}, err
}

func (s *SessionLifecycleService) RetargetSessionWorkspace(ctx context.Context, req serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.SessionRetargetWorkspaceResponse{}, err
	}
	if s == nil || s.retargeter == nil {
		return serverapi.SessionRetargetWorkspaceResponse{}, errSessionWorkspaceRetargeterRequired
	}
	retargetRequest := metadata.SessionWorkspaceRetargetRequest{
		SessionID:     req.SessionID,
		WorkspaceRoot: req.WorkspaceRoot,
		ProjectID:     req.ProjectID,
	}
	if req.Origin != nil {
		acknowledgement, err := s.retargeter.ScheduleWorkspaceRetarget(
			ctx,
			retargetRequest,
			*req.Origin,
			worktreecontract.NewOperationID(),
		)
		if err != nil {
			return serverapi.SessionRetargetWorkspaceResponse{}, err
		}
		return serverapi.SessionRetargetWorkspaceResponse{Scheduled: &acknowledgement}, nil
	}
	result, err := s.retargeter.RetargetWorkspace(ctx, retargetRequest)
	if err != nil {
		return serverapi.SessionRetargetWorkspaceResponse{}, err
	}
	binding := result.Binding
	bindingResponse := serverapi.ProjectBinding{
		ProjectID:       binding.ProjectID,
		ProjectKey:      binding.ProjectKey,
		ProjectName:     binding.ProjectName,
		WorkspaceID:     binding.WorkspaceID,
		CanonicalRoot:   binding.CanonicalRoot,
		WorkspaceName:   binding.WorkspaceName,
		WorkspaceStatus: binding.WorkspaceStatus,
	}
	return serverapi.SessionRetargetWorkspaceResponse{
		Binding:                 &bindingResponse,
		WorkspaceBindingCreated: result.WorkspaceBindingCreated,
	}, nil
}

func (s *SessionLifecycleService) ResolveTransition(ctx context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.SessionResolveTransitionResponse{}, err
	}
	return s.resolveTransitionOnce(ctx, req)
}

func (s *SessionLifecycleService) resolveTransitionOnce(ctx context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
	if req.Transition.Action == serverapi.SessionTransitionActionLogout {
		if s.authManager == nil {
			return serverapi.SessionResolveTransitionResponse{}, errors.New("auth manager is required for logout")
		}
		currentID := strings.TrimSpace(req.SessionID)
		if currentID == "" {
			return serverapi.SelectSessionDirective(serverapi.SessionAuthPreparationReauthenticate), nil
		}
		sessionID, err := runtimeids.ParseSessionID(currentID)
		if err != nil {
			return serverapi.SessionResolveTransitionResponse{}, err
		}
		return serverapi.LaunchSessionDirective(
			serverapi.OpenExistingSessionLaunchIntent(sessionID),
			serverapi.NewSessionLaunchPreparation(
				nil,
				serverapi.RestoreStoredDraftSessionDraftDisposition(),
				serverapi.SessionAuthPreparationReauthenticate,
			),
		), nil
	}
	if req.Transition.Action == serverapi.SessionTransitionActionForkRollback {
		var resolved serverapi.SessionResolveTransitionResponse
		err := s.withStore(ctx, req.SessionID, func(runCtx context.Context, store *session.Store) error {
			var err error
			resolved, err = s.resolveForkRollbackTransition(runCtx, req, store)
			return err
		})
		return resolved, err
	}
	if req.Transition.Action == serverapi.SessionTransitionActionOpenSession {
		meta, err := s.resolvePersistedSessionMeta(ctx, strings.TrimSpace(req.SessionID))
		if err != nil {
			return serverapi.SessionResolveTransitionResponse{}, err
		}
		resolved, err := resolveSessionTransition(ctx, sessionTransitionResolveRequest{Transition: sessionTransition{
			Action: req.Transition.Action, InitialInput: textutil.Pointer(req.Transition.InitialInput), TargetSessionID: req.Transition.TargetSessionID,
		}})
		if err != nil {
			return serverapi.SessionResolveTransitionResponse{}, err
		}
		return s.authorizeNavigationTransition(ctx, meta, resolved)
	}
	return resolveSessionTransition(ctx, sessionTransitionResolveRequest{
		Transition: sessionTransition{
			Action:                       req.Transition.Action,
			InitialPrompt:                req.Transition.InitialPrompt,
			InitialPromptHistoryRecorded: req.Transition.InitialPromptHistoryRecorded,
			InitialInput:                 textutil.Pointer(req.Transition.InitialInput),
			TargetSessionID:              req.Transition.TargetSessionID,
			PreviousSessionID:            req.Transition.PreviousSessionID,
		},
	})
}

func (s *SessionLifecycleService) resolveForkRollbackTransition(ctx context.Context, req serverapi.SessionResolveTransitionRequest, store *session.Store) (serverapi.SessionResolveTransitionResponse, error) {
	transition := sessionTransition{
		Action:                       req.Transition.Action,
		InitialPrompt:                req.Transition.InitialPrompt,
		InitialPromptHistoryRecorded: req.Transition.InitialPromptHistoryRecorded,
		InitialInput:                 textutil.Pointer(req.Transition.InitialInput),
		TargetSessionID:              req.Transition.TargetSessionID,
		PreviousSessionID:            req.Transition.PreviousSessionID,
	}
	forkUserMessageSeq, err := rollbacktarget.DecodeUserMessageSeq(req.Transition.ForkRollbackTargetID)
	if err != nil {
		return serverapi.SessionResolveTransitionResponse{}, err
	}
	transition.ForkUserMessageSeq = forkUserMessageSeq
	resolved, err := resolveSessionTransition(ctx, sessionTransitionResolveRequest{
		Store:      store,
		Transition: transition,
	})
	if err != nil {
		return serverapi.SessionResolveTransitionResponse{}, err
	}
	intent, ok := resolved.LaunchIntent()
	if !ok {
		return serverapi.SessionResolveTransitionResponse{}, errors.New("rollback transition did not resolve to a launch intent")
	}
	forkID, ok := intent.SessionID()
	if !ok {
		return serverapi.SessionResolveTransitionResponse{}, errors.New("rollback transition launch intent omitted fork session ID")
	}
	if err := s.preserveForkExecutionTarget(ctx, req.SessionID, forkID.String()); err != nil {
		return serverapi.SessionResolveTransitionResponse{}, err
	}
	return resolved, nil
}

func (s *SessionLifecycleService) authorizeNavigationTransition(ctx context.Context, current session.Meta, resolved serverapi.SessionDirective) (serverapi.SessionDirective, error) {
	intent, present := resolved.LaunchIntent()
	if !present {
		return serverapi.SessionDirective{}, errors.New("session navigation did not resolve to a launch intent")
	}
	requestedTarget, present := intent.SessionID()
	if !present {
		return serverapi.SessionDirective{}, errors.New("session navigation launch intent omitted target session id")
	}
	authorizedTarget := session.NavigationTargetSessionID(current)
	if authorizedTarget == nil || *authorizedTarget != requestedTarget {
		return serverapi.SessionDirective{}, errors.New("session navigation target does not match current session provenance")
	}
	if s == nil || s.navigation == nil {
		return serverapi.SessionDirective{}, errors.New("session navigation target resolver is required")
	}
	binding, err := s.navigation.ResolveSessionNavigationBinding(ctx, requestedTarget.String())
	if err != nil {
		return serverapi.SessionDirective{}, err
	}
	if err := binding.Validate(); err != nil {
		return serverapi.SessionDirective{}, err
	}
	preparation, present := resolved.LaunchPreparation()
	if !present {
		return serverapi.SessionDirective{}, errors.New("session navigation did not resolve launch preparation")
	}
	initialPrompt, hasInitialPrompt := preparation.InitialPrompt()
	var initialPromptPtr *serverapi.SessionInitialPromptMetadata
	if hasInitialPrompt {
		initialPromptPtr = &initialPrompt
	}
	return serverapi.LaunchSessionDirective(
		intent,
		serverapi.NewSessionNavigationLaunchPreparation(
			initialPromptPtr,
			preparation.DraftDisposition(),
			preparation.AuthPreparation(),
			binding,
		),
	), nil
}

func (s *SessionLifecycleService) preserveForkExecutionTarget(ctx context.Context, parentSessionID string, childSessionID string) error {
	if s == nil {
		return nil
	}
	trimmedParentID := strings.TrimSpace(parentSessionID)
	trimmedChildID := strings.TrimSpace(childSessionID)
	if trimmedParentID == "" || trimmedChildID == "" || trimmedParentID == trimmedChildID {
		return nil
	}
	if strings.TrimSpace(s.persistenceRoot) == "" {
		return nil
	}
	metadataStore, err := metadata.Open(s.persistenceRoot)
	if err != nil {
		return err
	}
	defer func() { _ = metadataStore.Close() }()
	target, err := metadataStore.ResolveSessionExecutionTarget(ctx, trimmedParentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, session.ErrSessionNotFound) {
			return nil
		}
		return err
	}
	return metadataStore.UpdateSessionExecutionTarget(ctx, metadata.SessionExecutionTargetUpdateFromReadModel(trimmedChildID, target))
}

func (s *SessionLifecycleService) withStore(
	ctx context.Context,
	sessionID string,
	callback func(context.Context, *session.Store) error,
) error {
	if s == nil || s.authority == nil {
		return errors.New("session runtime authority is required")
	}
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	var descriptor session.SessionDescriptor
	if containerDir := strings.TrimSpace(s.containerDir); containerDir != "" {
		descriptor, err = session.NewScopedOpenSessionDescriptor(id, containerDir)
	} else {
		descriptor, err = session.NewOpenSessionDescriptor(id)
	}
	if err != nil {
		return err
	}
	return s.authority.WithSessionStore(ctx, descriptor, callback)
}
