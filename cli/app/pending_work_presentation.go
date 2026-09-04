package app

import "core/shared/runtimeinput"

func pendingWorkInputs(pending runtimeinput.PendingWork) []ongoingLiveInput {
	out := make([]ongoingLiveInput, 0, len(pending.Items))
	for _, item := range pending.Items {
		disposition := ongoingLiveInputSteering
		if item.Lane == runtimeinput.PendingWorkLaneQueue {
			disposition = ongoingLiveInputQueued
		}
		out = append(out, ongoingLiveInput{Text: item.CanonicalInput, Disposition: disposition})
	}
	return out
}
