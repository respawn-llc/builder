package app

import "core/shared/clientui"

func (c *sessionRuntimeClient) cachedMainView() (clientui.RuntimeMainView, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	view := c.mainView
	if !c.hasMainView {
		return view, false
	}
	return view, true
}

func (c *sessionRuntimeClient) CachedMainView() (clientui.RuntimeMainView, bool) {
	if c == nil {
		return clientui.RuntimeMainView{}, false
	}
	return c.cachedMainView()
}

func (c *sessionRuntimeClient) storeMainView(view clientui.RuntimeMainView) clientui.RuntimeMainView {
	if view.Session.SessionID == "" {
		view.Session.SessionID = c.sessionID
	}
	c.mu.Lock()
	if !shouldStoreRuntimeMainView(c.mainView.Version, view.Version) {
		current := c.mainView
		c.mu.Unlock()
		return current
	}
	c.mainView = view
	c.hasMainView = true
	c.readModelStale = false
	c.mu.Unlock()
	return view
}

func shouldStoreRuntimeMainView(current clientui.ReadModelVersion, incoming clientui.ReadModelVersion) bool {
	if incoming.Validate() != nil {
		return current.Validate() != nil
	}
	if current.Validate() != nil {
		return true
	}
	if incoming.Epoch != current.Epoch {
		return true
	}
	if incoming.Generation != current.Generation {
		return incoming.Generation > current.Generation
	}
	return incoming.Sequence > current.Sequence
}

func (c *sessionRuntimeClient) patchMainView(apply func(view *clientui.RuntimeMainView)) {
	c.mu.Lock()
	apply(&c.mainView)
	if c.mainView.Session.SessionID == "" {
		c.mainView.Session.SessionID = c.sessionID
	}
	c.hasMainView = true
	c.mu.Unlock()
}

func (c *sessionRuntimeClient) consumeRuntimeReadModelStale() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	stale := c.readModelStale
	c.readModelStale = false
	return stale
}
