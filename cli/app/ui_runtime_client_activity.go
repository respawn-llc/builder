package app

import "core/shared/clientui"

type runtimeActivitySnapshotPatch struct {
	Version             clientui.ReadModelVersion
	Activity            clientui.RuntimeActivity
	InputReconciliation clientui.RuntimeInputReconciliationSnapshot
}

func (c *sessionRuntimeClient) patchVersionedRuntimeActivity(snapshot runtimeActivitySnapshotPatch) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch decideRuntimeActivitySnapshotCache(c.mainView.Version, snapshot.Version) {
	case runtimeActivitySnapshotCacheApply:
		view := &c.mainView
		view.Version = snapshot.Version
		view.Activity = snapshot.Activity
		view.InputReconciliation = snapshot.InputReconciliation
		if view.Session.SessionID == "" {
			view.Session.SessionID = c.sessionID
		}
		c.hasMainView = true
	case runtimeActivitySnapshotCacheRefresh:
		c.readModelStale = true
	}
}

type runtimeActivitySnapshotCacheDecision uint8

const (
	runtimeActivitySnapshotCacheIgnore runtimeActivitySnapshotCacheDecision = iota
	runtimeActivitySnapshotCacheApply
	runtimeActivitySnapshotCacheRefresh
)

func decideRuntimeActivitySnapshotCache(current clientui.ReadModelVersion, incoming clientui.ReadModelVersion) runtimeActivitySnapshotCacheDecision {
	if incoming.Validate() != nil {
		return runtimeActivitySnapshotCacheIgnore
	}
	if current.Validate() != nil {
		return runtimeActivitySnapshotCacheApply
	}
	if incoming.Epoch != current.Epoch {
		return runtimeActivitySnapshotCacheRefresh
	}
	if incoming.Generation != current.Generation {
		if incoming.Generation > current.Generation {
			return runtimeActivitySnapshotCacheRefresh
		}
		return runtimeActivitySnapshotCacheIgnore
	}
	if incoming.Sequence <= current.Sequence {
		return runtimeActivitySnapshotCacheIgnore
	}
	return runtimeActivitySnapshotCacheApply
}

func shouldApplyRuntimeActivitySnapshot(current clientui.ReadModelVersion, incoming clientui.ReadModelVersion) bool {
	return decideRuntimeActivitySnapshotCache(current, incoming) == runtimeActivitySnapshotCacheApply
}
