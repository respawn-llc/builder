package registry

import (
	"strings"

	"core/server/runtimeview"
	"core/shared/clientui"
	"core/shared/textutil"
)

type runtimeMainViewCatalog struct {
	bySession map[string]clientui.RuntimeMainView
}

func (r *RuntimeRegistry) RuntimeMainViewSnapshot(sessionID string) (clientui.RuntimeMainView, bool) {
	if r == nil {
		return clientui.RuntimeMainView{}, false
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return clientui.RuntimeMainView{}, false
	}
	catalog := r.mainViews.Load()
	if catalog == nil {
		return clientui.RuntimeMainView{}, false
	}
	view, ok := catalog.bySession[id]
	if !ok {
		return clientui.RuntimeMainView{}, false
	}
	return cloneRuntimeMainView(view), true
}

func (r *RuntimeRegistry) republishRuntimeMainView(entry *authorityRuntimeEntry) error {
	if r == nil || entry == nil {
		return nil
	}
	entry.publicationMu.Lock()
	defer entry.publicationMu.Unlock()
	readModel := entry.readModel.Load()
	if readModel == nil {
		return nil
	}
	return r.publishRuntimeMainViewLocked(entry, readModel.Version, readModel.Activity)
}

func (r *RuntimeRegistry) publishRuntimeMainViewLocked(
	entry *authorityRuntimeEntry,
	version clientui.ReadModelVersion,
	activity clientui.RuntimeActivity,
) error {
	view, err := runtimeview.MainViewFromRuntimeActivity(entry.engine, version, cloneRuntimeActivity(activity))
	if err != nil {
		return err
	}
	sessionID := entry.ref.SessionID().String()
	entry.mu.Lock()
	ready := entry.lifecycle == authorityRuntimeEntryReady
	entry.mu.Unlock()
	if !ready {
		return nil
	}
	r.mainViewCatalogMu.Lock()
	defer r.mainViewCatalogMu.Unlock()
	currentCatalog := r.mainViews.Load()
	if currentCatalog == nil {
		currentCatalog = &runtimeMainViewCatalog{bySession: make(map[string]clientui.RuntimeMainView)}
	}
	next := make(map[string]clientui.RuntimeMainView, len(currentCatalog.bySession)+1)
	for id, existing := range currentCatalog.bySession {
		next[id] = existing
	}
	next[sessionID] = view
	r.mainViews.Store(&runtimeMainViewCatalog{bySession: next})
	return nil
}

func (r *RuntimeRegistry) removeRuntimeMainView(entry *authorityRuntimeEntry) {
	if r == nil || entry == nil {
		return
	}
	sessionID := entry.ref.SessionID().String()
	entry.publicationMu.Lock()
	defer entry.publicationMu.Unlock()
	r.mainViewCatalogMu.Lock()
	defer r.mainViewCatalogMu.Unlock()
	current := r.mainViews.Load()
	if current == nil {
		return
	}
	if _, ok := current.bySession[sessionID]; !ok {
		return
	}
	next := make(map[string]clientui.RuntimeMainView, len(current.bySession)-1)
	for id, existing := range current.bySession {
		if id != sessionID {
			next[id] = existing
		}
	}
	r.mainViews.Store(&runtimeMainViewCatalog{bySession: next})
}

func cloneRuntimeMainView(view clientui.RuntimeMainView) clientui.RuntimeMainView {
	cloned := view
	cloned.Activity = cloneRuntimeActivity(view.Activity)
	cloned.Session.AgentRole = textutil.Pointer(view.Session.AgentRole)
	cloned.Session.ExecutionTarget = clientui.NormalizeSessionExecutionTarget(view.Session.ExecutionTarget)
	cloned.Status.PreviousSessionID = textutil.Pointer(view.Status.PreviousSessionID)
	cloned.Status.ParentAgentSessionID = textutil.Pointer(view.Status.ParentAgentSessionID)
	cloned.Status.NavigationTargetSessionID = textutil.Pointer(view.Status.NavigationTargetSessionID)
	cloned.Status.LastCommittedAssistantFinalAnswer = textutil.Pointer(view.Status.LastCommittedAssistantFinalAnswer)
	if view.Status.Goal != nil {
		goal := *view.Status.Goal
		if view.Status.Goal.Goal != nil {
			core := *view.Status.Goal.Goal
			goal.Goal = &core
		}
		if view.Status.Goal.Availability != nil {
			availability := *view.Status.Goal.Availability
			goal.Availability = &availability
		}
		cloned.Status.Goal = &goal
	}
	if view.Status.WorkflowSession != nil {
		workflow := *view.Status.WorkflowSession
		cloned.Status.WorkflowSession = &workflow
	}
	return cloned
}

func cloneRuntimeActivity(activity clientui.RuntimeActivity) clientui.RuntimeActivity {
	cloned := activity
	if activity.ActiveStep != nil {
		active := *activity.ActiveStep
		cloned.ActiveStep = &active
	}
	return cloned
}

func cloneRuntimeReadModelUpdate(update clientui.RuntimeReadModelUpdate) clientui.RuntimeReadModelUpdate {
	cloned := update
	cloned.Activity = cloneRuntimeActivity(update.Activity)
	return cloned
}
