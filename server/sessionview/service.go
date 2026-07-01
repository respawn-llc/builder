package sessionview

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/runtime"
	"core/server/session"
	servicecontract "core/shared/apicontract"
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

type ExternalRuntimeStatusResolver interface {
	ExternalRuntimeStatus(sessionID string) clientui.ExternalRuntimeStatus
}

type ExecutionTargetResolver interface {
	ResolveSessionExecutionTarget(ctx context.Context, sessionID string) (clientui.SessionExecutionTarget, error)
}

type UpdateStatusProvider interface {
	Status(ctx context.Context) clientui.UpdateStatus
}

type Service struct {
	snapshots        SessionSnapshotSource
	updates          UpdateStatusProvider
	cacheWarningMu   sync.RWMutex
	cacheWarningMode config.CacheWarningMode
}

func NewService(sessions SessionStoreResolver, runtimes RuntimeResolver, targets ExecutionTargetResolver) *Service {
	svc := &Service{
		cacheWarningMode: config.CacheWarningModeDefault,
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
	snapshot, err := s.resolveSnapshot(ctx, req.SessionID)
	if err != nil {
		return serverapi.SessionMainViewResponse{}, err
	}
	view, err := snapshot.MainView(ctx)
	if err != nil {
		return serverapi.SessionMainViewResponse{}, err
	}
	return serverapi.SessionMainViewResponse{MainView: view}, nil
}

func (s *Service) GetSessionTranscriptPage(ctx context.Context, req serverapi.SessionTranscriptPageRequest) (serverapi.SessionTranscriptPageResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.SessionTranscriptPageResponse{}, err
	}
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

func (s *Service) GetSessionCommittedTranscriptSuffix(ctx context.Context, req serverapi.SessionCommittedTranscriptSuffixRequest) (serverapi.SessionCommittedTranscriptSuffixResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.SessionCommittedTranscriptSuffixResponse{}, err
	}
	snapshot, err := s.resolveSnapshot(ctx, req.SessionID)
	if err != nil {
		return serverapi.SessionCommittedTranscriptSuffixResponse{}, err
	}
	suffix, err := snapshot.CommittedTranscriptSuffix(ctx, clientui.CommittedTranscriptSuffixRequest{})
	if err != nil {
		return serverapi.SessionCommittedTranscriptSuffixResponse{}, err
	}
	return serverapi.SessionCommittedTranscriptSuffixResponse{Suffix: suffix}, nil
}

func (s *Service) resolveSnapshot(ctx context.Context, sessionID string) (SessionSnapshot, error) {
	if s == nil || s.snapshots == nil {
		return nil, errSessionStoreResolverRequired
	}
	return s.snapshots.ResolveSessionSnapshot(ctx, sessionID)
}

var _ servicecontract.SessionViewService = (*Service)(nil)
