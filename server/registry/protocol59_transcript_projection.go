package registry

import (
	"core/server/runtimeactivity"
	"core/server/runtimefeed"
	"core/shared/clientui"
)

type protocol59TranscriptReadModel struct {
	activity       clientui.RuntimeActivity
	reconciliation clientui.RuntimeInputReconciliationSnapshot
}

func projectProtocol59TranscriptReadModel(update runtimefeed.RuntimeReadModelUpdate) protocol59TranscriptReadModel {
	return protocol59TranscriptReadModel{
		activity:       runtimeactivity.Protocol59RuntimeActivity(update.Activity),
		reconciliation: runtimeactivity.Protocol59InputReconciliation(update.Version, update.InputReconciliation),
	}
}

func (p protocol59TranscriptReadModel) messages() []clientui.TranscriptMessage {
	return []clientui.TranscriptMessage{
		{Kind: clientui.TranscriptMessageRuntimeActivity, RuntimeActivity: &p.activity},
		{Kind: clientui.TranscriptMessageInputReconciliation, InputReconciliation: &p.reconciliation},
	}
}

func (p protocol59TranscriptReadModel) applyToHydration(hydration *clientui.TranscriptHydration) {
	if hydration == nil {
		return
	}
	activity := p.activity
	reconciliation := p.reconciliation
	if p.reconciliation.Operations != nil {
		reconciliation.Operations = append([]clientui.RuntimeInputReconciliation(nil), p.reconciliation.Operations...)
	}
	hydration.RuntimeActivity = &activity
	hydration.InputReconciliation = &reconciliation
}
