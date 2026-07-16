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
	return c.mergeMainViewCandidate(view, runtimeTupleIngressAuthoritativeSnapshot).view
}

func (c *sessionRuntimeClient) mergeMainViewCandidate(
	view clientui.RuntimeMainView,
	ingress runtimeTupleIngress,
) runtimeTupleMergeResult {
	if view.Session.SessionID == "" {
		view.Session.SessionID = c.sessionID
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	decision := decideRuntimeTuple(c.mainView.Version, view.Version, ingress)
	c.mainView.Status = view.Status
	c.mainView.Session = view.Session
	if decision == runtimeTupleApply {
		applyRuntimeTuple(&c.mainView, runtimeTupleFromMainView(view))
	}
	c.hasMainView = true
	return runtimeTupleMergeResult{decision: decision, view: c.mainView, project: decision == runtimeTupleApply}
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
