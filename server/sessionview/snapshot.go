package sessionview

import (
	"context"
	"errors"
	"strings"

	"core/server/runtime"
	"core/server/runtimeview"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/config"
)

type SessionSnapshotSource interface {
	ResolveSessionSnapshot(ctx context.Context, sessionID string) (SessionSnapshot, error)
}

type SessionSnapshot interface {
	MainView(ctx context.Context) (clientui.RuntimeMainView, error)
	TranscriptPage(ctx context.Context, req clientui.TranscriptPageRequest) (clientui.TranscriptPage, error)
	CommittedTranscriptSuffix(ctx context.Context, req clientui.CommittedTranscriptSuffixRequest) (clientui.CommittedTranscriptSuffix, error)
}

type enrichedSessionSnapshotSource struct {
	base     SessionSnapshotSource
	targets  ExecutionTargetResolver
	updates  func() UpdateStatusProvider
	clearers []interface{ ClearCaches() }
}

func newEnrichedSessionSnapshotSource(base SessionSnapshotSource, targets ExecutionTargetResolver, updates func() UpdateStatusProvider) SessionSnapshotSource {
	source := &enrichedSessionSnapshotSource{base: base, targets: targets, updates: updates}
	if clearer, ok := base.(interface{ ClearCaches() }); ok {
		source.clearers = append(source.clearers, clearer)
	}
	return source
}

func (s *enrichedSessionSnapshotSource) ResolveSessionSnapshot(ctx context.Context, sessionID string) (SessionSnapshot, error) {
	if s == nil || s.base == nil {
		return nil, errSessionStoreResolverRequired
	}
	snapshot, err := s.base.ResolveSessionSnapshot(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return enrichedSessionSnapshot{base: snapshot, targets: s.targets, updates: s.updates}, nil
}

func (s *enrichedSessionSnapshotSource) ClearCaches() {
	if s == nil {
		return
	}
	for _, clearer := range s.clearers {
		clearer.ClearCaches()
	}
}

type enrichedSessionSnapshot struct {
	base    SessionSnapshot
	targets ExecutionTargetResolver
	updates func() UpdateStatusProvider
}

func (s enrichedSessionSnapshot) MainView(ctx context.Context) (clientui.RuntimeMainView, error) {
	view, err := s.base.MainView(ctx)
	if err != nil {
		return clientui.RuntimeMainView{}, err
	}
	if s.targets != nil && strings.TrimSpace(view.Session.SessionID) != "" {
		target, err := s.targets.ResolveSessionExecutionTarget(ctx, view.Session.SessionID)
		if err != nil {
			return clientui.RuntimeMainView{}, err
		}
		view.Session.ExecutionTarget = target
	}
	if s.updates != nil {
		if provider := s.updates(); provider != nil {
			view.Status.Update = provider.Status(ctx)
		}
	}
	return view, nil
}

func (s enrichedSessionSnapshot) TranscriptPage(ctx context.Context, req clientui.TranscriptPageRequest) (clientui.TranscriptPage, error) {
	return s.base.TranscriptPage(ctx, req)
}

func (s enrichedSessionSnapshot) CommittedTranscriptSuffix(ctx context.Context, req clientui.CommittedTranscriptSuffixRequest) (clientui.CommittedTranscriptSuffix, error) {
	return s.base.CommittedTranscriptSuffix(ctx, req)
}

type resolvedSessionSnapshotSource struct {
	sessions SessionStoreResolver
	runtimes RuntimeResolver
	dormant  *dormantSessionSnapshotSource
}

func newResolvedSessionSnapshotSource(sessions SessionStoreResolver, runtimes RuntimeResolver, cacheWarningMode func() config.CacheWarningMode) *resolvedSessionSnapshotSource {
	return &resolvedSessionSnapshotSource{
		sessions: sessions,
		runtimes: runtimes,
		dormant:  newDormantSessionSnapshotSource(cacheWarningMode),
	}
}

func (s *resolvedSessionSnapshotSource) ResolveSessionSnapshot(ctx context.Context, sessionID string) (SessionSnapshot, error) {
	if s == nil {
		return nil, errSessionStoreResolverRequired
	}
	if s.runtimes != nil {
		engine, err := s.runtimes.ResolveRuntime(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		if engine != nil {
			var external clientui.ExternalRuntimeStatus
			if resolver, ok := s.runtimes.(ExternalRuntimeStatusResolver); ok {
				external = resolver.ExternalRuntimeStatus(sessionID)
			}
			return liveRuntimeSessionSnapshot{engine: engine, sessions: s.sessions, external: external}, nil
		}
	}
	if s.sessions == nil {
		return nil, errSessionStoreResolverRequired
	}
	store, err := s.sessions.ResolveSessionStore(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, errSessionStoreResolverRequired
	}
	return s.dormant.snapshot(store), nil
}

func (s *resolvedSessionSnapshotSource) ClearCaches() {
	if s != nil && s.dormant != nil {
		s.dormant.clear()
	}
}

type liveRuntimeSessionSnapshot struct {
	engine   *runtime.Engine
	sessions SessionStoreResolver
	external clientui.ExternalRuntimeStatus
}

func (s liveRuntimeSessionSnapshot) MainView(ctx context.Context) (clientui.RuntimeMainView, error) {
	view := runtimeview.MainViewFromRuntime(s.engine)
	if s.external.State != "" {
		view.ExternalRuntime = &clientui.ExternalRuntimeStatus{
			State:          s.external.State,
			QueueAccepting: s.external.QueueAccepting,
		}
	}
	if s.sessions != nil && view.Status.WorkflowSession == nil {
		store, err := s.sessions.ResolveSessionStore(ctx, s.engine.SessionID())
		if err == nil && store != nil {
			if workflowSession := store.Meta().WorkflowSession; workflowSession != nil {
				view.Status.WorkflowSession = &clientui.WorkflowSessionStatus{
					RunID:      strings.TrimSpace(workflowSession.RunID),
					TaskID:     strings.TrimSpace(workflowSession.TaskID),
					WorkflowID: strings.TrimSpace(workflowSession.WorkflowID),
				}
			}
		}
	}
	return view, nil
}

func (s liveRuntimeSessionSnapshot) TranscriptPage(_ context.Context, req clientui.TranscriptPageRequest) (clientui.TranscriptPage, error) {
	return runtimeview.TranscriptPageFromRuntime(s.engine, req)
}

func (s liveRuntimeSessionSnapshot) CommittedTranscriptSuffix(_ context.Context, req clientui.CommittedTranscriptSuffixRequest) (clientui.CommittedTranscriptSuffix, error) {
	return runtimeview.CommittedTranscriptSuffixFromRuntime(s.engine, req)
}

type dormantSessionSnapshotSource struct {
	cacheWarningMode func() config.CacheWarningMode
	dormant          *dormantTranscriptCache
}

func newDormantSessionSnapshotSource(cacheWarningMode func() config.CacheWarningMode) *dormantSessionSnapshotSource {
	source := &dormantSessionSnapshotSource{cacheWarningMode: cacheWarningMode}
	source.dormant = newDormantTranscriptCacheWithLimit(dormantTranscriptCacheMaxEntries, func(ctx context.Context, store *session.Store) (dormantTranscriptCacheEntry, error) {
		return source.buildCacheEntry(ctx, store)
	})
	return source
}

func (s *dormantSessionSnapshotSource) snapshot(store *session.Store) dormantSessionSnapshot {
	return dormantSessionSnapshot{source: s, store: store}
}

func (s *dormantSessionSnapshotSource) clear() {
	if s == nil {
		return
	}
	if s.dormant != nil {
		s.dormant.clear()
	}
}

func (s *dormantSessionSnapshotSource) buildCacheEntry(ctx context.Context, store *session.Store) (dormantTranscriptCacheEntry, error) {
	return buildDormantTranscriptCacheEntryWithMode(ctx, store, s.cacheWarningModeOrDefault())
}

func (s *dormantSessionSnapshotSource) cacheWarningModeOrDefault() config.CacheWarningMode {
	if s != nil && s.cacheWarningMode != nil {
		return normalizeServiceCacheWarningMode(s.cacheWarningMode())
	}
	return config.CacheWarningModeDefault
}

type dormantSessionSnapshot struct {
	source *dormantSessionSnapshotSource
	store  *session.Store
}

func (s dormantSessionSnapshot) MainView(ctx context.Context) (clientui.RuntimeMainView, error) {
	if s.store == nil {
		return clientui.RuntimeMainView{}, errors.New("session store is required")
	}
	entry, err := s.source.dormant.get(ctx, s.store)
	if err != nil {
		return clientui.RuntimeMainView{}, err
	}
	meta := s.store.Meta()
	freshness := runtimeview.ConversationFreshnessFromSession(s.store.ConversationFreshness())
	return entry.mainView(meta, freshness), nil
}

func (s dormantSessionSnapshot) TranscriptPage(ctx context.Context, req clientui.TranscriptPageRequest) (clientui.TranscriptPage, error) {
	if s.store == nil {
		return clientui.TranscriptPage{}, errors.New("session store is required")
	}
	meta := s.store.Meta()
	freshness := runtimeview.ConversationFreshnessFromSession(s.store.ConversationFreshness())
	cacheWarningMode := s.source.cacheWarningModeOrDefault()
	if req.NewerCursor > 0 {
		segment, err := runtime.TranscriptSegmentPageForwardFromStore(s.store, req.NewerCursor, cacheWarningMode)
		if err != nil {
			return clientui.TranscriptPage{}, err
		}
		return runtimeview.TranscriptPageFromSegment(meta.SessionID, meta.Name, freshness, meta.LastSequence, segment), nil
	}
	if req.Cursor <= 0 {
		entry, err := s.source.dormant.get(ctx, s.store)
		if err != nil {
			return clientui.TranscriptPage{}, err
		}
		return entry.newestSegmentPage(meta, freshness), nil
	}
	segment, err := runtime.TranscriptSegmentPageFromStore(s.store, req.Cursor, cacheWarningMode)
	if err != nil {
		return clientui.TranscriptPage{}, err
	}
	return runtimeview.TranscriptPageFromSegment(meta.SessionID, meta.Name, freshness, meta.LastSequence, segment), nil
}

func (s dormantSessionSnapshot) CommittedTranscriptSuffix(ctx context.Context, req clientui.CommittedTranscriptSuffixRequest) (clientui.CommittedTranscriptSuffix, error) {
	if s.store == nil {
		return clientui.CommittedTranscriptSuffix{}, errors.New("session store is required")
	}
	meta := s.store.Meta()
	freshness := runtimeview.ConversationFreshnessFromSession(s.store.ConversationFreshness())
	entry, err := s.source.dormant.get(ctx, s.store)
	if err != nil {
		return clientui.CommittedTranscriptSuffix{}, err
	}
	return runtimeview.CommittedTranscriptSuffixFromSegment(meta.SessionID, meta.Name, freshness, meta.LastSequence, entry.newestSegment), nil
}
