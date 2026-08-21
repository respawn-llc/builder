package registry

import (
	"core/server/runtimeview"
	"core/shared/clientui"
	"core/shared/textutil"
)

func (r *RuntimeRegistry) RuntimeMainViewSnapshot(sessionID string) (clientui.RuntimeMainView, bool) {
	entry := r.authorityEntryBySession(sessionID)
	if entry == nil {
		return clientui.RuntimeMainView{}, false
	}
	view := entry.mainView.Load()
	if view == nil {
		return clientui.RuntimeMainView{}, false
	}
	return cloneRuntimeMainView(*view), true
}

func (r *RuntimeRegistry) publishTranscriptAndMainView(entry *authorityRuntimeEntry, build func() ([]clientui.TranscriptEvent, error)) error {
	entry.publicationMu.Lock()
	defer entry.publicationMu.Unlock()
	if err := entry.sessionFeed.PublishBuilt(build); err != nil {
		return err
	}
	view := entry.mainView.Load()
	if view == nil {
		return nil
	}
	return r.publishRuntimeMainViewLocked(entry, view.Version, view.Activity)
}

func (r *RuntimeRegistry) publishRuntimeMainViewLocked(entry *authorityRuntimeEntry, version clientui.ReadModelVersion, activity clientui.RuntimeActivity) error {
	view, err := runtimeview.MainViewFromRuntimeActivity(entry.engine, version, cloneRuntimeActivity(activity))
	if err != nil {
		return err
	}
	entry.mu.Lock()
	ready := entry.lifecycle == authorityRuntimeEntryReady
	entry.mu.Unlock()
	if !ready {
		return nil
	}
	view = cloneRuntimeMainView(view)
	entry.mainView.Store(&view)
	return nil
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
		goal.Goal = textutil.Pointer(view.Status.Goal.Goal)
		goal.Availability = textutil.Pointer(view.Status.Goal.Availability)
		cloned.Status.Goal = &goal
	}
	cloned.Status.WorkflowSession = textutil.Pointer(view.Status.WorkflowSession)
	return cloned
}

func cloneRuntimeActivity(activity clientui.RuntimeActivity) clientui.RuntimeActivity {
	cloned := activity
	cloned.ActiveStep = textutil.Pointer(activity.ActiveStep)
	return cloned
}
