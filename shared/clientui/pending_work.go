package clientui

import "core/shared/runtimeinput"

type TranscriptPendingWorkReplaced struct {
	PendingWork runtimeinput.PendingWork
}

func (r TranscriptPendingWorkReplaced) Validate() error {
	return r.PendingWork.Validate()
}

type TranscriptPendingWorkRestored struct {
	Restoration runtimeinput.PendingWorkTechnicalRestoration
}

func (r TranscriptPendingWorkRestored) Validate() error {
	return r.Restoration.Validate()
}
