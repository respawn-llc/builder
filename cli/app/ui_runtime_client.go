package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"core/shared/client"
	"core/shared/clientui"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

const uiRuntimeControlTimeout = 3 * time.Second
const uiRuntimeHydrationReadTimeout = 10 * time.Second
const runtimeReconnectWarningText = "Lost connection to the session runtime; reconnected."

var errRuntimeTranscriptRefreshUnsupported = errors.New("runtime transcript refresh is not available in the tui-redesign emergency path")

var uiRuntimeReadTimeout = 300 * time.Millisecond

type sessionRuntimeClient struct {
	reads                    client.SessionViewClient
	controls                 client.RuntimeControlClient
	sessionID                string
	reactivator              *runtimeReactivator
	diagLogf                 func(string)
	transcriptDiagnostics    bool
	connectionStateObserver  func(error)
	reconnectWarningObserver func(string, clientui.EntryVisibility)

	mu             sync.RWMutex
	mainView       clientui.RuntimeMainView
	hasMainView    bool
	readModelStale bool
}

func newUIRuntimeClientWithReads(sessionID string, reads client.SessionViewClient, controls client.RuntimeControlClient) clientui.RuntimeClient {
	if reads == nil || controls == nil {
		return nil
	}
	return &sessionRuntimeClient{
		sessionID:   sessionID,
		reactivator: newRuntimeReactivator(),
		reads:       reads,
		controls:    controls,
		mainView:    clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: sessionID}},
	}
}

func (c *sessionRuntimeClient) SetRuntimeReactivator(reactivator *runtimeReactivator) {
	if c == nil || reactivator == nil {
		return
	}
	c.mu.Lock()
	c.reactivator = reactivator
	c.mu.Unlock()
}

func (c *sessionRuntimeClient) runtimeReactivator() *runtimeReactivator {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.reactivator
}

func (c *sessionRuntimeClient) recoverRuntimeConnectionWithWarning(ctx context.Context, trigger error, appendWarning bool) error {
	return c.recoverRuntimeConnection(ctx, trigger, appendWarning, true)
}

func (c *sessionRuntimeClient) recoverRuntimeConnectionPreservingContext(ctx context.Context, trigger error, appendWarning bool) error {
	return c.recoverRuntimeConnection(ctx, trigger, appendWarning, false)
}

func (c *sessionRuntimeClient) recoverRuntimeConnection(ctx context.Context, trigger error, appendWarning bool, detach bool) error {
	reactivator := c.runtimeReactivator()
	if reactivator == nil {
		return errRuntimeReactivationUnavailable
	}
	reconnectBase := ctx
	if detach {
		reconnectBase = context.WithoutCancel(ctx)
	}
	reconnectCtx, cancel := context.WithTimeout(reconnectBase, uiRuntimeControlTimeout)
	defer cancel()
	if err := reactivator.Reactivate(reconnectCtx); err != nil {
		return err
	}
	if appendWarning && isRecoverableRuntimeControlError(trigger) {
		c.appendRuntimeReconnectWarning()
	}
	return nil
}

func (c *sessionRuntimeClient) appendRuntimeReconnectWarning() {
	if c == nil || c.controls == nil {
		return
	}
	warningCtx, cancel := context.WithTimeout(context.Background(), uiRuntimeControlTimeout)
	defer cancel()
	if err := c.controls.AppendCommittedEntry(warningCtx, serverapi.RuntimeAppendCommittedEntryRequest{
		ClientRequestID: uuid.NewString(),
		SessionID:       c.sessionID,
		Role:            "warning",
		Text:            runtimeReconnectWarningText,
		Visibility:      string(clientui.EntryVisibilityAll),
	}); err != nil {
		c.notifyRuntimeReconnectWarning(runtimeReconnectWarningText, clientui.EntryVisibilityAll)
	}
}

func isRecoverableRuntimeControlError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, serverapi.ErrRuntimeUnavailable)
}

func runtimeControlCall[T any](c *sessionRuntimeClient, appendWarning bool, call func(ctx context.Context, requestID string) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(context.Background(), uiRuntimeControlTimeout)
	defer cancel()
	return runtimeRequestCall(ctx, c, appendWarning, call)
}

func runtimeRequestCall[T any](ctx context.Context, c *sessionRuntimeClient, appendWarning bool, call func(ctx context.Context, requestID string) (T, error)) (T, error) {
	requestID := uuid.NewString()
	return runtimeRequestCallWithID(ctx, c, appendWarning, requestID, call)
}

func runtimeControlCallNoResult(c *sessionRuntimeClient, call func(ctx context.Context, requestID string) error) error {
	_, err := runtimeControlCall(c, true, func(ctx context.Context, requestID string) (struct{}, error) {
		return struct{}{}, call(ctx, requestID)
	})
	return err
}

func runtimeRequestCallNoResult(ctx context.Context, c *sessionRuntimeClient, call func(ctx context.Context, requestID string) error) error {
	_, err := runtimeRequestCall(ctx, c, true, func(ctx context.Context, requestID string) (struct{}, error) {
		return struct{}{}, call(ctx, requestID)
	})
	return err
}

func retryRuntimeUnavailableCall[T any](ctx context.Context, recoverRuntimeConnection func(context.Context, error, bool) error, appendRecoveryWarning bool, call func() (T, error)) (T, error) {
	value, err := call()
	if !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		return value, err
	}
	var zero T
	if recoverErr := recoverRuntimeConnection(ctx, err, appendRecoveryWarning); recoverErr != nil {
		return zero, recoverErr
	}
	return call()
}

func (c *sessionRuntimeClient) SetTranscriptDiagnosticLogger(logf func(string)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.diagLogf = logf
}

func (c *sessionRuntimeClient) SetTranscriptDiagnosticsEnabled(enabled bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.transcriptDiagnostics = enabled
	if enabled {
		return
	}
}

func (c *sessionRuntimeClient) SetConnectionStateObserver(observer func(error)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connectionStateObserver = observer
}

func (c *sessionRuntimeClient) SetRuntimeReconnectWarningObserver(observer func(string, clientui.EntryVisibility)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reconnectWarningObserver = observer
}

func (c *sessionRuntimeClient) MainView() clientui.RuntimeMainView {
	view, hasView := c.cachedMainView()
	if !hasView {
		refreshed, err := c.refreshMainViewSync(uiRuntimeReadTimeout, nil)
		if err == nil {
			return refreshed
		}
		return view
	}
	return view
}

func (c *sessionRuntimeClient) RefreshMainView() (clientui.RuntimeMainView, error) {
	return c.RefreshMainViewWithPendingRefs(nil)
}

func (c *sessionRuntimeClient) RefreshMainViewWithPendingRefs(refs []clientui.RuntimeOperationRef) (clientui.RuntimeMainView, error) {
	return c.refreshMainViewSync(uiRuntimeHydrationReadTimeout, refs)
}

func (c *sessionRuntimeClient) transcriptDiagnosticsEnabled() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.transcriptDiagnostics
}

func (c *sessionRuntimeClient) Status() clientui.RuntimeStatus {
	return c.MainView().Status
}

func (c *sessionRuntimeClient) SessionView() clientui.RuntimeSessionView {
	return c.MainView().Session
}

func (c *sessionRuntimeClient) readContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = uiRuntimeReadTimeout
	}
	return context.WithTimeout(context.Background(), timeout)
}

func (c *sessionRuntimeClient) observeRuntimeEventStatus(evt clientui.Event) {
	if c == nil {
		return
	}
	if evt.Kind == clientui.EventRuntimeActivityChanged && evt.RuntimeActivity != nil && evt.InputReconciliation != nil {
		c.patchVersionedRuntimeActivity(runtimeActivitySnapshotPatch{
			Version:             evt.ReadModelVersion,
			Activity:            *evt.RuntimeActivity,
			InputReconciliation: *evt.InputReconciliation,
		})
	}
	if evt.ContextUsage == nil && evt.GoalStatus == nil {
		return
	}
	c.patchMainView(func(view *clientui.RuntimeMainView) {
		if evt.ContextUsage != nil {
			view.Status.ContextUsage = *evt.ContextUsage
		}
		if evt.Kind == clientui.EventGoalStatusUpdated && evt.GoalStatus != nil {
			view.Status.Goal = runtimeGoalFromStatusUpdate(view.Status.Goal, *evt.GoalStatus)
		}
	})
}

func runtimeGoalFromStatusUpdate(existing *clientui.RuntimeGoal, update clientui.RuntimeGoalStatusUpdate) *clientui.RuntimeGoal {
	if update.Cleared {
		return nil
	}
	goal := &clientui.RuntimeGoal{
		ID:        strings.TrimSpace(update.ID),
		Objective: update.Objective,
		Status:    update.Status,
	}
	if existing != nil &&
		strings.TrimSpace(existing.ID) == goal.ID &&
		existing.Status == clientui.RuntimeGoalStatusActive &&
		goal.Status == clientui.RuntimeGoalStatusActive {
		goal.Suspended = existing.Suspended
	}
	return goal
}

func (c *sessionRuntimeClient) refreshMainViewSync(timeout time.Duration, refs []clientui.RuntimeOperationRef) (clientui.RuntimeMainView, error) {
	ctx, cancel := c.readContext(timeout)
	defer cancel()
	resp, err := retryRuntimeUnavailableCall(ctx, c.recoverRuntimeConnectionPreservingContext, false, func() (serverapi.SessionMainViewResponse, error) {
		return c.reads.GetSessionMainView(ctx, serverapi.SessionMainViewRequest{SessionID: c.sessionID, PendingOperationRefs: refs})
	})
	c.notifyConnectionState(err)
	if err != nil {
		c.mu.Lock()
		view := c.mainView
		if view.Session.SessionID == "" {
			view.Session.SessionID = c.sessionID
		}
		c.mainView = view
		c.hasMainView = true
		c.mu.Unlock()
		return view, err
	}
	return c.storeMainView(resp.MainView), nil
}

func (c *sessionRuntimeClient) notifyConnectionState(err error) {
	if c == nil {
		return
	}
	c.mu.RLock()
	observer := c.connectionStateObserver
	c.mu.RUnlock()
	if observer == nil {
		return
	}
	observer(err)
}

func (c *sessionRuntimeClient) notifyRuntimeReconnectWarning(text string, visibility clientui.EntryVisibility) {
	if c == nil || strings.TrimSpace(text) == "" {
		return
	}
	c.mu.RLock()
	observer := c.reconnectWarningObserver
	c.mu.RUnlock()
	if observer == nil {
		return
	}
	observer(text, visibility)
}

func (c *sessionRuntimeClient) logTranscriptDiag(line string) {
	if c == nil {
		return
	}
	c.mu.RLock()
	logf := c.diagLogf
	c.mu.RUnlock()
	if logf == nil {
		return
	}
	logf(strings.TrimSpace(line))
}

func cloneTranscriptEntries(entries []clientui.ChatEntry) []clientui.ChatEntry {
	if len(entries) == 0 {
		return nil
	}
	cloned := make([]clientui.ChatEntry, 0, len(entries))
	for _, entry := range entries {
		copyEntry := entry
		if entry.ToolCall != nil {
			copyMeta := *entry.ToolCall
			if len(entry.ToolCall.Suggestions) > 0 {
				copyMeta.Suggestions = append([]string(nil), entry.ToolCall.Suggestions...)
			}
			if entry.ToolCall.RenderHint != nil {
				renderHint := *entry.ToolCall.RenderHint
				copyMeta.RenderHint = &renderHint
			}
			copyEntry.ToolCall = &copyMeta
		}
		cloned = append(cloned, copyEntry)
	}
	return cloned
}
