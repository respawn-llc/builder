package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

const uiRuntimeControlTimeout = 3 * time.Second
const uiRuntimeHydrationReadTimeout = 10 * time.Second

var errRuntimeTranscriptRefreshUnsupported = errors.New("runtime transcript refresh is not available in the tui-redesign emergency path")

var uiRuntimeReadTimeout = 300 * time.Millisecond

type sessionRuntimeClient struct {
	reads                   apicontract.SessionViewService
	controls                apicontract.RuntimeControlService
	sessionID               string
	connectionStateObserver func(error)

	mu               sync.RWMutex
	mainView         clientui.RuntimeMainView
	hasMainView      bool
	metadataRevision uint64
}

func newUIRuntimeClientWithReads(sessionID string, reads apicontract.SessionViewService, controls apicontract.RuntimeControlService) clientui.RuntimeClient {
	if reads == nil || controls == nil {
		return nil
	}
	return &sessionRuntimeClient{
		sessionID: sessionID,
		reads:     reads,
		controls:  controls,
		mainView:  clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: sessionID}},
	}
}

func runtimeControlCall[T any](call func(ctx context.Context, requestID string) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(context.Background(), uiRuntimeControlTimeout)
	defer cancel()
	return runtimeRequestCall(ctx, call)
}

func runtimeRequestCall[T any](ctx context.Context, call func(ctx context.Context, requestID string) (T, error)) (T, error) {
	requestID := uuid.NewString()
	return runtimeRequestCallWithID(ctx, requestID, call)
}

func runtimeControlCallNoResult(call func(ctx context.Context, requestID string) error) error {
	_, err := runtimeControlCall(func(ctx context.Context, requestID string) (struct{}, error) {
		return struct{}{}, call(ctx, requestID)
	})
	return err
}

func (c *sessionRuntimeClient) SetConnectionStateObserver(observer func(error)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connectionStateObserver = observer
}

func (c *sessionRuntimeClient) MainView() clientui.RuntimeMainView {
	view, _ := c.cachedMainView()
	if view.Session.SessionID == "" {
		view.Session.SessionID = c.sessionID
	}
	return view
}

func (c *sessionRuntimeClient) RefreshMainView() (clientui.RuntimeMainView, error) {
	return c.refreshMainViewSync(uiRuntimeHydrationReadTimeout)
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

func (c *sessionRuntimeClient) refreshMainViewSync(timeout time.Duration) (clientui.RuntimeMainView, error) {
	view, err := c.fetchMainViewSync(timeout)
	if err != nil {
		c.mu.Lock()
		if c.mainView.Session.SessionID == "" {
			c.mainView.Session.SessionID = c.sessionID
		}
		c.hasMainView = true
		view = c.mainView
		c.mu.Unlock()
		return view, err
	}
	return c.storeMainView(view), nil
}

func (c *sessionRuntimeClient) fetchMainView() (clientui.RuntimeMainView, error) {
	return c.fetchMainViewSync(uiRuntimeHydrationReadTimeout)
}

func (c *sessionRuntimeClient) fetchMainViewSync(timeout time.Duration) (clientui.RuntimeMainView, error) {
	ctx, cancel := c.readContext(timeout)
	defer cancel()
	resp, err := c.reads.GetSessionMainView(ctx, serverapi.SessionMainViewRequest{SessionID: c.sessionID})
	c.notifyConnectionState(err)
	if err != nil {
		view, _ := c.cachedMainView()
		if view.Session.SessionID == "" {
			view.Session.SessionID = c.sessionID
		}
		return view, err
	}
	if resp.MainView.Session.SessionID == "" {
		resp.MainView.Session.SessionID = c.sessionID
	}
	return resp.MainView, nil
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
