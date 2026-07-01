package sessionservice

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"

	"core/server/auth"
	"core/server/metadata"
	"core/server/requestmemo"
	"core/server/session"
	"core/shared/client"
	"core/shared/rollbacktarget"
	"core/shared/serverapi"
)

type SessionLifecycleService struct {
	persistenceRoot string
	containerDir    string
	stores          sessionStoreResolver
	authManager     *auth.Manager
	storeOptions    []session.StoreOption
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

type sessionStoreResolver interface {
	ResolveStore(ctx context.Context, sessionID string) (*session.Store, error)
}

func NewSessionLifecycleService(containerDir string, stores sessionStoreResolver, authManager *auth.Manager, storeOptions ...session.StoreOption) *SessionLifecycleService {
	return &SessionLifecycleService{containerDir: strings.TrimSpace(containerDir), stores: stores, authManager: authManager, storeOptions: append([]session.StoreOption(nil), storeOptions...), drafts: requestmemo.New[sessionDraftMemoRequest, serverapi.SessionPersistInputDraftResponse](), transitions: requestmemo.New[sessionTransitionMemoRequest, serverapi.SessionResolveTransitionResponse]()}
}

func NewGlobalSessionLifecycleService(persistenceRoot string, stores sessionStoreResolver, authManager *auth.Manager, storeOptions ...session.StoreOption) *SessionLifecycleService {
	return &SessionLifecycleService{persistenceRoot: strings.TrimSpace(persistenceRoot), stores: stores, authManager: authManager, storeOptions: append([]session.StoreOption(nil), storeOptions...), drafts: requestmemo.New[sessionDraftMemoRequest, serverapi.SessionPersistInputDraftResponse](), transitions: requestmemo.New[sessionTransitionMemoRequest, serverapi.SessionResolveTransitionResponse]()}
}

type MetadataBackedSessionLifecycleClient struct {
	mu     sync.RWMutex
	client client.SessionLifecycleClient
	store  *metadata.Store
}

func NewMetadataBackedSessionLifecycleClient(persistenceRoot string, authManager *auth.Manager) (*MetadataBackedSessionLifecycleClient, error) {
	store, err := metadata.Open(persistenceRoot)
	if err != nil {
		return nil, err
	}
	service := NewGlobalSessionLifecycleService(persistenceRoot, nil, authManager, store.AuthoritativeSessionStoreOptions()...)
	return &MetadataBackedSessionLifecycleClient{client: client.NewLoopbackSessionLifecycleClient(service), store: store}, nil
}

func (c *MetadataBackedSessionLifecycleClient) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	store := c.store
	c.client = nil
	c.store = nil
	if store == nil {
		return nil
	}
	return store.Close()
}

func (c *MetadataBackedSessionLifecycleClient) GetInitialInput(ctx context.Context, req serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
	return callMetadataBackedLoopbackClient(c, ctx, req, client.SessionLifecycleClient.GetInitialInput)
}

func (c *MetadataBackedSessionLifecycleClient) PersistInputDraft(ctx context.Context, req serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
	return callMetadataBackedLoopbackClient(c, ctx, req, client.SessionLifecycleClient.PersistInputDraft)
}

func (c *MetadataBackedSessionLifecycleClient) RetargetSessionWorkspace(ctx context.Context, req serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error) {
	return callMetadataBackedLoopbackClient(c, ctx, req, client.SessionLifecycleClient.RetargetSessionWorkspace)
}

func (c *MetadataBackedSessionLifecycleClient) ResolveTransition(ctx context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
	return callMetadataBackedLoopbackClient(c, ctx, req, client.SessionLifecycleClient.ResolveTransition)
}

func callMetadataBackedLoopbackClient[Req any, Resp any](c *MetadataBackedSessionLifecycleClient, ctx context.Context, req Req, call func(client.SessionLifecycleClient, context.Context, Req) (Resp, error)) (Resp, error) {
	if c == nil {
		var zero Resp
		return zero, errLifecycleClientClosed
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	client := c.client
	if client == nil {
		var zero Resp
		return zero, errLifecycleClientClosed
	}
	return call(client, ctx, req)
}

func (s *SessionLifecycleService) WithPersistenceRoot(root string) *SessionLifecycleService {
	if s == nil {
		return nil
	}
	s.persistenceRoot = strings.TrimSpace(root)
	return s
}

func (s *SessionLifecycleService) GetInitialInput(_ context.Context, req serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.SessionInitialInputResponse{}, err
	}
	store, err := s.openStore(req.SessionID)
	if err != nil {
		return serverapi.SessionInitialInputResponse{}, err
	}
	return serverapi.SessionInitialInputResponse{Input: initialSessionInput(store, req.TransitionInput)}, nil
}

func (s *SessionLifecycleService) PersistInputDraft(ctx context.Context, req serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.SessionPersistInputDraftResponse{}, err
	}
	memoReq := sessionDraftMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Input: req.Input}
	return s.drafts.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameSessionDraftMemoRequest, func(context.Context) (serverapi.SessionPersistInputDraftResponse, error) {
		store, err := s.openStore(req.SessionID)
		if err != nil {
			return serverapi.SessionPersistInputDraftResponse{}, err
		}
		if err := persistSessionInputDraft(store, req.Input); err != nil {
			return serverapi.SessionPersistInputDraftResponse{}, err
		}
		return serverapi.SessionPersistInputDraftResponse{}, nil
	})
}

func (s *SessionLifecycleService) RetargetSessionWorkspace(ctx context.Context, req serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.SessionRetargetWorkspaceResponse{}, err
	}
	if strings.TrimSpace(s.persistenceRoot) == "" {
		return serverapi.SessionRetargetWorkspaceResponse{}, errPersistenceRootRequired
	}
	metadataStore, err := metadata.Open(s.persistenceRoot)
	if err != nil {
		return serverapi.SessionRetargetWorkspaceResponse{}, err
	}
	defer func() { _ = metadataStore.Close() }()
	binding, err := metadataStore.RetargetSessionWorkspace(ctx, req.SessionID, req.WorkspaceRoot)
	if err != nil {
		return serverapi.SessionRetargetWorkspaceResponse{}, err
	}
	return serverapi.SessionRetargetWorkspaceResponse{Binding: serverapi.ProjectBinding{
		ProjectID:       binding.ProjectID,
		ProjectName:     binding.ProjectName,
		WorkspaceID:     binding.WorkspaceID,
		CanonicalRoot:   binding.CanonicalRoot,
		WorkspaceName:   binding.WorkspaceName,
		WorkspaceStatus: binding.WorkspaceStatus,
	}}, nil
}

func (s *SessionLifecycleService) ResolveTransition(ctx context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.SessionResolveTransitionResponse{}, err
	}
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
		a.Transition.InitialInput == b.Transition.InitialInput &&
		a.Transition.TargetSessionID == b.Transition.TargetSessionID &&
		a.Transition.ForkRollbackTargetID == b.Transition.ForkRollbackTargetID &&
		a.Transition.ParentSessionID == b.Transition.ParentSessionID
}

func sameSessionDraftMemoRequest(a sessionDraftMemoRequest, b sessionDraftMemoRequest) bool {
	return a.SessionID == b.SessionID && a.Input == b.Input
}

func (s *SessionLifecycleService) resolveTransitionOnce(ctx context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
	if req.Transition.Action == serverapi.SessionTransitionActionLogout {
		if s.authManager == nil {
			return serverapi.SessionResolveTransitionResponse{}, errors.New("auth manager is required for logout")
		}
		return serverapi.SessionResolveTransitionResponse{
			NextSessionID:  strings.TrimSpace(req.SessionID),
			ShouldContinue: true,
			RequiresReauth: true,
		}, nil
	}
	var (
		store *session.Store
		err   error
	)
	if req.Transition.Action == serverapi.SessionTransitionActionForkRollback {
		store, err = s.openStore(req.SessionID)
		if err != nil {
			return serverapi.SessionResolveTransitionResponse{}, err
		}
		forkUserMessageSeq, resolveErr := rollbacktarget.DecodeUserMessageSeq(req.Transition.ForkRollbackTargetID)
		if resolveErr != nil {
			return serverapi.SessionResolveTransitionResponse{}, resolveErr
		}
		resolved, err := resolveSessionTransition(ctx, sessionTransitionResolveRequest{
			Store: store,
			Transition: sessionTransition{
				Action:                       req.Transition.Action,
				InitialPrompt:                req.Transition.InitialPrompt,
				InitialPromptHistoryRecorded: req.Transition.InitialPromptHistoryRecorded,
				InitialInput:                 req.Transition.InitialInput,
				TargetSessionID:              req.Transition.TargetSessionID,
				ForkUserMessageSeq:           forkUserMessageSeq,
				ParentSessionID:              req.Transition.ParentSessionID,
			},
		})
		if err != nil {
			return serverapi.SessionResolveTransitionResponse{}, err
		}
		if err := s.preserveForkExecutionTarget(ctx, req.SessionID, resolved.NextSessionID); err != nil {
			return serverapi.SessionResolveTransitionResponse{}, err
		}
		return serverapi.SessionResolveTransitionResponse{
			NextSessionID:                resolved.NextSessionID,
			InitialPrompt:                resolved.InitialPrompt,
			InitialPromptHistoryRecorded: resolved.InitialPromptHistoryRecorded,
			InitialInput:                 resolved.InitialInput,
			ParentSessionID:              resolved.ParentSessionID,
			ForceNewSession:              resolved.ForceNewSession,
			ShouldContinue:               resolved.ShouldContinue,
		}, nil
	}
	resolved, err := resolveSessionTransition(ctx, sessionTransitionResolveRequest{
		Store: store,
		Transition: sessionTransition{
			Action:                       req.Transition.Action,
			InitialPrompt:                req.Transition.InitialPrompt,
			InitialPromptHistoryRecorded: req.Transition.InitialPromptHistoryRecorded,
			InitialInput:                 req.Transition.InitialInput,
			TargetSessionID:              req.Transition.TargetSessionID,
			ParentSessionID:              req.Transition.ParentSessionID,
		},
	})
	if err != nil {
		return serverapi.SessionResolveTransitionResponse{}, err
	}
	return serverapi.SessionResolveTransitionResponse{
		NextSessionID:                resolved.NextSessionID,
		InitialPrompt:                resolved.InitialPrompt,
		InitialPromptHistoryRecorded: resolved.InitialPromptHistoryRecorded,
		InitialInput:                 resolved.InitialInput,
		ParentSessionID:              resolved.ParentSessionID,
		ForceNewSession:              resolved.ForceNewSession,
		ShouldContinue:               resolved.ShouldContinue,
	}, nil
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
	return metadataStore.UpdateSessionExecutionTargetByID(ctx, trimmedChildID, target.WorkspaceID, target.WorktreeID, target.CwdRelpath)
}

func (s *SessionLifecycleService) openStore(sessionID string) (*session.Store, error) {
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return nil, nil
	}
	if s != nil && s.stores != nil {
		if store, err := s.stores.ResolveStore(context.Background(), trimmed); err != nil {
			return nil, err
		} else if store != nil {
			return store, nil
		}
	}
	if strings.TrimSpace(s.containerDir) == "" {
		if strings.TrimSpace(s.persistenceRoot) == "" {
			return nil, nil
		}
		return session.OpenByID(s.persistenceRoot, trimmed, s.storeOptions...)
	}
	sessionDir, err := session.ResolveScopedSessionDir(s.containerDir, trimmed)
	if err != nil {
		return nil, err
	}
	return session.Open(sessionDir, s.storeOptions...)
}
