package registry

import (
	"strings"

	"core/server/runtimeview"
	"core/shared/clientui"
)

type runtimeMainViewCatalog struct {
	bySession map[string]*authorityRuntimeEntry
}

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
	catalog := r.mainViews.Load()
	if catalog == nil {
		return clientui.RuntimeMainView{}, false
	}
	entry := catalog.bySession[id]
	if entry == nil {
		return clientui.RuntimeMainView{}, false
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

func (r *RuntimeRegistry) addRuntimeMainViewEntry(entry *authorityRuntimeEntry) {
	r.mainViewCatalogMu.Lock()
	defer r.mainViewCatalogMu.Unlock()
	currentCatalog := r.mainViews.Load()
	if currentCatalog == nil {
		currentCatalog = &runtimeMainViewCatalog{bySession: make(map[string]*authorityRuntimeEntry)}
	}
	next := make(map[string]*authorityRuntimeEntry, len(currentCatalog.bySession)+1)
	for id, existing := range currentCatalog.bySession {
		next[id] = existing
	}
	next[entry.ref.SessionID().String()] = entry
	r.mainViews.Store(&runtimeMainViewCatalog{bySession: next})
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
	next := make(map[string]*authorityRuntimeEntry, len(current.bySession)-1)
	for id, existing := range current.bySession {
		if id != sessionID {
			next[id] = existing
		}
	}
	r.mainViews.Store(&runtimeMainViewCatalog{bySession: next})
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
