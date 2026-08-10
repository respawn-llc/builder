package sessionview

import (
	"context"
	"errors"
	"strings"

	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/runtimeview"
	"core/server/session"
	"core/server/sessionruntime"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type sessionSnapshot interface {
	MainView(ctx context.Context) (clientui.RuntimeMainView, error)
	TranscriptPage(ctx context.Context, req clientui.TranscriptPageRequest) (clientui.TranscriptPage, error)
	TranscriptTailEntries(ctx context.Context) ([]runtime.ChatEntry, error)
}

type runtimeReadModelSnapshotProvider interface {
	RuntimeReadModelSnapshot(ctx context.Context, sessionID string) (runtimeactivity.ResponseSnapshot, error)
}

func resolveRuntimeActivitySnapshot(
	ctx context.Context,
	activity runtimeReadModelSnapshotProvider,
	sessionID string,
) (runtimeactivity.ResponseSnapshot, error) {
	if activity != nil {
		return activity.RuntimeReadModelSnapshot(ctx, sessionID)
	}
	return runtimeactivity.BuildSnapshot(sessionID, func() (runtimeactivity.ResolverSnapshot, error) {
		return runtimeactivity.ResolverSnapshot{}, nil
	})
}

func readWithContext[T any](ctx context.Context, read func() (T, error)) (T, error) {
	var zero T
	if err := context.Cause(ctx); err != nil {
		return zero, err
	}
	value, err := read()
	if err != nil {
		return zero, err
	}
	if err := context.Cause(ctx); err != nil {
		return zero, err
	}
	return value, nil
}

func resultWithContext[T any](ctx context.Context, value T) (T, error) {
	if err := context.Cause(ctx); err != nil {
		var zero T
		return zero, err
	}
	return value, nil
}

type resolvedSessionSnapshotSource struct {
	sessions         SessionStoreResolver
	activity         runtimeReadModelSnapshotProvider
	authority        *sessionruntime.Authority
	cacheWarningMode func() config.CacheWarningMode
}

func resolveSessionStoreAndEventLog(
	ctx context.Context,
	sessions SessionStoreResolver,
	sessionID string,
) (*session.Store, session.MaterializedEventLog, error) {
	if sessions == nil {
		return nil, session.MaterializedEventLog{}, errSessionStoreResolverRequired
	}
	store, err := sessions.ResolveSessionStore(ctx, sessionID)
	if err != nil {
		return nil, session.MaterializedEventLog{}, err
	}
	if store == nil {
		return nil, session.MaterializedEventLog{}, errSessionStoreResolverRequired
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		return nil, session.MaterializedEventLog{}, err
	}
	return store, eventLog, nil
}

func newResolvedSessionSnapshotSource(
	sessions SessionStoreResolver,
	activity runtimeReadModelSnapshotProvider,
	authority *sessionruntime.Authority,
	cacheWarningMode func() config.CacheWarningMode,
) *resolvedSessionSnapshotSource {
	return &resolvedSessionSnapshotSource{
		sessions:         sessions,
		activity:         activity,
		authority:        authority,
		cacheWarningMode: cacheWarningMode,
	}
}

func (s *resolvedSessionSnapshotSource) resolveSessionSnapshot(ctx context.Context, sessionID string) (sessionSnapshot, error) {
	if s == nil {
		return nil, errSessionStoreResolverRequired
	}
	readModelSnapshot, err := resolveRuntimeActivitySnapshot(ctx, s.activity, sessionID)
	if err != nil {
		return nil, err
	}
	if s.authority != nil {
		id, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
		if err != nil {
			return nil, err
		}
		err = s.authority.WithCurrentRuntime(ctx, id, func(context.Context, *runtime.Engine) error {
			return nil
		})
		if err == nil {
			return liveRuntimeSessionSnapshot{
				authority: s.authority,
				sessionID: id,
				snapshot:  readModelSnapshot,
			}, nil
		}
		if !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
			return nil, err
		}
	}
	if s.sessions == nil {
		return nil, errSessionStoreResolverRequired
	}
	store, eventLog, err := resolveSessionStoreAndEventLog(ctx, s.sessions, sessionID)
	if err != nil {
		return nil, err
	}
	return dormantSessionSnapshot{
		store:            store,
		eventLog:         eventLog,
		activity:         readModelSnapshot,
		cacheWarningMode: s.cacheWarningMode,
	}, nil
}

type liveRuntimeSessionSnapshot struct {
	authority *sessionruntime.Authority
	sessionID runtimeids.SessionID
	snapshot  runtimeactivity.ResponseSnapshot
}

func (s liveRuntimeSessionSnapshot) MainView(ctx context.Context) (clientui.RuntimeMainView, error) {
	return withLiveRuntime(ctx, s.authority, s.sessionID, func(engine *runtime.Engine) (clientui.RuntimeMainView, error) {
		view, err := runtimeview.MainViewFromRuntimeActivity(engine, s.snapshot.Version, s.snapshot.Activity)
		if err != nil {
			return clientui.RuntimeMainView{}, err
		}
		return view, nil
	})
}

func (s liveRuntimeSessionSnapshot) TranscriptPage(ctx context.Context, req clientui.TranscriptPageRequest) (clientui.TranscriptPage, error) {
	return withLiveRuntime(ctx, s.authority, s.sessionID, func(engine *runtime.Engine) (clientui.TranscriptPage, error) {
		return runtimeview.TranscriptPageFromRuntime(engine, req)
	})
}

func (s liveRuntimeSessionSnapshot) TranscriptTailEntries(ctx context.Context) ([]runtime.ChatEntry, error) {
	return withLiveRuntime(ctx, s.authority, s.sessionID, func(engine *runtime.Engine) ([]runtime.ChatEntry, error) {
		page, err := engine.TranscriptNewestSegmentPage()
		if err != nil {
			return nil, err
		}
		return append([]runtime.ChatEntry(nil), page.Snapshot.Entries...), nil
	})
}

func withLiveRuntime[T any](
	ctx context.Context,
	authority *sessionruntime.Authority,
	sessionID runtimeids.SessionID,
	read func(*runtime.Engine) (T, error),
) (T, error) {
	var value T
	err := authority.WithCurrentRuntime(ctx, sessionID, func(callbackCtx context.Context, engine *runtime.Engine) error {
		var err error
		value, err = readWithContext(callbackCtx, func() (T, error) {
			return read(engine)
		})
		return err
	})
	return value, err
}

type dormantSessionSnapshot struct {
	store            *session.Store
	eventLog         session.MaterializedEventLog
	activity         runtimeactivity.ResponseSnapshot
	cacheWarningMode func() config.CacheWarningMode
}

func (s dormantSessionSnapshot) MainView(ctx context.Context) (clientui.RuntimeMainView, error) {
	if s.store == nil {
		return clientui.RuntimeMainView{}, errors.New("session store is required")
	}
	segment, err := s.newestSegment(ctx)
	if err != nil {
		return clientui.RuntimeMainView{}, err
	}
	meta := s.store.Meta()
	sessionFreshness, err := s.eventLog.ConversationFreshness()
	if err != nil {
		return clientui.RuntimeMainView{}, err
	}
	freshness := runtimeview.ConversationFreshnessFromSession(sessionFreshness)
	goalAvailability, err := s.store.GoalAvailability(); if err != nil { return clientui.RuntimeMainView{}, err }
	status := clientui.RuntimeStatus{
		ConversationFreshness:             freshness,
		PreviousSessionID:                 textutil.Pointer(meta.PreviousSessionID),
		ParentAgentSessionID:              textutil.Pointer(meta.ParentAgentSessionID),
		NavigationTargetSessionID:         session.NavigationTargetSessionID(meta),
		LastCommittedAssistantFinalAnswer: segment.LastCommittedAssistantFinalAnswer,
		Goal:                              runtimeview.GoalFromSessionState(meta.Goal, goalAvailability, false),
	}
	view := runtimeview.RuntimeMainViewFromActivity(
		s.activity.Activity,
		status,
		clientui.RuntimeSessionView{
			SessionID:             meta.SessionID,
			SessionName:           meta.Name,
			AgentRole:             session.ContinuationAgentRole(meta),
			ConversationFreshness: freshness,
		},
	)
	view.Version = s.activity.Version
	return resultWithContext(ctx, view)
}

func (s dormantSessionSnapshot) TranscriptPage(ctx context.Context, req clientui.TranscriptPageRequest) (clientui.TranscriptPage, error) {
	if s.store == nil {
		return clientui.TranscriptPage{}, errors.New("session store is required")
	}
	if req.NewerCursor == nil && req.Cursor == nil {
		segment, err := s.newestSegment(ctx)
		if err != nil {
			return clientui.TranscriptPage{}, err
		}
		page, err := s.transcriptPage(segment)
		if err != nil {
			return clientui.TranscriptPage{}, err
		}
		return resultWithContext(ctx, page)
	}
	var (
		segment runtime.TranscriptSegmentPage
		err     error
	)
	if req.NewerCursor != nil {
		segment, err = readWithContext(ctx, func() (runtime.TranscriptSegmentPage, error) {
			return runtime.TranscriptSegmentPageForwardFromEventLog(s.eventLog, *req.NewerCursor, s.cacheWarningModeOrDefault())
		})
	} else {
		segment, err = readWithContext(ctx, func() (runtime.TranscriptSegmentPage, error) {
			return runtime.TranscriptSegmentPageFromEventLog(s.eventLog, *req.Cursor, s.cacheWarningModeOrDefault())
		})
	}
	if err != nil {
		return clientui.TranscriptPage{}, err
	}
	newest, err := s.newestSegment(ctx)
	if err != nil {
		return clientui.TranscriptPage{}, err
	}
	segment.LatestRollbackCandidate = newest.LatestRollbackCandidate
	page, err := s.transcriptPage(segment)
	if err != nil {
		return clientui.TranscriptPage{}, err
	}
	return resultWithContext(ctx, page)
}

func (s dormantSessionSnapshot) TranscriptTailEntries(ctx context.Context) ([]runtime.ChatEntry, error) {
	if s.store == nil {
		return nil, errors.New("session store is required")
	}
	segment, err := s.newestSegment(ctx)
	if err != nil {
		return nil, err
	}
	return resultWithContext(ctx, append([]runtime.ChatEntry(nil), segment.Snapshot.Entries...))
}

func (s dormantSessionSnapshot) newestSegment(ctx context.Context) (runtime.TranscriptSegmentPage, error) {
	return readWithContext(ctx, func() (runtime.TranscriptSegmentPage, error) {
		return runtime.TranscriptNewestSegmentPageFromEventLog(s.eventLog, s.cacheWarningModeOrDefault())
	})
}

func (s dormantSessionSnapshot) cacheWarningModeOrDefault() config.CacheWarningMode {
	if s.cacheWarningMode == nil {
		return config.CacheWarningModeDefault
	}
	return normalizeServiceCacheWarningMode(s.cacheWarningMode())
}

func (s dormantSessionSnapshot) transcriptPage(segment runtime.TranscriptSegmentPage) (clientui.TranscriptPage, error) {
	meta := s.store.Meta()
	freshness, err := s.eventLog.ConversationFreshness()
	if err != nil {
		return clientui.TranscriptPage{}, err
	}
	return runtimeview.TranscriptPageFromSegment(
		meta.SessionID,
		meta.Name,
		runtimeview.ConversationFreshnessFromSession(freshness),
		segment,
	)
}
