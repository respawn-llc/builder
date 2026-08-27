package clientui

import "core/shared/runtimeinput"

type TranscriptPendingWorkReplaced struct {
	PendingWork runtimeinput.PendingWork
}

func (r TranscriptPendingWorkReplaced) Validate() error {
	return r.PendingWork.Validate()
}
