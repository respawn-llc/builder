package app

import (
	"fmt"

	"core/shared/runtimeinput"
)

func pendingWorkInputs(pending runtimeinput.PendingWork) []ongoingLiveInput {
	out := make([]ongoingLiveInput, 0, len(pending.Items))
	for _, lane := range []runtimeinput.PendingWorkLane{runtimeinput.PendingWorkLaneQueue, runtimeinput.PendingWorkLaneSteer} {
		for _, item := range pending.Items {
			if item.Lane != lane {
				continue
			}
			disposition := ongoingLiveInputSteering
			if lane == runtimeinput.PendingWorkLaneQueue {
				disposition = ongoingLiveInputQueued
			}
			out = append(out, ongoingLiveInput{Text: pendingWorkItemText(item), Disposition: disposition})
		}
	}
	return out
}

func pendingWorkItemText(item runtimeinput.PendingWorkItem) string {
	switch item.Kind {
	case runtimeinput.PendingWorkItemKindMessage:
		if item.Message != nil {
			return item.Message.Text
		}
	case runtimeinput.PendingWorkItemKindManualCompaction:
		if item.ManualCompaction != nil {
			return item.ManualCompaction.RestorationInput
		}
	}
	panic(fmt.Sprintf("unsupported Pending Work item kind %q", item.Kind))
}
