package registry

import (
	"strings"

	"core/server/runtimeview"
	"core/shared/clientui"
)

func (r *RuntimeRegistry) RuntimeMainViewSnapshot(sessionID string) (clientui.RuntimeMainView, bool) {
	if r == nil {
		return clientui.RuntimeMainView{}, false
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return clientui.RuntimeMainView{}, false
	}
	entry := r.authorityEntryBySession(id)
	if entry == nil {
		return clientui.RuntimeMainView{}, false
	}
	view := entry.mainView.Load()
	if view == nil {
		return clientui.RuntimeMainView{}, false
	}
	return *view, true
}

func (r *RuntimeRegistry) republishRuntimeMainView(entry *authorityRuntimeEntry) error {
	if r == nil || entry == nil {
		return nil
	}
	view := entry.mainView.Load()
	if view == nil {
		return nil
	}
	return r.publishRuntimeMainView(entry, view.Version, view.Activity)
}

func (r *RuntimeRegistry) publishRuntimeMainView(
	entry *authorityRuntimeEntry,
	version clientui.ReadModelVersion,
	activity clientui.RuntimeActivity,
) error {
	if activity.ActiveStep != nil {
		active := *activity.ActiveStep
		activity.ActiveStep = &active
	}
	view, err := runtimeview.MainViewFromRuntimeActivity(entry.engine, version, activity)
	if err != nil {
		return err
	}
	entry.mu.Lock()
	ready := entry.lifecycle == authorityRuntimeEntryReady
	entry.mu.Unlock()
	if !ready {
		return nil
	}
	entry.mainView.Store(&view)
	return nil
}
