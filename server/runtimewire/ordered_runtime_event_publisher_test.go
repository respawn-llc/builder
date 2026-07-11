package runtimewire

import (
	"testing"

	"core/server/runtime"
)

type recordingOrderedRuntimeEventPublisher struct {
	published []runtime.Event
	onPublish func(runtime.Event)
}

func (p *recordingOrderedRuntimeEventPublisher) PublishRuntimeEventForEngine(_ string, _ *runtime.Engine, evt runtime.Event) {
	p.published = append(p.published, evt)
	if p.onPublish != nil {
		p.onPublish(evt)
	}
}

func TestOrderedRuntimeEventPublisherQueuesEventsArrivingDuringFlush(t *testing.T) {
	registry := &recordingOrderedRuntimeEventPublisher{}
	publisher := NewOrderedRuntimeEventPublisher("session-1", registry)
	publisher.Publish(runtime.Event{Kind: runtime.EventAssistantMessage, StepID: "first"})
	publisher.BindEngine(&runtime.Engine{})

	registry.onPublish = func(evt runtime.Event) {
		if evt.StepID == "first" {
			publisher.Publish(runtime.Event{Kind: runtime.EventToolCallStarted, StepID: "second"})
		}
	}
	publisher.FlushAfterResolve()
	publisher.Publish(runtime.Event{Kind: runtime.EventToolCallCompleted, StepID: "third"})

	if got, want := len(registry.published), 3; got != want {
		t.Fatalf("published count = %d, want %d", got, want)
	}
	for i, want := range []string{"first", "second", "third"} {
		if got := registry.published[i].StepID; got != want {
			t.Fatalf("published[%d].StepID = %q, want %q", i, got, want)
		}
	}
}
