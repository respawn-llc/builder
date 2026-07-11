package app

import (
	"core/server/runtime"
	"core/server/runtimeview"
	"core/shared/clientui"
)

func projectRuntimeEvent(evt runtime.Event) clientui.Event {
	return runtimeview.EventFromRuntime(evt)
}

func projectedRuntimeEventMsg(evt runtime.Event) runtimeEventMsg {
	return runtimeEventMsg{event: projectRuntimeEvent(evt)}
}

func projectRuntimeEventChannel(src <-chan runtime.Event, gaps <-chan struct{}, stop <-chan struct{}) <-chan clientui.Event {
	if src == nil {
		return nil
	}
	out := make(chan clientui.Event, cap(src))
	go func() {
		defer close(out)
		for {
			select {
			case <-stop:
				return
			case _, ok := <-gaps:
				if !ok && gaps != nil {
					gaps = nil
					continue
				}
				if !publishProjectedRuntimeEvent(stop, out, clientui.Event{Kind: clientui.EventStreamGap, RecoveryCause: clientui.TranscriptRecoveryCauseStreamGap}) {
					return
				}
			case evt, ok := <-src:
				if !ok {
					return
				}
				if !publishProjectedRuntimeEvent(stop, out, projectRuntimeEvent(evt)) {
					return
				}
			}
		}
	}()
	return out
}

func publishProjectedRuntimeEvent(stop <-chan struct{}, out chan<- clientui.Event, evt clientui.Event) bool {
	select {
	case <-stop:
		return false
	case out <- evt:
		return true
	}
}

func (a uiRuntimeAdapter) handleRuntimeEvent(evt runtime.Event) {
	a.applyProjectedRuntimeEvent(projectRuntimeEvent(evt))
}
