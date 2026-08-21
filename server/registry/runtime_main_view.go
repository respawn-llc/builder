package registry

import (
	"strings"

	"core/server/runtimeview"
	"core/shared/clientui"
)

type runtimeMainViewPublication struct {
	update clientui.RuntimeReadModelUpdate
	view   clientui.RuntimeMainView
}

func (r *RuntimeRegistry) RuntimeMainViewSnapshot(sessionID string) (clientui.RuntimeMainView, bool) {
	if r == nil {
		return clientui.RuntimeMainView{}, false
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return clientui.RuntimeMainView{}, false
	}
	value, ok := r.mainViews.Load(id)
	if !ok {
		return clientui.RuntimeMainView{}, false
	}
	entry, ok := value.(*authorityRuntimeEntry)
	if !ok {
		panic("Runtime Main View index contains an invalid entry")
	}
	publication := entry.mainView.Load()
	if publication == nil {
		return clientui.RuntimeMainView{}, false
	}
	return publication.view, true
}

func (r *RuntimeRegistry) republishRuntimeMainView(entry *authorityRuntimeEntry) error {
	if r == nil || entry == nil {
		return nil
	}
	entry.publicationMu.Lock()
	defer entry.publicationMu.Unlock()
	publication := entry.mainView.Load()
	if publication == nil {
		return nil
	}
	return r.publishRuntimeMainViewLocked(entry, publication.update)
}

func (r *RuntimeRegistry) publishRuntimeMainViewLocked(
	entry *authorityRuntimeEntry,
	update clientui.RuntimeReadModelUpdate,
) error {
	view, err := runtimeview.MainViewFromRuntimeActivity(entry.engine, update.Version, update.Activity)
	if err != nil {
		return err
	}
	entry.mu.Lock()
	ready := entry.lifecycle == authorityRuntimeEntryReady
	entry.mu.Unlock()
	if !ready {
		return nil
	}
	entry.mainView.Store(&runtimeMainViewPublication{update: update, view: view})
	return nil
}

func cloneRuntimeReadModelUpdate(update clientui.RuntimeReadModelUpdate) clientui.RuntimeReadModelUpdate {
	cloned := update
	if update.Activity.ActiveStep != nil {
		active := *update.Activity.ActiveStep
		cloned.Activity.ActiveStep = &active
	}
	return cloned
}
