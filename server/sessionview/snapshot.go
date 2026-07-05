package sessionview

import (
	"context"
	"errors"
	"strings"

	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/runtimeview"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/config"
)

type SessionSnapshotSource interface {
	ResolveSessionSnapshot(ctx context.Context, sessionID string, refs []clientui.RuntimeOperationRef) (SessionSnapshot, error)
}

type SessionSnapshot interface {
	MainView(ctx context.Context) (clientui.RuntimeMainView, error)
	TranscriptPage(ctx context.Context, req clientui.TranscriptPageRequest) (clientui.TranscriptPage, error)
	CommittedTranscriptSuffix(ctx context.Context, req clientui.CommittedTranscriptSuffixRequest) (clientui.CommittedTranscriptSuffix, error)
	TranscriptTailEntries(ctx context.Context) ([]runtime.ChatEntry, error)
}

type runtimeReadModelSnapshotProvider interface {
	RuntimeReadModelSnapshot(ctx context.Context, sessionID string, refs []clientui.RuntimeOperationRef) (runtimeactivity.ResponseSnapshot, error)
}

type sessionRuntimeActivityResolver interface {
	Snapshot(ctx context.Context, sessionID string, refs []clientui.RuntimeOperationRef) (runtimeactivity.ResponseSnapshot, error)
}

type defaultSessionRuntimeActivityResolver struct {
	runtimes RuntimeResolver
}

func newDefaultSessionRuntimeActivityResolver(runtimes RuntimeResolver) sessionRuntimeActivityResolver {
	return defaultSessionRuntimeActivityResolver{runtimes: runtimes}
}

func (r defaultSessionRuntimeActivityResolver) Snapshot(ctx context.Context, sessionID string, refs []clientui.RuntimeOperationRef) (runtimeactivity.ResponseSnapshot, error) {
	if provider, ok := r.runtimes.(runtimeReadModelSnapshotProvider); ok {
		return provider.RuntimeReadModelSnapshot(ctx, sessionID, refs)
	}
	var engine *runtime.Engine
	var err error
	if r.runtimes != nil {
		engine, err = r.runtimes.ResolveRuntime(ctx, sessionID)
	}
	if err != nil {
		return runtimeactivity.ResponseSnapshot{}, err
	}
	registry := runtimeactivity.RegistrySnapshot{Registered: engine != nil, QueueAccepting: true}
	return runtimeactivity.BuildSnapshot(sessionID, func(version clientui.ReadModelVersion) (runtimeactivity.SnapshotInput, error) {
		return runtimeactivity.SnapshotInput{
			Resolver:            runtimeactivity.ResolverSnapshot{Registry: registry, Active: runtimeactivity.ActiveStepFromProvider(engine)},
			InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(version),
		}, nil
	})
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

func (s *enrichedSessionSnapshotSource) ResolveSessionSnapshot(ctx context.Context, sessionID string, refs []clientui.RuntimeOperationRef) (SessionSnapshot, error) {
	if s == nil || s.base == nil {
		return nil, errSessionStoreResolverRequired
	}
	snapshot, err := s.base.ResolveSessionSnapshot(ctx, sessionID, refs)
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

func (s enrichedSessionSnapshot) TranscriptTailEntries(ctx context.Context) ([]runtime.ChatEntry, error) {
	return s.base.TranscriptTailEntries(ctx)
}

type resolvedSessionSnapshotSource struct {
	sessions SessionStoreResolver
	runtimes RuntimeResolver
	activity sessionRuntimeActivityResolver
	dormant  *dormantSessionSnapshotSource
}

func newResolvedSessionSnapshotSource(sessions SessionStoreResolver, runtimes RuntimeResolver, cacheWarningMode func() config.CacheWarningMode) *resolvedSessionSnapshotSource {
	return &resolvedSessionSnapshotSource{
		sessions: sessions,
		runtimes: runtimes,
		activity: newDefaultSessionRuntimeActivityResolver(runtimes),
		dormant:  newDormantSessionSnapshotSource(cacheWarningMode),
	}
}

func (s *resolvedSessionSnapshotSource) ResolveSessionSnapshot(ctx context.Context, sessionID string, refs []clientui.RuntimeOperationRef) (SessionSnapshot, error) {
	if s == nil {
		return nil, errSessionStoreResolverRequired
	}
	if s.runtimes != nil {
		engine, err := s.runtimes.ResolveRuntime(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		if engine != nil {
			snapshot, err := s.activityResolver().Snapshot(ctx, sessionID, refs)
			if err != nil {
				return nil, err
			}
			return liveRuntimeSessionSnapshot{engine: engine, sessions: s.sessions, snapshot: snapshot}, nil
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
	snapshot := s.dormant.snapshot(store)
	readModelSnapshot, err := s.activityResolver().Snapshot(ctx, sessionID, refs)
	if err != nil {
		return nil, err
	}
	return activityOverrideSnapshot{base: snapshot, snapshot: readModelSnapshot}, nil
}

func (s *resolvedSessionSnapshotSource) activityResolver() sessionRuntimeActivityResolver {
	if s != nil && s.activity != nil {
		return s.activity
	}
	if s == nil {
		return newDefaultSessionRuntimeActivityResolver(nil)
	}
	return newDefaultSessionRuntimeActivityResolver(s.runtimes)
}

func (s *resolvedSessionSnapshotSource) ClearCaches() {
	if s != nil && s.dormant != nil {
		s.dormant.clear()
	}
}

type liveRuntimeSessionSnapshot struct {
	engine   *runtime.Engine
	sessions SessionStoreResolver
	snapshot runtimeactivity.ResponseSnapshot
}

func (s liveRuntimeSessionSnapshot) MainView(ctx context.Context) (clientui.RuntimeMainView, error) {
	view := runtimeview.MainViewFromRuntimeActivity(s.engine, s.snapshot.Version, s.snapshot.Activity)
	view.InputReconciliation = s.snapshot.InputReconciliation
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

func (s liveRuntimeSessionSnapshot) TranscriptTailEntries(_ context.Context) ([]runtime.ChatEntry, error) {
	page, err := s.engine.TranscriptNewestSegmentPage()
	if err != nil {
		return nil, err
	}
	return append([]runtime.ChatEntry(nil), page.Snapshot.Entries...), nil
}

type activityOverrideSnapshot struct {
	base     SessionSnapshot
	snapshot runtimeactivity.ResponseSnapshot
}

func (s activityOverrideSnapshot) MainView(ctx context.Context) (clientui.RuntimeMainView, error) {
	view, err := s.base.MainView(ctx)
	if err != nil {
		return clientui.RuntimeMainView{}, err
	}
	view.Version = s.snapshot.Version
	view.Activity = s.snapshot.Activity
	view.InputReconciliation = s.snapshot.InputReconciliation
	return view, nil
}

func (s activityOverrideSnapshot) TranscriptPage(ctx context.Context, req clientui.TranscriptPageRequest) (clientui.TranscriptPage, error) {
	return s.base.TranscriptPage(ctx, req)
}

func (s activityOverrideSnapshot) CommittedTranscriptSuffix(ctx context.Context, req clientui.CommittedTranscriptSuffixRequest) (clientui.CommittedTranscriptSuffix, error) {
	return s.base.CommittedTranscriptSuffix(ctx, req)
}

func (s activityOverrideSnapshot) TranscriptTailEntries(ctx context.Context) ([]runtime.ChatEntry, error) {
	return s.base.TranscriptTailEntries(ctx)
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
	if req.NewerCursor != nil {
		segment, err := runtime.TranscriptSegmentPageForwardFromStore(s.store, *req.NewerCursor, cacheWarningMode)
		if err != nil {
			return clientui.TranscriptPage{}, err
		}
		return runtimeview.TranscriptPageFromSegment(meta.SessionID, meta.Name, freshness, meta.LastSequence, segment), nil
	}
	if req.Cursor == nil {
		entry, err := s.source.dormant.get(ctx, s.store)
		if err != nil {
			return clientui.TranscriptPage{}, err
		}
		return entry.newestSegmentPage(meta, freshness), nil
	}
	segment, err := runtime.TranscriptSegmentPageFromStore(s.store, *req.Cursor, cacheWarningMode)
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

func (s dormantSessionSnapshot) TranscriptTailEntries(ctx context.Context) ([]runtime.ChatEntry, error) {
	if s.store == nil {
		return nil, errors.New("session store is required")
	}
	entry, err := s.source.dormant.get(ctx, s.store)
	if err != nil {
		return nil, err
	}
	return entry.newestSegmentTailEntries(), nil
}
