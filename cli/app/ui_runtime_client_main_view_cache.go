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
	metadataAccepted := metadataBaselineRevision == nil || c.metadataRevision == *metadataBaselineRevision
	previousGoal := c.mainView.Status.Goal
	if metadataAccepted {
		c.mainView.Status = view.Status
		c.mainView.Session = view.Session
		if !runtimeGoalsEqual(previousGoal, c.mainView.Status.Goal) {
			c.clearGoalMutationPendingLocked()
		}
		c.advanceMetadataRevision()
	}
	if decision == runtimeTupleApply {
		applyRuntimeTuple(&c.mainView, runtimeTupleFromMainView(view))
	}
	c.hasMainView = true
	return runtimeTupleMergeResult{decision: decision, view: c.mainView, project: decision == runtimeTupleApply}
}

func runtimeGoalsEqual(left, right *clientui.RuntimeGoal) bool {
	if left == right {
		return true
	}
	if left == nil || right == nil {
		return false
	}
	if left.Availability != right.Availability || left.Suspended != right.Suspended {
		return false
	}
	if left.Goal == nil || right.Goal == nil {
		return left.Goal == right.Goal
	}
	return left.ID == right.ID &&
		left.Objective == right.Objective &&
		left.Status == right.Status &&
		left.CreatedAt.Equal(right.CreatedAt) &&
		left.UpdatedAt.Equal(right.UpdatedAt)
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
