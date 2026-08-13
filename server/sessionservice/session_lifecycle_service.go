package sessionservice

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"core/server/auth"
	"core/server/metadata"
	"core/server/requestmemo"
	"core/server/session"
	"core/server/sessionruntime"
	"core/shared/apicontract"
	servicecontract "core/shared/apicontract"
	"core/shared/rollbacktarget"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

var errSessionWorkspaceRetargeterRequired = errors.New("session workspace retargeter is required")

type SessionLifecycleService struct {
	persistenceRoot string
	containerDir    string
	authority       *sessionruntime.Authority
	retargeter      sessionWorkspaceRetargeter
	navigation      sessionNavigationTargetResolver
	authManager     *auth.Manager
	drafts          *requestmemo.Memo[sessionDraftMemoRequest, serverapi.SessionPersistInputDraftResponse]
	transitions     *requestmemo.Memo[sessionTransitionMemoRequest, serverapi.SessionResolveTransitionResponse]
}

type sessionDraftMemoRequest struct {
	SessionID string
	Input     string
}

type sessionTransitionMemoRequest struct {
	SessionID  string
	Transition serverapi.SessionTransition
}

type sessionWorkspaceRetargeter interface {
	RetargetWorkspace(ctx context.Context, req metadata.SessionWorkspaceRetargetRequest) (metadata.SessionWorkspaceRetargetResult, error)
}

type sessionNavigationTargetResolver interface {
	ResolveSessionNavigationBinding(ctx context.Context, sessionID string) (serverapi.SessionNavigationBinding, error)
}

func NewSessionLifecycleService(persistenceRoot string, authority *sessionruntime.Authority, authManager *auth.Manager) *SessionLifecycleService {
	return &SessionLifecycleService{
		containerDir: strings.TrimSpace(persistenceRoot),
		authority:    authority,
		authManager:  authManager,
		drafts:       requestmemo.New[sessionDraftMemoRequest, serverapi.SessionPersistInputDraftResponse](),
		transitions:  requestmemo.New[sessionTransitionMemoRequest, serverapi.SessionResolveTransitionResponse](),
	}
}

func NewGlobalSessionLifecycleService(persistenceRoot string, authority *sessionruntime.Authority, authManager *auth.Manager) *SessionLifecycleService {
	return &SessionLifecycleService{
		persistenceRoot: strings.TrimSpace(persistenceRoot),
		authority:       authority,
		authManager:     authManager,
		drafts:          requestmemo.New[sessionDraftMemoRequest, serverapi.SessionPersistInputDraftResponse](),
		transitions:     requestmemo.New[sessionTransitionMemoRequest, serverapi.SessionResolveTransitionResponse](),
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
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.SessionInitialInputRequest]) (serverapi.SessionInitialInputResponse, error) {
		return s.getInitialInput(ctx, validated.Value())
	})
}

func (s *SessionLifecycleService) GetInitialInputValidated(ctx context.Context, req servicecontract.Validated[serverapi.SessionInitialInputRequest], _ servicecontract.OptionalAuthorizedSessionInActiveProject) (serverapi.SessionInitialInputResponse, error) {
	return s.getInitialInput(ctx, req.Value())
}

func (s *SessionLifecycleService) getInitialInput(ctx context.Context, req serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
	if strings.TrimSpace(req.SessionID) == "" {
		return serverapi.SessionInitialInputResponse{Input: req.TransitionInput}, nil
	}
	var resp serverapi.SessionInitialInputResponse
	err := s.withStore(ctx, req.SessionID, func(_ context.Context, store *session.Store) error {
		if req.OverrideStoredDraft {
			resp.Input = req.TransitionInput
			return nil
		}
		resp = serverapi.SessionInitialInputResponse{
			Input: initialSessionInput(store, req.TransitionInput),
		}
		return nil
	})
	if err != nil {
		return serverapi.SessionInitialInputResponse{}, err
	}
	return resp, nil
}

func (s *SessionLifecycleService) PersistInputDraft(ctx context.Context, req serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.SessionPersistInputDraftRequest]) (serverapi.SessionPersistInputDraftResponse, error) {
		return s.persistInputDraft(ctx, validated.Value())
	})
}

func (s *SessionLifecycleService) PersistInputDraftValidated(ctx context.Context, req servicecontract.Validated[serverapi.SessionPersistInputDraftRequest], _ servicecontract.AuthorizedSessionInActiveProject) (serverapi.SessionPersistInputDraftResponse, error) {
	return s.persistInputDraft(ctx, req.Value())
}

func (s *SessionLifecycleService) persistInputDraft(ctx context.Context, req serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
	memoReq := sessionDraftMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Input: req.Input}
	return s.drafts.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameSessionDraftMemoRequest, func(runCtx context.Context) (serverapi.SessionPersistInputDraftResponse, error) {
		err := s.withStore(runCtx, req.SessionID, func(_ context.Context, store *session.Store) error {
			return persistSessionInputDraft(store, req.Input)
		})
		return serverapi.SessionPersistInputDraftResponse{}, err
	})
}

func (s *SessionLifecycleService) RetargetSessionWorkspace(ctx context.Context, req serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error) {
	return apicontract.WithValidated(
		req,
		apicontract.SemanticValidationRequired,
		func(validated apicontract.Validated[serverapi.SessionRetargetWorkspaceRequest]) (serverapi.SessionRetargetWorkspaceResponse, error) {
			return s.RetargetSessionWorkspaceValidated(ctx, validated, apicontract.AbsentAttachedProjectConstraint())
		},
	)
}

func (s *SessionLifecycleService) RetargetSessionWorkspaceValidated(
	ctx context.Context,
	validated apicontract.Validated[serverapi.SessionRetargetWorkspaceRequest],
	constraint apicontract.AttachedProjectConstraint,
) (serverapi.SessionRetargetWorkspaceResponse, error) {
	if s == nil || s.retargeter == nil {
		return serverapi.SessionRetargetWorkspaceResponse{}, errSessionWorkspaceRetargeterRequired
	}
	req := validated.Value()
	result, err := s.retargeter.RetargetWorkspace(ctx, metadata.SessionWorkspaceRetargetRequest{
		SessionID:                 req.SessionID,
		WorkspaceRoot:             req.WorkspaceRoot,
		ProjectID:                 req.ProjectID,
		AttachedProjectConstraint: constraint,
	})
	if err != nil {
		var mismatch *metadata.AttachedProjectMismatchError
		if errors.As(err, &mismatch) {
			return serverapi.SessionRetargetWorkspaceResponse{}, fmt.Errorf("session %q not available", mismatch.SessionID)
		}
		return serverapi.SessionRetargetWorkspaceResponse{}, err
	}
	binding := result.Binding
	return serverapi.SessionRetargetWorkspaceResponse{Binding: serverapi.ProjectBinding{
		ProjectID:       binding.ProjectID,
		ProjectKey:      binding.ProjectKey,
		ProjectName:     binding.ProjectName,
		WorkspaceID:     binding.WorkspaceID,
		CanonicalRoot:   binding.CanonicalRoot,
		WorkspaceName:   binding.WorkspaceName,
		WorkspaceStatus: binding.WorkspaceStatus,
	}, WorkspaceBindingCreated: result.WorkspaceBindingCreated}, nil
}

func (s *SessionLifecycleService) ResolveTransition(ctx context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.SessionResolveTransitionRequest]) (serverapi.SessionResolveTransitionResponse, error) {
		return s.resolveTransition(ctx, validated.Value())
	})
}

func (s *SessionLifecycleService) ResolveTransitionValidated(ctx context.Context, req servicecontract.Validated[serverapi.SessionResolveTransitionRequest], _ servicecontract.OptionalAuthorizedSessionInActiveProject) (serverapi.SessionResolveTransitionResponse, error) {
	return s.resolveTransition(ctx, req.Value())
}

func (s *SessionLifecycleService) resolveTransition(ctx context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
	memoReq := sessionTransitionMemoRequest{
		SessionID:  strings.TrimSpace(req.SessionID),
		Transition: req.Transition,
	}
	return s.transitions.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameSessionTransitionMemoRequest, func(context.Context) (serverapi.SessionResolveTransitionResponse, error) {
		return s.resolveTransitionOnce(ctx, req)
	})
}

func sameSessionTransitionMemoRequest(a sessionTransitionMemoRequest, b sessionTransitionMemoRequest) bool {
	return a.SessionID == b.SessionID &&
		a.Transition.Action == b.Transition.Action &&
		a.Transition.InitialPrompt == b.Transition.InitialPrompt &&
		a.Transition.InitialPromptHistoryRecorded == b.Transition.InitialPromptHistoryRecorded &&
		textutil.EqualOptional(a.Transition.InitialInput, b.Transition.InitialInput) &&
		a.Transition.TargetSessionID == b.Transition.TargetSessionID &&
		a.Transition.ForkRollbackTargetID == b.Transition.ForkRollbackTargetID &&
		textutil.EqualOptional(a.Transition.PreviousSessionID, b.Transition.PreviousSessionID)
}

func sameSessionDraftMemoRequest(a sessionDraftMemoRequest, b sessionDraftMemoRequest) bool {
	return a.SessionID == b.SessionID && a.Input == b.Input
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
	if req.Transition.Action == serverapi.SessionTransitionActionForkRollback ||
		req.Transition.Action == serverapi.SessionTransitionActionOpenSession {
		var resolved serverapi.SessionResolveTransitionResponse
		err := s.withStore(ctx, req.SessionID, func(runCtx context.Context, store *session.Store) error {
			var err error
			resolved, err = s.resolveStoreTransition(runCtx, req, store)
			return err
		})
		return resolved, err
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

func (s *SessionLifecycleService) resolveStoreTransition(
	ctx context.Context,
	req serverapi.SessionResolveTransitionRequest,
	store *session.Store,
) (serverapi.SessionResolveTransitionResponse, error) {
	transition := sessionTransition{
		Action:                       req.Transition.Action,
		InitialPrompt:                req.Transition.InitialPrompt,
		InitialPromptHistoryRecorded: req.Transition.InitialPromptHistoryRecorded,
		InitialInput:                 textutil.Pointer(req.Transition.InitialInput),
		TargetSessionID:              req.Transition.TargetSessionID,
		PreviousSessionID:            req.Transition.PreviousSessionID,
	}
	if req.Transition.Action == serverapi.SessionTransitionActionForkRollback {
		forkUserMessageSeq, err := rollbacktarget.DecodeUserMessageSeq(req.Transition.ForkRollbackTargetID)
		if err != nil {
			return serverapi.SessionResolveTransitionResponse{}, err
		}
		transition.ForkUserMessageSeq = forkUserMessageSeq
	}
	resolved, err := resolveSessionTransition(ctx, sessionTransitionResolveRequest{
		Store:      store,
		Transition: transition,
	})
	if err != nil {
		return serverapi.SessionResolveTransitionResponse{}, err
	}
	if req.Transition.Action == serverapi.SessionTransitionActionOpenSession {
		return s.authorizeNavigationTransition(ctx, store, resolved)
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

func (s *SessionLifecycleService) authorizeNavigationTransition(ctx context.Context, current *session.Store, resolved serverapi.SessionDirective) (serverapi.SessionDirective, error) {
	if current == nil {
		return serverapi.SessionDirective{}, errors.New("current session is required for session navigation")
	}
	intent, present := resolved.LaunchIntent()
	if !present {
		return serverapi.SessionDirective{}, errors.New("session navigation did not resolve to a launch intent")
	}
	requestedTarget, present := intent.SessionID()
	if !present {
		return serverapi.SessionDirective{}, errors.New("session navigation launch intent omitted target session id")
	}
	authorizedTarget := session.NavigationTargetSessionID(current.Meta())
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
