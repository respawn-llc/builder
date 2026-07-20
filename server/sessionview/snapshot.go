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
	RuntimeReadModelSnapshot(ctx context.Context, sessionID string, refs []clientui.RuntimeOperationRef) (runtimeactivity.ResponseSnapshot, error)
}

func resolveRuntimeActivitySnapshot(
	ctx context.Context,
	activity runtimeReadModelSnapshotProvider,
	sessionID string,
	refs []clientui.RuntimeOperationRef,
) (runtimeactivity.ResponseSnapshot, error) {
	if activity != nil {
		return activity.RuntimeReadModelSnapshot(ctx, sessionID, refs)
	}
	return runtimeactivity.BuildSnapshot(sessionID, func() (runtimeactivity.SnapshotInput, error) {
		return runtimeactivity.SnapshotInput{
			Resolver:            runtimeactivity.ResolverSnapshot{},
			InputReconciliation: clientui.RuntimeInputReconciliationSnapshot{},
		}, nil
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

func (s *resolvedSessionSnapshotSource) resolveSessionSnapshot(ctx context.Context, sessionID string, refs []clientui.RuntimeOperationRef) (sessionSnapshot, error) {
	if s == nil {
		return nil, errSessionStoreResolverRequired
	}
	readModelSnapshot, err := resolveRuntimeActivitySnapshot(ctx, s.activity, sessionID, refs)
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
	store, err := s.sessions.ResolveSessionStore(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, errSessionStoreResolverRequired
	}
	return dormantSessionSnapshot{
		store:            store,
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
		view := runtimeview.MainViewFromRuntimeActivity(engine, s.snapshot.Version, s.snapshot.Activity)
		view.InputReconciliation = s.snapshot.InputReconciliation
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
	freshness := runtimeview.ConversationFreshnessFromSession(s.store.ConversationFreshness())
	status := clientui.RuntimeStatus{
		ConversationFreshness:             freshness,
		PreviousSessionID:                 textutil.Pointer(meta.PreviousSessionID),
		ParentAgentSessionID:              textutil.Pointer(meta.ParentAgentSessionID),
		NavigationTargetSessionID:         session.NavigationTargetSessionID(meta),
		LastCommittedAssistantFinalAnswer: segment.LastCommittedAssistantFinalAnswer,
		Goal:                              runtimeview.GoalFromSessionState(meta.Goal, false),
		WorkflowSession:                   workflowSessionStatus(meta.WorkflowSession),
	}
	view := runtimeview.RuntimeMainViewFromActivity(
		s.activity.Activity,
		status,
		clientui.RuntimeSessionView{
			SessionID:             meta.SessionID,
			SessionName:           meta.Name,
			ConversationFreshness: freshness,
		},
	)
	view.Version = s.activity.Version
	view.InputReconciliation = s.activity.InputReconciliation
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
		return resultWithContext(ctx, s.transcriptPage(segment))
	}
	var (
		segment runtime.TranscriptSegmentPage
		err     error
	)
	if req.NewerCursor != nil {
		segment, err = readWithContext(ctx, func() (runtime.TranscriptSegmentPage, error) {
			return runtime.TranscriptSegmentPageForwardFromStore(s.store, *req.NewerCursor, s.cacheWarningModeOrDefault())
		})
	} else {
		segment, err = readWithContext(ctx, func() (runtime.TranscriptSegmentPage, error) {
			return runtime.TranscriptSegmentPageFromStore(s.store, *req.Cursor, s.cacheWarningModeOrDefault())
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
	return resultWithContext(ctx, s.transcriptPage(segment))
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
		return runtime.TranscriptNewestSegmentPageFromStore(s.store, s.cacheWarningModeOrDefault())
	})
}

func (s dormantSessionSnapshot) cacheWarningModeOrDefault() config.CacheWarningMode {
	if s.cacheWarningMode == nil {
		return config.CacheWarningModeDefault
	}
	return normalizeServiceCacheWarningMode(s.cacheWarningMode())
}

func (s dormantSessionSnapshot) transcriptPage(segment runtime.TranscriptSegmentPage) clientui.TranscriptPage {
	meta := s.store.Meta()
	return runtimeview.TranscriptPageFromSegment(
		meta.SessionID,
		meta.Name,
		runtimeview.ConversationFreshnessFromSession(s.store.ConversationFreshness()),
		segment,
	)
}

func workflowSessionStatus(state *session.WorkflowSessionState) *clientui.WorkflowSessionStatus {
	if state == nil {
		return nil
	}
	return &clientui.WorkflowSessionStatus{
		RunID:      strings.TrimSpace(state.RunID),
		TaskID:     strings.TrimSpace(state.TaskID),
		WorkflowID: strings.TrimSpace(state.WorkflowID),
	}
}
