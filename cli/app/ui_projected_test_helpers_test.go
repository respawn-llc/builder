package app

import (
	"testing"
	"time"

	"core/server/registry"
	"core/server/runtime"
	"core/server/runtimecontrol"
	"core/server/runtimeview"
	"core/server/sessionview"
	"core/shared/client"
	"core/shared/clientui"
)

func closedProjectedRuntimeEvents() <-chan clientui.Event {
	ch := make(chan clientui.Event)
	close(ch)
	return ch
}

func closedAskEvents() <-chan askEvent {
	ch := make(chan askEvent)
	close(ch)
	return ch
}

func waitForTestCondition(t *testing.T, timeout time.Duration, label string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", label)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newProjectedTestUIModel(runtimeClient clientui.RuntimeClient, runtimeEvents <-chan clientui.Event, askEvents <-chan askEvent, opts ...UIOption) *uiModel {
	if runtimeEvents == nil {
		runtimeEvents = make(chan clientui.Event)
	}
	if askEvents == nil {
		askEvents = make(chan askEvent)
	}
	m := NewProjectedUIModel(runtimeClient, runtimeEvents, askEvents, opts...).(*uiModel)
	seedTestRollbackTargets(m)
	return m
}

func newProjectedClosedUIModel(runtimeClient clientui.RuntimeClient, opts ...UIOption) *uiModel {
	return newProjectedTestUIModel(runtimeClient, closedProjectedRuntimeEvents(), closedAskEvents(), opts...)
}

func newProjectedRuntimeEventsUIModel(runtimeClient clientui.RuntimeClient, runtimeEvents <-chan clientui.Event, opts ...UIOption) *uiModel {
	return newProjectedTestUIModel(runtimeClient, runtimeEvents, closedAskEvents(), opts...)
}

func newSizedProjectedClosedUIModel(runtimeClient clientui.RuntimeClient, width, height int, opts ...UIOption) *uiModel {
	return sizedTestUIModel(newProjectedClosedUIModel(runtimeClient, opts...), width, height)
}

func newSizedProjectedRuntimeEventsUIModel(runtimeClient clientui.RuntimeClient, runtimeEvents <-chan clientui.Event, width, height int, opts ...UIOption) *uiModel {
	return sizedTestUIModel(newProjectedRuntimeEventsUIModel(runtimeClient, runtimeEvents, opts...), width, height)
}

func setTestUITerminalSize(m *uiModel, width, height int) *uiModel {
	m.termWidth = width
	m.termHeight = height
	return m
}

func sizedTestUIModel(m *uiModel, width, height int) *uiModel {
	m = setTestUITerminalSize(m, width, height)
	m.windowSizeKnown = true
	return m
}

func newProjectedStaticUIModel(opts ...UIOption) *uiModel {
	return newProjectedTestUIModel(nil, nil, nil, opts...)
}

func newProjectedEngineUIModel(engine *runtime.Engine, opts ...UIOption) *uiModel {
	return newProjectedTestUIModel(newUIRuntimeClient(engine), nil, nil, opts...)
}

func newUIRuntimeClientFromEngine(engine *runtime.Engine) clientui.RuntimeClient {
	if engine == nil {
		return nil
	}
	resolver := sessionview.NewStaticRuntimeResolver(engine)
	reads := client.NewLoopbackSessionViewClient(sessionview.NewService(nil, resolver, nil))
	controlRegistry := registry.NewRuntimeRegistry()
	controlRegistry.Register(engine.SessionID(), engine)
	controls := client.NewLoopbackRuntimeControlClient(runtimecontrol.NewService(controlRegistry))
	runtimeClient := newUIRuntimeClientWithReads(engine.SessionID(), reads, controls).(*sessionRuntimeClient)
	runtimeClient.storeMainView(runtimeview.MainViewFromRuntime(engine))
	return runtimeClient
}

func newUIRuntimeClient(engine *runtime.Engine) clientui.RuntimeClient {
	return newUIRuntimeClientFromEngine(engine)
}

func newTestSessionRuntimeClient(reads client.SessionViewClient, controls client.RuntimeControlClient) *sessionRuntimeClient {
	return newUIRuntimeClientWithReads("session-1", reads, controls).(*sessionRuntimeClient)
}

func newTestSessionRuntimeClientWithControls(controls client.RuntimeControlClient) *sessionRuntimeClient {
	return newTestSessionRuntimeClient(&countingSessionViewClient{}, controls)
}
