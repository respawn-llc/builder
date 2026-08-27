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
	return c.mergeMainViewCandidate(view, runtimeTupleIngressAuthoritativeSnapshot, nil).view
}

func (c *sessionRuntimeClient) mergeMainViewCandidate(
	view clientui.RuntimeMainView,
	ingress runtimeTupleIngress,
	metadataBaselineRevision *uint64,
) runtimeTupleMergeResult {
	if view.Session.SessionID == "" {
		view.Session.SessionID = c.sessionID
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	decision := decideRuntimeTuple(c.mainView.Version, view.Version, ingress)
	if metadataBaselineRevision == nil || c.metadataRevision == *metadataBaselineRevision {
		if c.mainView.Session.SessionID == view.Session.SessionID {
			view.Status.ContextUsage = mergeRuntimeContextUsagePolicy(
				c.mainView.Status.ContextUsage,
				view.Status.ContextUsage,
			)
		}
		c.mainView.Status = view.Status
		c.mainView.Session = view.Session
		c.advanceMetadataRevision()
	}
	if decision == runtimeTupleApply {
		applyRuntimeTuple(&c.mainView, runtimeTupleFromMainView(view))
	}
	c.hasMainView = true
	return runtimeTupleMergeResult{decision: decision, view: c.mainView, project: decision == runtimeTupleApply}
}

func (c *sessionRuntimeClient) mainViewMetadataRevision() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.metadataRevision
}

func (c *sessionRuntimeClient) advanceMetadataRevision() {
	if c.metadataRevision == ^uint64(0) {
		panic("runtime main-view metadata revision overflow")
	}
	c.metadataRevision++
}

func (c *sessionRuntimeClient) patchMainView(apply func(view *clientui.RuntimeMainView)) {
	c.mu.Lock()
	apply(&c.mainView)
	if c.mainView.Session.SessionID == "" {
		c.mainView.Session.SessionID = c.sessionID
	}
	c.hasMainView = true
	c.advanceMetadataRevision()
	c.mu.Unlock()
}
