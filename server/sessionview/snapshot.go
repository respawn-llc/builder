package sessionview

import (
	"context"
	"errors"

	"core/server/runtime"
	"core/server/runtimeview"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/textutil"
)

type runtimeMainViewSnapshotProvider interface {
	RuntimeMainViewSnapshot(sessionID string) (clientui.RuntimeMainView, bool)
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

func (s *Service) resolveMainView(ctx context.Context, sessionID string) (clientui.RuntimeMainView, error) {
	if err := context.Cause(ctx); err != nil {
		return clientui.RuntimeMainView{}, err
	}
	if s.mainViews != nil {
		if view, ok := s.mainViews.RuntimeMainViewSnapshot(sessionID); ok {
			return resultWithContext(ctx, view)
		}
	}
	view, err := session.ResolvePersistedSessionView(ctx, s.persisted, sessionID)
	if err != nil {
		return clientui.RuntimeMainView{}, err
	}
	return (dormantSessionSnapshot{
		view:             view,
		cacheWarningMode: s.cacheWarningMode,
	}).MainView(ctx)
}

type dormantSessionSnapshot struct {
	view             *session.PersistedSessionView
	cacheWarningMode config.CacheWarningMode
}

func (s dormantSessionSnapshot) MainView(ctx context.Context) (clientui.RuntimeMainView, error) {
	if s.view == nil {
		return clientui.RuntimeMainView{}, errors.New("persisted Session view is required")
	}
	segment, err := s.newestSegment(ctx)
	if err != nil {
		return clientui.RuntimeMainView{}, err
	}
	meta := s.view.Meta()
	sessionFreshness := s.view.ConversationFreshness()
	version, err := clientui.NewReadModelVersion(
		"persisted-session-"+meta.SessionID,
		1,
		uint64(meta.LastSequence)+1,
	)
	if err != nil {
		return clientui.RuntimeMainView{}, err
	}
	freshness := runtimeview.ConversationFreshnessFromSession(sessionFreshness)
	goalAvailability, err := session.GoalAvailabilityFromMeta(meta)
	if err != nil {
		return clientui.RuntimeMainView{}, err
	}
	status := clientui.RuntimeStatus{
		ConversationFreshness:             freshness,
		PreviousSessionID:                 textutil.Pointer(meta.PreviousSessionID),
		ParentAgentSessionID:              textutil.Pointer(meta.ParentAgentSessionID),
		NavigationTargetSessionID:         session.NavigationTargetSessionID(meta),
		LastCommittedAssistantFinalAnswer: segment.LastCommittedAssistantFinalAnswer,
		Goal:                              runtimeview.GoalFromSessionState(meta.Goal, goalAvailability, false),
	}
	view := clientui.RuntimeMainView{
		Version: version,
		Status:  status,
		Session: clientui.RuntimeSessionView{
			SessionID:             meta.SessionID,
			SessionName:           meta.Name,
			AgentRole:             session.ContinuationAgentRole(meta),
			ConversationFreshness: freshness,
		},
		Activity: clientui.RuntimeActivity{
			State:    clientui.RuntimeActivityUnavailable,
			Reviewer: clientui.ReviewerActivityInactive,
		},
	}
	return resultWithContext(ctx, view)
}

func (s dormantSessionSnapshot) TranscriptPage(ctx context.Context, req clientui.TranscriptPageRequest) (clientui.TranscriptPage, error) {
	if s.view == nil {
		return clientui.TranscriptPage{}, errors.New("persisted Session view is required")
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
			return runtime.TranscriptSegmentPageForwardFromEventLog(s.view, *req.NewerCursor, s.cacheWarningModeOrDefault())
		})
	} else {
		segment, err = readWithContext(ctx, func() (runtime.TranscriptSegmentPage, error) {
			return runtime.TranscriptSegmentPageFromEventLog(s.view, *req.Cursor, s.cacheWarningModeOrDefault())
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
	if s.view == nil {
		return nil, errors.New("persisted Session view is required")
	}
	segment, err := s.newestSegment(ctx)
	if err != nil {
		return nil, err
	}
	return resultWithContext(ctx, append([]runtime.ChatEntry(nil), segment.Snapshot.Entries...))
}

func (s dormantSessionSnapshot) newestSegment(ctx context.Context) (runtime.TranscriptSegmentPage, error) {
	return readWithContext(ctx, func() (runtime.TranscriptSegmentPage, error) {
		return runtime.TranscriptNewestSegmentPageFromEventLog(s.view, s.cacheWarningModeOrDefault())
	})
}

func (s dormantSessionSnapshot) cacheWarningModeOrDefault() config.CacheWarningMode {
	return normalizeServiceCacheWarningMode(s.cacheWarningMode)
}

func (s dormantSessionSnapshot) transcriptPage(segment runtime.TranscriptSegmentPage) (clientui.TranscriptPage, error) {
	meta := s.view.Meta()
	return runtimeview.TranscriptPageFromSegment(
		meta.SessionID,
		meta.Name,
		runtimeview.ConversationFreshnessFromSession(s.view.ConversationFreshness()),
		segment,
	)
}
