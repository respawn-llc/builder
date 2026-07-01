package app

import "core/shared/clientui"

// turnQueueHook observes projected runtime events and the user-facing queue
// lifecycle so features can react when a queued turn sequence truly finishes.
type turnQueueHook interface {
	OnProjectedRuntimeEvent(evt clientui.Event)
	OnTurnQueueDrained()
	OnTurnQueueAborted()
	OnUserCompactionCompleted(queueDrained bool)
}

type askNotificationHook interface {
	OnAsk(req clientui.PendingPromptEvent)
}
