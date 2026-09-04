package clientui

import "core/shared/runtimeinput"

type TranscriptPendingWorkChanged struct{}

func (TranscriptPendingWorkChanged) Validate() error { return nil }

type TranscriptPendingWorkRestored struct {
	Restoration runtimeinput.PendingWorkTechnicalRestoration
}

func (r TranscriptPendingWorkRestored) Validate() error {
	return r.Restoration.Validate()
}
